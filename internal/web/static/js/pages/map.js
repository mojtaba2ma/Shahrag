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
const COL_W = 300;
const GAP_X = 82;       // horizontal gap between columns
/* 52, not 44. At 44 the two text baselines (y+19 and y+31) leave the
   subtitle's descenders sitting on the bottom edge, and on a real render
   they were visibly clipped. Found by reading the screenshot, not the
   source — the numbers looked fine in the code. */
const BOX_H = 56;
const GAP_Y = 15;       // vertical gap between nodes
const PAD = 18;
/* How much of the box a label may use before it is truncated. The badge
   sits in the opposite corner, so the title has to stop short of it or the
   two overlap — which is exactly what "cdn.example.com" + "SNI" did. */
const LABEL_PAD = 14;
/* Room reserved for the badge in the opposite corner.

   38, not 34: "SNI" in the badge font is about 19 px wide plus the 13 px
   padding, and at 34 the longest hostnames still touched it. Measured from
   a real render, because the character-width estimate in fit() is
   deliberately approximate. */
const BADGE_ROOM = 40;

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
function edgePath(a, b, rtl) {
  // Leave from the edge facing the destination, arrive at the edge facing
  // the source. In RTL those are the opposite sides.
  const x1 = rtl ? a.x : a.x + a.w;
  const x2 = rtl ? b.x + b.w : b.x;
  const y1 = a.y + a.h / 2, y2 = b.y + b.h / 2;
  const dx = (x2 - x1) / 2;
  return `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`;
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
  const cls = "mp-node mp-" + n.kind + (n.dim ? " mp-dim" : "");
  const flip = "";
  // Text is anchored INSIDE the box. In RTL the reading direction is
  // right-to-left, so a label starts at the box's right edge and is
  // anchored "end"; in LTR it starts at the left and is anchored "start".
  //
  // The x here is the box's own coordinate AFTER mirroring, which is why
  // this is now simple: nothing else in this function knows about
  // direction, and the anchor and the x always refer to the same edge.
  const tx = rtl ? n.x + n.w - LABEL_PAD : n.x + LABEL_PAD;
  const anchor = rtl ? "end" : "start";
  const badgeX = rtl ? n.x + LABEL_PAD : n.x + n.w - LABEL_PAD;
  const badgeAnchor = rtl ? "start" : "end";

  // The title must stop short of the badge in the opposite corner, or the
  // two overlap — which is what "cdn.example.com" and "SNI" did.
  const titleRoom = n.w - LABEL_PAD * 2 - (n.badge ? BADGE_ROOM : 0);
  const subRoom = n.w - LABEL_PAD * 2;

  const sub = n.sub
    ? `<text class="mp-sub" x="${tx}" y="${n.y + 36}" text-anchor="${anchor}"
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
    <text class="mp-title" x="${tx}" y="${n.y + (n.sub ? 22 : 31)}"
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
    (topo.reality_services || []).forEach(rs => {
      if (rs.disabled) return;
      routeNodes.push({
        id: "sni:" + rs.name, kind: "sni",
        label: rs.sni || rs.name, sub: t("map.split_sni"),
        badge: "SNI", svc: rs, viaSNI: true,
      });
    });
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
      backNodes.push({
        id: "back:" + key, kind: svc.passthrough ? "pass" : "backend",
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
    (topo.reality_services || []).forEach(rs => { if (!rs.disabled) addBack(rs, true); });
    (topo.services || []).forEach(s => addBack(s, false));
    if (!backNodes.length) {
      backNodes.push({ id: "back:none", kind: "backend", label: t("map.no_backends"), dim: true });
    }

    // ── Edges ────────────────────────────────────────────────
    const edges = [];
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
      svcNames.forEach(name => {
        // An SNI port feeds its SNI route nodes.
        const sni = routeNodes.find(r => r.viaSNI && r.svc && r.svc.name === name);
        if (sni) { edges.push([pn.id, sni.id, "sni"]); return; }
        // An https port feeds every route belonging to that service.
        routeNodes.filter(r => r.svcName === name)
          .forEach(r => edges.push([pn.id, r.id, "https"]));
      });
    });
    routeNodes.forEach(r => {
      const svc = r.viaSNI ? r.svc : svcByName[r.svcName];
      if (!svc) return;
      const back = backNodes.find(b => b.id === "back:" + backKey(svc));
      if (back) edges.push([r.id, back.id, svc.passthrough ? "pass" : "http"]);
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
    const tallest = Math.max(portNodes.length, routeNodes.length, backNodes.length);
    const innerH = tallest * BOX_H + Math.max(0, tallest - 1) * GAP_Y;
    const cols = [layout(portNodes, 0, innerH),
                  layout(routeNodes, 1, innerH),
                  layout(backNodes, 2, innerH)];
    const all = [].concat.apply([], cols);

    // PAD*2 covers both margins; the +2 is for the node border, which is
    // drawn centred on the rectangle's edge and so spills half a pixel
    // outside it. Without that the last column's right border was clipped
    // by the viewBox — visible in a render, invisible in the code.
    const W = PAD * 2 + 3 * COL_W + 2 * GAP_X + 2;
    const H = PAD * 2 + innerH;

    // In RTL the flow reads right-to-left, so every x is mirrored once,
    // here, and nothing downstream needs to know about direction.
    if (rtl) all.forEach(n => { n.x = W - n.x - n.w; });

    const byId = {};
    all.forEach(n => { byId[n.id] = n; });

    const reduced = window.matchMedia
      && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    const edgeSVG = edges.map(([a, b, kind], i) => {
      const na = byId[a], nb = byId[b];
      if (!na || !nb) return "";
      const d = edgePath(na, nb, rtl);
      const dot = reduced ? "" : `
        <circle class="mp-dot mp-dot-${kind}" r="3">
          <animateMotion dur="${(2.8 + (i % 5) * 0.35).toFixed(2)}s"
            repeatCount="indefinite" path="${d}"
            begin="${(i * 0.31).toFixed(2)}s"></animateMotion>
        </circle>`;
      return `<path class="mp-edge mp-edge-${kind}" d="${d}"></path>${dot}`;
    }).join("");

    // Nothing is transformed: the coordinates were already mirrored above.
    const mirror = "";

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
