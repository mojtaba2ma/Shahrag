/* The topology map — the whole server's routing in one picture.

   Four columns, left to right in the direction traffic actually flows:

     internet  ->  ports  ->  hostnames/paths  ->  backends

   Drawn as an inline SVG with no library. That is not stubbornness: assets
   are go:embed'ed into the binary, there is no build step and no CDN, and a
   charting library would be several hundred kilobytes to draw twenty
   rectangles and some curves. The whole file is a few KB and renders the
   same in the sandboxed preview as it does in a browser.

   RTL is handled by MIRRORING the whole drawing rather than by rewriting the
   layout. Traffic flowing "inward" reads left-to-right in English and
   right-to-left in Persian, so the columns are laid out once and the SVG is
   flipped with a transform; the labels are then flipped back individually so
   the text stays readable. Doing it the other way — recomputing every
   coordinate per direction — is where mirrored diagrams usually go wrong.

   The animation is deliberately restrained: dots travelling along the paths
   at a constant, slow rate. It is there to make the DIRECTION of flow
   obvious at a glance, not to imply live throughput — animating dot speed
   from real traffic would be a lie at any sensible refresh rate, and a busy
   diagram is harder to read than a still one. It stops entirely when the
   operator prefers reduced motion. */
window.Pages = window.Pages || {};

(function () {
"use strict";

/* Column width.
 *
 * 300, not 190. A real installation has names like
 * "kannb.sugerdood.com" and paths like "/Xp3IYReUB55CmT4J9RwS1t", and at
 * 190 both were truncated to an ellipsis — which defeats the point of a map
 * whose whole job is to tell you what is where. Measured against the
 * longest names in a real config rather than guessed.
 */
/* Column width.
 *
 * Back to 230 after 300 was reported as too big. The label area is what
 * actually needed the room, not the box: LABEL_PAD is small and the title
 * now gets the full width because the badge moved to its own line. Measured
 * against the longest real name, "kannb.sugerdood.com" at 19 characters. */
const COL_W = 230;
const GAP_X = 74;       // horizontal gap between columns
/* 52, not 44. At 44 the two text baselines (y+19 and y+31) leave the
   subtitle's descenders sitting on the bottom edge, and on a real render
   they were visibly clipped. Found by reading the screenshot, not the
   source — the numbers looked fine in the code. */
const BOX_H = 52;
const GAP_Y = 13;       // vertical gap between nodes
const PAD = 18;
/* How much of the box a label may use before it is truncated. The badge
   sits in the opposite corner, so the title has to stop short of it or the
   two overlap — which is exactly what "cdn.example.com" + "SNI" did. */
const LABEL_PAD = 12;
/* Room reserved for the badge in the opposite corner.

   38, not 34: "SNI" in the badge font is about 19 px wide plus the 13 px
   padding, and at 34 the longest hostnames still touched it. Measured from
   a real render, because the character-width estimate in fit() is
   deliberately approximate. */
/* The badge sits ABOVE the title now, on its own line, so the title gets
 * the full width. That is what lets a 19-character hostname fit in a 230px
 * box where it did not fit in 300 with the badge beside it. */
const BADGE_ROOM = 0;

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* Lay one column out vertically, centred on the tallest column so the
   picture is balanced rather than top-heavy. */
function layout(items, colIndex, totalHeight) {
  const h = items.length * BOX_H + Math.max(0, items.length - 1) * GAP_Y;
  const top = PAD + Math.max(0, (totalHeight - h) / 2);
  return items.map((it, i) => Object.assign({}, it, {
    x: PAD + colIndex * (COL_W + GAP_X),
    y: top + i * (BOX_H + GAP_Y),
    w: COL_W,
    h: BOX_H,
  }));
}

/* A cubic bezier between the right edge of one node and the left edge of
   another. Curves rather than straight lines because with a dozen edges the
   straight ones overlap into an unreadable star; curves separate visually
   even when they share endpoints. */
function edgePath(a, b, rtl, spread, fromEntry) {
  // Leave from the edge facing the destination, arrive at the edge facing
  // the source. In RTL those are the opposite sides.
  //
  // fromEntry makes the line START at the source's ENTRY edge instead of
  // its exit edge. That is what draws a fan from the point where traffic
  // arrives at a container out to the rules inside it — which is exactly
  // what happens: the connection lands once and is then tested against
  // each rule, so all those lines share one origin.
  const x1 = fromEntry ? (rtl ? a.x + a.w : a.x) : (rtl ? a.x : a.x + a.w);
  const x2 = rtl ? b.x + b.w : b.x;
  // `spread` fans parallel edges apart so N routes look like N lines.
  const y1 = a.y + a.h / 2 + (spread || 0);
  const y2 = b.y + b.h / 2 + (spread || 0);
  const dx = (x2 - x1) / 2;
  return `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`;
}

/* A container: the SNI stage or the HTTP stage.

   Drawn behind its nodes, with its own label band. The colour matches the
   nodes inside it — a tint of the same accent — so the grouping reads
   without a legend, and the fill stays light enough that the boxes on top
   remain legible. */
function groupSVG(box, label, cls, rtl) {
  if (!box) return "";
  const tx = rtl ? box.x + box.w - 12 : box.x + 12;
  return `<g class="mp-group mp-group-${cls}">
    <rect x="${box.x}" y="${box.y}" width="${box.w}" height="${box.h}" rx="13"></rect>
    <text class="mp-group-t" x="${tx}" y="${box.y + 17}"
          text-anchor="${rtl ? "end" : "start"}">${esc(label)}</text>
  </g>`;
}

/* Truncate a label to fit.

   SVG text does not wrap and does not clip, so an over-long hostname simply
   runs out of its box and across whatever is next to it. Measured in
   characters against an approximate advance width, which is enough because
   both fonts here are close to monospace at these sizes and the result is
   only ever "shorten it a bit more than strictly needed". */
function fit(text, pxAvailable, pxPerChar) {
  const max = Math.max(3, Math.floor(pxAvailable / pxPerChar));
  const s = String(text == null ? "" : text);
  return s.length <= max ? s : s.slice(0, max - 1) + "…";
}

/* Draw one node.

   RTL is handled by mirroring the LAYOUT — the x coordinate of every node
   and every edge endpoint — and never by transforming the elements. An
   earlier version counter-flipped each label with its own transform, which
   is the obvious idea and is wrong: the counter-flip has to be relative to
   the mirrored parent, the node groups were not inside that parent, and
   every label ended up drawn to the LEFT of its own box. It looked almost
   right at a glance and was completely broken on measurement.

   Mirroring coordinates cannot go wrong that way: a box at x is simply a
   box at (width - x - w), and the text inside it is ordinary text. */
function nodeSVG(n, rtl) {
  // A destination outside this server is marked, so the dashed border
  // can say "this traffic leaves the machine" without a legend entry.
  const cls = "mp-node mp-" + n.kind + (n.dim ? " mp-dim" : "") +
    (n.isRemote ? " mp-node-remote" : "") +
    (n.isNginxHop ? " mp-node-hop" : "");
  const flip = "";
  // Text is anchored INSIDE the box. In RTL the reading direction is
  // right-to-left, so a label starts at the box's right edge and is
  // anchored "end"; in LTR it starts at the left and is anchored "start".
  //
  // The x here is the box's own coordinate AFTER mirroring, which is why
  // this is now simple: nothing else in this function knows about
  // direction, and the anchor and the x always refer to the same edge.
  // Titles and subtitles are CENTRED. Left-aligned text in a mirrored
  // layout reads as ragged on one side and cramped on the other, and the
  // report was that it looked wrong; centring is direction-neutral and
  // needs no RTL special case at all.
  const tx = n.x + n.w / 2;
  const anchor = "middle";
  // The badge stays in the top corner, which is where it was and where it
  // reads well — the leading corner for the reading direction.
  const badgeX = rtl ? n.x + n.w - LABEL_PAD : n.x + LABEL_PAD;
  const badgeAnchor = rtl ? "end" : "start";

  // The title must stop short of the badge in the opposite corner, or the
  // two overlap — which is what "cdn.example.com" and "SNI" did.
  const titleRoom = n.w - LABEL_PAD * 2;
  const subRoom = n.w - LABEL_PAD * 2;

  const sub = n.sub
    ? `<text class="mp-sub" x="${tx}" y="${n.y + 41}" text-anchor="${anchor}"
         >${esc(fit(n.sub, subRoom, 5.9))}</text>`
    : "";
  const badge = n.badge
    ? `<text class="mp-badge-t" x="${badgeX}" y="${n.y + 20}"
         text-anchor="${badgeAnchor}">${esc(n.badge)}</text>`
    : "";
  // A title tooltip carries the untruncated value, so nothing is actually
  // lost — it is just not painted over its neighbour.
  const full = n.sub ? n.label + " — " + n.sub : n.label;
  return `<g class="${cls}" ${flip}>
    <title>${esc(full)}</title>
    <rect x="${n.x}" y="${n.y}" width="${n.w}" height="${n.h}" rx="9"></rect>
    <text class="mp-title" x="${tx}" y="${n.y + (n.sub ? 29 : 32)}"
          text-anchor="${anchor}">${esc(fit(n.label, titleRoom, 7.2))}</text>
    ${sub}${badge}
  </g>`;
}

window.Pages.map = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;
    const rtl = document.documentElement.dir === "rtl";

    const topo = await api("/api/stats/topology");

    const svcByName = {};
    (topo.services || []).forEach(s => { svcByName[s.name] = s; });

    // ── Column 1: the ports the world can reach ──────────────
    // Every PUBLIC port belongs in the picture — the SNI-split ones and
    // the ordinary TLS ones alike. Only the internal fallback is left out,
    // because nothing reaches it from outside.
    //
    // The earlier version drew SNI ports only, so a port that splits by
    // Host and path appeared to have no way in at all.
    const ports = (topo.ports || []).filter(p => p.kind !== "http");
    const internal = (topo.ports || []).filter(p => p.kind === "http");

    const portNodes = ports.map(p => ({
      id: "port:" + p.port,
      kind: "port",
      label: String(p.port),
      sub: p.kind === "sni" ? t("map.kind_sni") : t("map.kind_https"),
      badge: p.kind === "sni" ? "SNI" : "TLS",
      port: p,
    }));
    if (!portNodes.length) {
      portNodes.push({ id: "port:none", kind: "port", label: t("map.no_ports"), dim: true });
    }

    // ── Column 2: how a connection is split ──────────────────
    // SNI names first (they are decided before nginx's http block even
    // sees the connection), then hostname+path routes.
    const routeNodes = [];
    const seenRoute = {};
    // SNI rules are only drawn when SNI routing is actually ON. With it
    // off the stream module is not in the path at all: the rules are
    // stored but inert, and drawing them would show a stage that does not
    // exist. Reported from a real install after switching it off.
    if (topo.reality_enabled) {
      (topo.reality_services || []).forEach(rs => {
        if (rs.disabled) return;
        routeNodes.push({
          id: "sni:" + rs.name, kind: "sni",
          label: rs.sni || rs.name, sub: t("map.split_sni"),
          badge: "SNI", svc: rs, viaSNI: true,
        });
      });
    }
    (topo.services || []).forEach(s => {
      const binds = s.bindings && s.bindings.length ? s.bindings : [{ fqdn: "*" }];
      binds.forEach(b => {
        const path = s.path ? "/" + String(s.path).replace(/^\//, "") : "/";
        const key = b.fqdn + path;
        if (seenRoute[key]) return;
        seenRoute[key] = true;
        routeNodes.push({
          id: "route:" + key, kind: "route",
          label: b.fqdn, sub: path,
          // "HTTP" rather than a padlock. The badge says HOW the request
          // is split — by Host and path, in nginx's http block — which is
          // the counterpart to the "SNI" badge above it. A padlock said
          // something else entirely (that a certificate exists) in the
          // one place the operator is trying to read the routing.
          badge: "HTTP",
          cert: !!b.has_cert,
          svcName: s.name, dim: s.disabled,
        });
      });
    });
    if (!routeNodes.length) {
      routeNodes.push({ id: "route:none", kind: "route", label: t("map.no_routes"), dim: true });
    }

    // ── Column 3: where it ends up ───────────────────────────
    const backNodes = [];
    const seenBack = {};
    /* The identity of a backend node.

       Defined once and used by BOTH the node builder and the edge builder.
       It was previously computed separately in each, from slightly
       different inputs, and the two drifted: a passthrough node was created
       under one key and looked up under another, so it appeared on the map
       with nothing connected to it. Found by reading the render, not the
       source. */
    const backKey = (svc) => {
      const port = svc.local_port || 0;
      // Every passthrough shares the same literal target, so keying by
      // target would collapse them all into one node. Key by service.
      if (svc.passthrough) return "pass:" + svc.name + ":" + port;
      const target = svc.target && svc.target !== "127.0.0.1" && svc.target !== "localhost"
        ? svc.target : "127.0.0.1";
      return target + ":" + port;
    };

    const addBack = (svc, isSNI) => {
      const target = svc.target && svc.target !== "127.0.0.1" && svc.target !== "localhost"
        ? svc.target : "127.0.0.1";
      const port = svc.local_port || 0;
      const key = backKey(svc);
      if (seenBack[key]) return key;
      seenBack[key] = true;
      // Local or remote decides which group this box belongs to in the
      // third column, and therefore what colour reaches it.
      const isLocal = !svc.passthrough &&
        (target === "127.0.0.1" || target === "localhost");
      backNodes.push({
        id: "back:" + key, kind: svc.passthrough ? "pass" : "backend",
        isLocal: isLocal, isRemote: !isLocal,
        // The service NAME is the headline: a bare port number tells the
        // operator nothing about what is listening on it, and "which
        // service is that?" is the question this column exists to answer.
        label: svc.name || (port ? String(port) : target),
        sub: svc.passthrough
          ? t("map.passthrough")
          : (port ? target + ":" + port : target),
        badge: svc.is_panel ? t("map.panel") : "",
        dim: svc.disabled, isSNI,
      });
      return key;
    };
    if (topo.reality_enabled) {
      (topo.reality_services || []).forEach(rs => { if (!rs.disabled) addBack(rs, true); });
    }
    (topo.services || []).forEach(s => addBack(s, false));
    if (!backNodes.length) {
      backNodes.push({ id: "back:none", kind: "backend", label: t("map.no_backends"), dim: true });
    }

    /* ── Grouping ────────────────────────────────────────────

       The two kinds of split are wrapped in their own container, because
       the ORDER matters and was not visible before: a connection is split
       by TLS server name first, in the stream module, and only what is
       destined for our own http block is then split again by host and
       path. Drawing both as one undifferentiated column implied they were
       alternatives at the same level, which is wrong.

       The SNI container disappears entirely when SNI routing is off, and
       the ports then connect straight to the HTTP container — which is
       exactly what the config does in that case. */
    const sniNodes = routeNodes.filter(r => r.viaSNI);
    const httpNodes = routeNodes.filter(r => !r.viaSNI);
    const sniOn = !!topo.reality_enabled && sniNodes.length > 0;

    /* The hop from the SNI stage into nginx's own http block, drawn as a
       box INSIDE the SNI container rather than as a bare line leaving it.

       That is what the config actually does: the stream module matches an
       SNI, and everything that matched no passthrough rule is proxied to
       127.0.0.1:<http_port>, which is a destination exactly like any
       other SNI rule's. Drawing it as a line made that destination
       invisible, so the http port — the one number an operator needs when
       the fallback breaks — appeared nowhere on the map.

       Always the LAST row, and re-pinned to last whenever SNI services
       are added or removed, because it is not one of the rules: it is
       what happens after all of them. */
    const httpPortNum = topo.reality_http_port || 0;
    const nginxHTTPBox = sniOn ? {
      id: "sni:nginx-http", kind: "route", viaSNI: true, isNginxHop: true,
      label: t("map.nginx_http"),
      sub: httpPortNum ? "127.0.0.1:" + httpPortNum : "127.0.0.1",
      badge: "HTTP",
    } : null;
    // Appended after the rules, so it is last however many there are.
    const sniStageNodes = nginxHTTPBox ? sniNodes.concat([nginxHTTPBox]) : sniNodes;

    // ── Edges ────────────────────────────────────────────────
    const edges = [];
    /* Edges from a port go to the CONTAINER, not to the boxes inside it.

       The point the operator asked for: a connection enters on a port,
       passes THROUGH the SNI stage, and arrives at the HTTP stage. Drawing
       a line from a port into an individual SNI box implied the port was
       wired to that one rule; drawing it to the container's edge shows the
       stage. One line per real route, so the COUNT is visible too. */
    // Recorded by TAG and resolved after the geometry runs, so the edge
    // list does not need the boxes to exist yet.
    const edgeToBox = (fromId, _box, kind, tag) =>
      edges.push([fromId, "box:" + tag, kind]);

    /* Every inbound line lands on the OUTER container, and its colour
       says where that traffic is ultimately headed.

       When SNI splitting is on, a port's traffic always enters the SNI
       stage first — that is simply what the stream module does — but a
       connection destined for an ordinary HTTP service and one destined
       for a passthrough are different things arriving on the same wire,
       and the map has to show both. So a port with HTTP services draws a
       line coloured for HTTP, a port with SNI rules draws one coloured
       for SNI, and a port carrying both draws the two side by side. The
       gradient (see edgeGradients) starts in the colour of the stage the
       line is IN and ends in the colour of what it will become, so
       neither meaning is lost. */
    portNodes.forEach(pn => {
      if (!pn.port) return;
      // An https port with no service list still carries every route
      // whose service listens on it; without this those ports were drawn
      // with no outgoing edge and looked unused.
      const svcNames = (pn.port.services || []).length
        ? pn.port.services
        : (topo.services || [])
            .filter(s => !s.disabled && (s.listen_port || 443) === pn.port.port)
            .map(s => s.name);

      // The FIRST stage is the SNI container when SNI is on, otherwise
      // the HTTP container. Both are outer boxes, so the line always
      // terminates on a container edge and never on an inner rule.
      const firstTag = sniOn ? "sni" : "http";

      let sniCount = 0, httpCount = 0;
      svcNames.forEach(name => {
        if (sniNodes.some(r => r.svc && r.svc.name === name)) sniCount++;
        else if (httpNodes.some(r => r.svcName === name)) httpCount++;
      });

      // One line per route, so the count stays readable, and each line
      // carries the colour of its own destination.
      for (let i = 0; i < httpCount; i++) {
        edges.push([pn.id, "box:" + firstTag, sniOn ? "in-http" : "http"]);
      }
      for (let i = 0; i < sniCount; i++) {
        edges.push([pn.id, "box:" + firstTag, "sni"]);
      }
      // A port that carries nothing recognisable still gets one line, or
      // it looks unwired when it is merely unused.
      if (!sniCount && !httpCount) {
        edges.push([pn.id, "box:" + firstTag, "https"]);
      }
    });

    /* The SNI stage feeds the HTTP stage.

       Everything whose SNI matched no rule — and everything an SNI rule
       sends to our own http port — continues into the http block. One line
       per such route. When SNI is off this stage does not exist and the
       ports already connect straight to HTTP above. */
    /* The SNI stage feeds the HTTP stage — out of the Nginx-HTTP box.

       One line, in the HTTP colour, from that box's exit to the HTTP
       container's entry. It used to be N lines from container edge to
       container edge, which implied the SNI stage as a whole forwarded
       to HTTP; in reality exactly one destination does, and now that
       destination is a box you can point at. */
    if (sniOn && nginxHTTPBox) {
      edges.push([nginxHTTPBox.id, "box:http", "http"]);
    }

    /* Inside each container, the entry point fans out to every active
       rule, in that container's own colour.

       This is what the operator asked for and it is also more truthful
       than what was there: a connection arriving at the SNI stage is
       tested against every rule, and a request arriving at the HTTP
       stage is matched against every server/location. Drawing the fan
       shows that the rules are alternatives being chosen between, rather
       than a column of unconnected boxes. */
    sniStageNodes.forEach(r => {
      if (r.dim) return;
      edges.push(["box:sni", r.id, "sni-fan"]);
    });
    httpNodes.forEach(r => {
      if (r.dim) return;
      edges.push(["box:http", r.id, "http-fan"]);
    });
    /* A rule to its destination.

       The line keeps the colour of the STAGE the rule lives in, so an
       operator can follow one colour the whole way across: purple means
       "this was decided by SNI", green means "this was decided by host
       and path". A passthrough keeps its own dashed grey, because it is
       the one case where nginx does not look inside the traffic at all.

       Local destinations land on the LocalHost container rather than on
       their own box, and then fan out inside it — the same shape as the
       two stages, for the same reason. */
    routeNodes.forEach(r => {
      const svc = r.viaSNI ? r.svc : svcByName[r.svcName];
      if (!svc) return;
      const back = backNodes.find(b => b.id === "back:" + backKey(svc));
      if (!back) return;
      const colour = svc.passthrough ? "pass" : (r.viaSNI ? "sni" : "http");
      // Into the container for a local backend, straight to the box for
      // a remote one.
      edges.push([r.id, back.isLocal ? "box:local" : back.id, colour]);
    });

    /* Inside the LocalHost container, the entry fans out to each service
       in the HTTP colour, exactly as the operator described: the point
       where the route reaches the outer box is where the inner lines
       start. */
    backNodes.forEach(b => {
      if (!b.isLocal || b.dim) return;
      edges.push(["box:local", b.id, "http-fan"]);
    });

    /* Every backend must be reachable from somewhere.

       A node with no edge is a drawing bug, not information — it tells the
       operator nothing and looks like a mistake, which it is. Asserting it
       here in development terms: any orphan is logged so it shows up in the
       browser test's console-error check rather than being quietly pretty. */
    backNodes.forEach(b => {
      if (b.id === "back:none") return;
      if (!edges.some(e => e[1] === b.id)) {
        console.warn("topology map: backend " + b.id + " has no incoming edge");
      }
    });

    // ── Geometry ─────────────────────────────────────────────
    const tallest = Math.max(portNodes.length,
      sniStageNodes.length + httpNodes.length, backNodes.length);
    const innerH = tallest * BOX_H + Math.max(0, tallest - 1) * GAP_Y;
    // The middle column is laid out as two stacked groups rather than one
    // list, with a gap and a header band for each container.
    const GROUP_PAD = 16;      // inside a container box
    const GROUP_HEAD = 22;     // the container's own label band
    const GROUP_GAP = 26;      // between the two containers

    // sniStageNodes, not sniNodes: the Nginx-HTTP box is inside this
    // container and the container has to be tall enough to hold it.
    const sniH = sniOn
      ? GROUP_HEAD + GROUP_PAD * 2 +
        sniStageNodes.length * BOX_H + Math.max(0, sniStageNodes.length - 1) * GAP_Y
      : 0;
    const httpH = GROUP_HEAD + GROUP_PAD * 2 +
      Math.max(1, httpNodes.length) * BOX_H +
      Math.max(0, httpNodes.length - 1) * GAP_Y;
    const midH = sniH + (sniOn ? GROUP_GAP : 0) + httpH;

    const innerH2 = Math.max(innerH, midH);
    const midTop = PAD + Math.max(0, (innerH2 - midH) / 2);

    const midX = PAD + 1 * (COL_W + GAP_X);
    const sniBox = sniOn
      ? { x: midX - GROUP_PAD, y: midTop, w: COL_W + GROUP_PAD * 2, h: sniH }
      : null;
    const httpBox = {
      x: midX - GROUP_PAD,
      y: midTop + (sniOn ? sniH + GROUP_GAP : 0),
      w: COL_W + GROUP_PAD * 2,
      h: httpH,
    };

    const place = (list, boxTop) => list.map((it, i) => Object.assign({}, it, {
      x: midX,
      y: boxTop + GROUP_HEAD + GROUP_PAD + i * (BOX_H + GAP_Y),
      w: COL_W, h: BOX_H,
    }));

    /* The third column is grouped the same way the middle one is.

       Order, which the operator specified and which is also the order
       that keeps the lines from crossing: remote destinations reached
       through SNI at the TOP (they leave the SNI container, which is the
       upper one), then the LocalHost container, then remote destinations
       reached through HTTP at the bottom. Following a line never has to
       cross the column. */
    const localBacks = backNodes.filter(b => b.isLocal);
    const remoteSNI = backNodes.filter(b => !b.isLocal && b.isSNI);
    const remoteHTTP = backNodes.filter(b => !b.isLocal && !b.isSNI);

    const localH = localBacks.length
      ? GROUP_HEAD + GROUP_PAD * 2 + localBacks.length * BOX_H +
        Math.max(0, localBacks.length - 1) * GAP_Y
      : 0;
    const backH =
      remoteSNI.length * (BOX_H + GAP_Y) +
      localH + (localBacks.length ? GROUP_GAP : 0) +
      remoteHTTP.length * (BOX_H + GAP_Y);

    const innerH3 = Math.max(innerH2, backH);
    const backX = PAD + 2 * (COL_W + GAP_X);
    let by = PAD + Math.max(0, (innerH3 - backH) / 2);

    const placedRemoteSNI = remoteSNI.map((it, i) => Object.assign({}, it, {
      x: backX, y: by + i * (BOX_H + GAP_Y), w: COL_W, h: BOX_H }));
    by += remoteSNI.length * (BOX_H + GAP_Y);

    const localBox = localBacks.length
      ? { x: backX - GROUP_PAD, y: by, w: COL_W + GROUP_PAD * 2, h: localH }
      : null;
    const placedLocal = localBacks.map((it, i) => Object.assign({}, it, {
      x: backX,
      y: by + GROUP_HEAD + GROUP_PAD + i * (BOX_H + GAP_Y),
      w: COL_W, h: BOX_H }));
    if (localBacks.length) by += localH + GROUP_GAP;

    const placedRemoteHTTP = remoteHTTP.map((it, i) => Object.assign({}, it, {
      x: backX, y: by + i * (BOX_H + GAP_Y), w: COL_W, h: BOX_H }));

    const cols = [layout(portNodes, 0, innerH3),
                  place(sniStageNodes, sniOn ? sniBox.y : 0)
                    .concat(place(httpNodes, httpBox.y)),
                  placedRemoteSNI.concat(placedLocal, placedRemoteHTTP)];
    const all = [].concat.apply([], cols);

    // PAD*2 covers both margins; the +2 is for the node border, which is
    // drawn centred on the rectangle's edge and so spills half a pixel
    // outside it. Without that the last column's right border was clipped
    // by the viewBox — visible in a render, invisible in the code.
    const W = PAD * 2 + 3 * COL_W + 2 * GAP_X + 2;
    const H = PAD * 2 + Math.max(innerH, midH, backH);

    // In RTL the flow reads right-to-left, so every x is mirrored once,
    // here, and nothing downstream needs to know about direction.
    if (rtl) {
      all.forEach(n => { n.x = W - n.x - n.w; });
      [sniBox, httpBox, localBox].forEach(b => { if (b) b.x = W - b.x - b.w; });
    }

    const byId = {};
    all.forEach(n => { byId[n.id] = n; });

    const reduced = window.matchMedia
      && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    // Container boxes are edge endpoints too, so they join the lookup.
    if (sniOn) byId["box:sni"] = Object.assign({ id: "box:sni" }, sniBox);
    byId["box:http"] = Object.assign({ id: "box:http" }, httpBox);
    if (localBox) byId["box:local"] = Object.assign({ id: "box:local" }, localBox);

    /* Parallel edges between the same pair are fanned out vertically.

       Without this, N routes from one port to the HTTP stage draw N
       identical lines on top of each other and look like one — losing
       exactly the count the operator asked to see. */
    const pairSeen = {};
    // A fan starts inside its container, at the point traffic enters it.
    const isFan = k => k === "sni-fan" || k === "http-fan";
    const edgeSVG = edges.map(([a, b, kind], i) => {
      const na = byId[a], nb = byId[b];
      if (!na || !nb) return "";
      const pk = a + ">" + b;
      const idx = (pairSeen[pk] = (pairSeen[pk] || 0) + 1) - 1;
      const total = edges.filter(e => e[0] === a && e[1] === b).length;
      // Spread around the centre: -1, 0, +1 ... times a small step.
      const spread = total > 1 ? (idx - (total - 1) / 2) * Math.min(9, 40 / total) : 0;
      const d = edgePath(na, nb, rtl, isFan(kind) ? 0 : spread, isFan(kind));
      const dot = reduced ? "" : `
        <circle class="mp-dot mp-dot-${kind}" r="3">
          <animateMotion dur="${(2.8 + (i % 5) * 0.35).toFixed(2)}s"
            repeatCount="indefinite" path="${d}"
            begin="${(i * 0.31).toFixed(2)}s"></animateMotion>
        </circle>`;
      // An inbound line whose destination is an HTTP service, drawn
      // while it is still inside the SNI stage, is painted with a
      // gradient: it starts purple (where it IS) and ends green (what it
      // will become). One line, both facts, no third colour to learn.
      const stroke = kind === "in-http"
        ? ` stroke="url(#mp-grad-in-http${rtl ? "-rtl" : ""})"` : "";
      return `<path class="mp-edge mp-edge-${kind}" d="${d}"${stroke}></path>${dot}`;
    }).join("");

    /* The gradient definitions.

       Two of them, because a gradient runs left-to-right in user space
       and the whole drawing is mirrored under RTL — without the mirrored
       copy the colours would run backwards in Persian, which is exactly
       the class of bug the coordinate mirroring was introduced to avoid. */
    const gradSVG = `
      <defs>
        <linearGradient id="mp-grad-in-http" x1="0" y1="0" x2="1" y2="0">
          <stop offset="0%" class="mp-stop-sni"></stop>
          <stop offset="100%" class="mp-stop-http"></stop>
        </linearGradient>
        <linearGradient id="mp-grad-in-http-rtl" x1="1" y1="0" x2="0" y2="0">
          <stop offset="0%" class="mp-stop-sni"></stop>
          <stop offset="100%" class="mp-stop-http"></stop>
        </linearGradient>
      </defs>`;

    // Nothing is transformed: the coordinates were already mirrored above.
    const mirror = "";

    // The HTTP stage carries its port number, because it is configurable
    // and an operator reading the map should not have to go and look it up.
    const httpPort = topo.reality_http_port || 0;
    // Just "HTTP · 6038" and "SNI". The containers used to be labelled
    // "Split by SNI" and "Split by HTTP", which is three words to say
    // what the position in the diagram already says, and it pushed the
    // port number off the end of the band on a narrow screen.
    const httpLabel = httpPort
      ? t("map.stage_http") + " · " + httpPort
      : t("map.stage_http");

    const legend = [
      ["port", t("map.legend_port")],
      ["sni", t("map.legend_sni")],
      ["route", t("map.legend_route")],
      ["backend", t("map.legend_backend")],
      ["pass", t("map.legend_pass")],
    ].map(([k, label]) =>
      `<span class="mp-key"><i class="mp-sw mp-sw-${k}"></i>${label}</span>`).join("");

    const fallbackNote = internal.length
      ? `<p class="tiny muted">${t("map.fallback")}: <code dir="ltr">${internal[0].port}</code></p>`
      : "";

    container.innerHTML = `
      <div class="card">
        <div class="card-head">
          <h3 class="card-title">${Icons.svg("network", 16)} ${t("map.title")}</h3>
          <button class="btn btn-ghost btn-sm" id="mp-refresh">
            ${Icons.svg("refresh", 14)} <span class="btn-label">${t("stats.refresh")}</span>
          </button>
        </div>
        <p class="tiny muted">${t("map.lede")}</p>

        <!-- The headings are a plain grid, so the document direction
             already orders them to match the mirrored drawing. -->
        <div class="mp-heads">
          <span>${t("map.col_ports")}</span>
          <span>${t("map.col_routes")}</span>
          <span>${t("map.col_backends")}</span>
        </div>

        <div class="mp-scroll">
          <!-- direction="ltr" on the SVG itself.

               The page is dir="rtl" in Persian and Arabic, and an SVG
               INHERITS that: the browser then mirrors text placement
               inside it, on top of the coordinate mirroring this file
               already does. The result was every label drawn outside its
               own box — measured, not guessed. Since the layout above
               mirrors the coordinates explicitly, the SVG must be told
               not to do it a second time. -->
          <svg class="mp-svg" viewBox="0 0 ${W} ${H}"
               width="${W}" height="${H}" role="img" direction="ltr"
               aria-label="${esc(t("map.title"))}">
            ${gradSVG}
            <g>${groupSVG(sniBox, t("map.stage_sni"), "sni", rtl)}${
                 groupSVG(httpBox, httpLabel, "http", rtl)}${
                 groupSVG(localBox, t("map.localhost"), "local", rtl)}</g>
            <g ${mirror}>${edgeSVG}</g>
            <g>${all.map(n => nodeSVG(n, rtl)).join("")}</g>
          </svg>
        </div>

        <div class="mp-legend">${legend}</div>
        ${fallbackNote}
      </div>

      <div class="card">
        <h3 class="card-title">${Icons.svg("reality", 16)} ${t("map.sni_summary")}</h3>
        <p class="tiny muted">${t("map.sni_summary_help")}</p>
        <div class="table-wrap"><table class="data-table">
          <thead><tr>
            <th>${t("map.sni_name")}</th><th>${t("map.port")}</th>
            <th>${t("map.services")}</th><th>${t("map.target")}</th>
          </tr></thead>
          <tbody>${(topo.reality_services || []).length
            ? (topo.reality_services || []).map(rs => `
            <tr class="${rs.disabled ? "row-off" : ""}">
              <td class="mono tiny" dir="ltr">${esc(rs.sni || "—")}</td>
              <td class="mono" dir="ltr">${(rs.ports || []).join(", ") || "—"}</td>
              <td>${esc(rs.name)}${rs.disabled
                ? ` <span class="badge badge-off">${t("map.off")}</span>` : ""}</td>
              <td class="mono tiny" dir="ltr">${rs.passthrough
                ? t("map.passthrough")
                : "127.0.0.1:" + (rs.local_port || 0)}</td>
            </tr>`).join("")
            : `<tr><td colspan="4" class="muted tiny">${t("map.no_sni")}</td></tr>`}
          </tbody>
        </table></div>
      </div>

      <div class="card">
        <h3 class="card-title">${Icons.svg("globe", 16)} ${t("map.http_summary")}</h3>
        <p class="tiny muted">${t("map.http_summary_help")}</p>
        <div class="table-wrap"><table class="data-table">
          <thead><tr>
            <th>${t("map.host")}</th><th>${t("map.path")}</th>
            <th>${t("map.port")}</th><th>${t("map.services")}</th>
            <th>${t("map.target")}</th>
          </tr></thead>
          <tbody>${(() => {
            const rows = [];
            (topo.services || []).forEach(sv => {
              const path = sv.path ? "/" + String(sv.path).replace(/^\//, "") : "/";
              const binds = sv.bindings && sv.bindings.length
                ? sv.bindings : [{ fqdn: "*" }];
              binds.forEach(b => rows.push(`
                <tr class="${sv.disabled ? "row-off" : ""}">
                  <td class="mono tiny" dir="ltr">${esc(b.fqdn)}${b.has_cert
                    ? ` <span class="badge badge-success">${t("map.cert")}</span>` : ""}</td>
                  <td class="mono tiny" dir="ltr">${esc(path)}</td>
                  <td class="mono" dir="ltr">${sv.listen_port || 443}</td>
                  <td>${esc(sv.name)}${sv.is_panel
                    ? ` <span class="badge badge-info">${t("map.panel")}</span>` : ""}${
                    sv.disabled ? ` <span class="badge badge-off">${t("map.off")}</span>` : ""}</td>
                  <td class="mono tiny" dir="ltr">${sv.passthrough
                    ? t("map.passthrough")
                    : (sv.target && sv.target !== "127.0.0.1" && sv.target !== "localhost"
                        ? sv.target : "127.0.0.1") + ":" + (sv.local_port || 0)}</td>
                </tr>`));
            });
            return rows.length ? rows.join("")
              : `<tr><td colspan="5" class="muted tiny">${t("map.no_http")}</td></tr>`;
          })()}
          </tbody>
        </table></div>
      </div>

      <div class="card">
        <h3 class="card-title">${Icons.svg("ports", 16)} ${t("map.summary")}</h3>
        <div class="table-wrap"><table class="data-table">
          <thead><tr>
            <th>${t("map.port")}</th><th>${t("map.type")}</th>
            <th>${t("map.services")}</th><th>${t("map.reachable")}</th>
          </tr></thead>
          <tbody>${(topo.ports || []).map(p => `
            <tr>
              <td class="mono" dir="ltr">${p.port}</td>
              <td><span class="badge badge-${p.kind === "sni" ? "info" : "neutral"}">
                ${p.kind === "sni" ? t("map.kind_sni")
                  : p.kind === "http" ? t("map.kind_internal") : t("map.kind_https")}</span></td>
              <td class="tiny">${(p.services || []).map(esc).join(", ") || "—"}</td>
              <td class="tiny mono" dir="ltr">${(p.sni_names || []).map(esc).join(", ") || "—"}</td>
            </tr>`).join("")}
          </tbody>
        </table></div>
      </div>`;

    document.getElementById("mp-refresh").onclick = () =>
      window.Pages.map.render(container, state, ctx);
  },
};

})();
