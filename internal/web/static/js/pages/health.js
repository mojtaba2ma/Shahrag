/* Server health.

   One page that answers "is this server all right?" with numbers, and one
   number in particular.

   The reason this page exists at all is swap. Every monitoring tool shows a
   swap GAUGE — "287 MB of 383 MB used, 75%" — and that reading makes people
   panic, redeploy things, buy bigger servers. It is almost always
   meaningless. Linux never volunteers to bring a page back from swap: once
   something was paged out during one busy minute three weeks ago, it stays
   out until it is touched again. So the gauge is a record of the worst
   moment since boot, not a description of now.

   What describes now is the RATE: pages going out and coming back, per
   second, right this moment — the si/so columns in vmstat. If that is zero,
   the machine is not swapping, whatever the gauge says.

   So this page shows both, side by side, and the verdict comes from the
   rate. A bar at 100% with "idle" beside it is a healthy server, and the
   page says so in words rather than leaving the operator to infer it.

   The same JSON drives the Telegram bot, so the two can never disagree. */
window.Pages = window.Pages || {};

(function () {
"use strict";

/* Refresh interval.

   5 seconds is what the stats page uses and it feels live. It is also
   affordable: one report costs about 0.6 ms of /proc reading (measured, see
   TestCollectStaysCheap), and the two expensive probes behind it — systemctl
   and `nginx -t`, which fork — are cached for 10 and 60 seconds
   respectively, so polling faster does NOT fork faster. 12 requests a minute
   at 0.6 ms is under 0.02% of one core. */
const REFRESH_MS = 5000;

function pct(v) { return (v || 0).toFixed(0) + "%"; }

function bytes(b) {
  if (!b || b < 0) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return (b >= 100 ? b.toFixed(0) : b.toFixed(1)) + " " + u[i];
}

/* A duration like "3 دقیقه".

   The number and the unit have OPPOSITE bidi directions, so writing them
   next to each other lets the browser reorder them: "3دقیقه" rendered as
   "دقیقه3", with the number on the wrong side. Each number+unit pair is
   therefore wrapped in its own isolate, which is what \u2068/\u2069 (FSI/PDI)
   are for — they tell the bidi algorithm to lay the run out independently
   and then place the whole thing as a unit. */
function iso(n, unit) { return "\u2068" + n + " " + unit + "\u2069"; }

function duration(sec, t) {
  sec = Math.max(0, sec | 0);
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return iso(d, t("health.unit_d")) + " " + iso(h, t("health.unit_h"));
  if (h) return iso(h, t("health.unit_h")) + " " + iso(m, t("health.unit_m"));
  return iso(m, t("health.unit_m"));
}

/* A continuous green-to-red scale for a percentage.

   Three fixed colours (green / amber / red) make a bar jump from "fine" to
   "warning" at an arbitrary threshold, and 69% looks identical to 5%. A
   continuous ramp shows the trend: an operator watching memory creep from
   60% to 80% sees it darkening long before any threshold fires, which is
   exactly when there is still time to act.

   Interpolated in HSL rather than RGB. Blending green (120 deg) to red (0
   deg) in RGB passes through a muddy grey-brown around the middle, because
   the two channels cross while both are dim. Rotating the hue instead keeps
   every intermediate colour fully saturated and readable, and passes
   through yellow at the midpoint, which is what people expect a gauge to do.

   The hue is eased rather than linear: the first half of the range stays
   comfortably green, because 40% memory use is not half a problem. */
function ramp(pct) {
  const p = Math.max(0, Math.min(100, pct || 0)) / 100;
  // Squaring biases the ramp so it stays green through the low half and
  // moves quickly once it is genuinely high.
  const hue = 120 * (1 - p * p);
  // Lift the lightness slightly at the red end: pure red on a dark theme
  // is hard to read next to the track behind it.
  return `hsl(${hue.toFixed(0)}, 72%, ${(46 + p * 6).toFixed(0)}%)`;
}

/* A bar.

   Two independent inputs, and keeping them independent is the point:

     `valuePct` sets the WIDTH.
     `tone` sets the COLOUR when the caller has a verdict to express;
     passing "scale" instead colours it by the value itself.

   The swap bar must use a verdict, because a swap bar at 100% has to be
   able to render green — a full-but-idle swap is not a problem, and that is
   the whole reason this page exists. The resource bars use the scale,
   because for CPU, memory and disk the number IS the severity. */
function bar(valuePct, tone) {
  const w = Math.max(0, Math.min(100, valuePct || 0));
  if (tone === "scale") {
    return `<div class="hx-bar"><span class="hx-fill"
      style="width:${w}%;background:${ramp(valuePct)}"></span></div>`;
  }
  return `<div class="hx-bar"><span class="hx-fill hx-${tone || "ok"}"
    style="width:${w}%"></span></div>`;
}

const LEVEL_ICON = { ok: "check", warn: "warning", bad: "warning" };

function checkRow(ch, t, Icons) {
  const hint = ch.hint ? Icons.help(t("health.hint_" + ch.hint)) : "";
  return `
    <div class="hx-check hx-l-${ch.level}">
      <span class="hx-dot">${Icons.svg(LEVEL_ICON[ch.level] || "info", 13)}</span>
      <span class="hx-check-name">${t("health.check_" + ch.id)}${hint}</span>
      <span class="hx-check-val mono" dir="ltr">${ch.value || ""}</span>
      <span class="hx-check-detail mono tiny" dir="ltr">${ch.detail || ""}</span>
    </div>`;
}

/* The swap card, which is the point of the page. */
function swapCard(r, t, Icons) {
  const s = r.swap || {};
  const check = (r.checks || []).find(c => c.id === "swap") || { level: "ok" };

  if (!s.total_bytes) {
    return `<div class="card hx-card">
      <h3 class="card-title">${Icons.svg("database", 16)} ${t("health.swap")}</h3>
      <p class="muted tiny">${t("health.swap_none")}</p></div>`;
  }

  const rateIn = s.in_pages_per_sec || 0;
  const rateOut = s.out_pages_per_sec || 0;
  // Pages are 4 KB on every architecture this runs on. Showing KB/s as well
  // as pages/s matters because "50 pages a second" means nothing to most
  // people and "200 KB/s" means something to everyone.
  const kbs = (rateIn + rateOut) * 4;

  let verdict, verdictClass;
  if (!s.measured) {
    verdict = t("health.swap_measuring"); verdictClass = "muted";
  } else if (!s.active) {
    verdict = t("health.swap_verdict_idle"); verdictClass = "hx-good";
  } else if (check.level === "bad") {
    verdict = t("health.swap_verdict_thrashing"); verdictClass = "hx-bad";
  } else {
    verdict = t("health.swap_verdict_active"); verdictClass = "hx-warnt";
  }

  return `
    <div class="card hx-card">
      <h3 class="card-title">${Icons.svg("database", 16)} ${t("health.swap")}</h3>

      <div class="hx-two">
        <div class="hx-half">
          <div class="hx-label">${t("health.swap_gauge")}${Icons.help(t("health.hint_swap_gauge"))}</div>
          <div class="hx-big mono" dir="ltr">${pct(s.used_pct)}</div>
          <div class="tiny muted mono" dir="ltr">${bytes(s.used_bytes)} / ${bytes(s.total_bytes)}</div>
          ${bar(s.used_pct, check.level)}
        </div>
        <div class="hx-half">
          <div class="hx-label">${t("health.swap_rate")}${Icons.help(t("health.hint_swap_rate"))}</div>
          <div class="hx-big mono" dir="ltr">${s.measured ? (rateIn + rateOut).toFixed(0) + " p/s" : "…"}</div>
          <div class="tiny muted mono" dir="ltr">
            ${s.measured ? `in ${rateIn.toFixed(0)} · out ${rateOut.toFixed(0)} · ${bytes(kbs * 1024)}/s` : ""}
          </div>
        </div>
      </div>

      <p class="hx-verdict ${verdictClass}">${verdict}</p>
      <p class="tiny muted">${t("health.swap_explain")}</p>
    </div>`;
}

function resourceCard(r, t, Icons) {
  // Still used for the extra iowait/steal lines below, which ARE verdicts
  // rather than magnitudes.
  const lvl = id => ((r.checks || []).find(c => c.id === id) || {}).level || "ok";
  const cpu = r.cpu || {}, mem = r.memory || {}, disk = r.disk || {};

  const row = (icon, label, value, sub, fill, tone, hint) => `
    <div class="hx-res">
      <div class="hx-res-head">
        <span class="hx-res-name">${Icons.svg(icon, 14)} ${label}${hint ? Icons.help(hint) : ""}</span>
        <span class="hx-res-val mono" dir="ltr" style="color:${ramp(fill)}">${value}</span>
      </div>
      ${bar(fill, tone)}
      <div class="tiny muted mono hx-res-sub" dir="ltr">${sub}</div>
    </div>`;

  let extra = "";
  if ((cpu.iowait_pct || 0) >= 1) {
    extra += `<div class="tiny muted mono" dir="ltr">iowait ${cpu.iowait_pct.toFixed(1)}%</div>`;
  }
  if ((cpu.steal_pct || 0) >= 1) {
    extra += `<div class="tiny muted mono" dir="ltr">steal ${cpu.steal_pct.toFixed(1)}%</div>`;
  }

  return `
    <div class="card hx-card">
      <h3 class="card-title">${Icons.svg("cpu", 16)} ${t("health.resources")}</h3>
      ${row("cpu", t("health.cpu"), pct(cpu.used_pct),
        `load ${(cpu.load1 || 0).toFixed(2)} / ${(cpu.load5 || 0).toFixed(2)} / ${(cpu.load15 || 0).toFixed(2)} · ${cpu.cores || 0} cores`,
        cpu.used_pct, "scale", t("health.hint_cpu_load"))}
      ${extra}
      ${row("database", t("health.memory"), pct(mem.used_pct),
        `${bytes(mem.used_bytes)} / ${bytes(mem.total_bytes)} · \u2068${t("health.cache")} ${bytes(mem.cache_bytes)}\u2069`,
        mem.used_pct, "scale", t("health.hint_memory_available"))}
      ${row("server", t("health.disk"), pct(disk.used_pct),
        `\u2068${bytes(disk.free_bytes)} ${t("health.free")}\u2069 · inodes ${pct(disk.inodes_pct)}`,
        disk.used_pct, "scale", t("health.hint_inodes"))}
    </div>`;
}

function serviceCard(r, t, Icons) {
  const n = r.nginx || {}, sec = r.security || {}, p = r.panel || {}, c = r.certs || {};
  const yn = v => v
    ? `<span class="badge badge-success">${t("health.yes")}</span>`
    : `<span class="badge badge-danger">${t("health.no")}</span>`;

  const certLine = c.total
    ? (c.expired && c.expired.length
        ? `<span class="badge badge-danger">\u2068${c.expired.length} ${t("health.expired")}\u2069</span> ${c.expired.join(", ")}`
        : `${iso(c.soonest_days, t("health.unit_d"))} — ${c.soonest_name || ""}`)
    : "—";

  return `
    <div class="card hx-card">
      <h3 class="card-title">${Icons.svg("server", 16)} ${t("health.services")}</h3>
      <div class="table-wrap"><table class="data-table hx-kv">
        <tbody>
          <tr><td>${t("health.nginx_running")}</td><td>${yn(n.active)}</td></tr>
          <tr><td>${t("health.nginx_boot")}</td><td>${yn(n.enabled_at_boot)}</td></tr>
          <tr><td>${t("health.nginx_config")}</td><td>${n.config_ok
            ? yn(true)
            : `<span class="badge badge-danger">${t("health.no")}</span>
               <span class="tiny mono" dir="ltr">${(n.config_error || "").slice(0, 120)}</span>`}</td></tr>
          <tr><td>${t("health.nginx_workers")}</td><td class="mono" dir="ltr">${n.workers || 0} × ${n.worker_connections || 0}</td></tr>
          <tr><td>${t("health.nginx_version")}</td><td class="mono tiny" dir="ltr">${n.version || "—"}</td></tr>
          <tr><td>${t("health.certs")}</td><td dir="ltr">${certLine}</td></tr>
          <tr><td>${t("health.honeypot")}</td><td>${sec.honeypot_on
            ? `<span class="badge badge-success">${t("health.on")}</span>
               <span class="tiny mono">${sec.honeypot_mode || ""}</span>`
            : `<span class="badge badge-neutral">${t("health.off")}</span>`}</td></tr>
          <tr><td>${t("health.autoban")}</td><td>${sec.autoban_on
            ? `<span class="badge badge-success">${t("health.on")}</span>
               <span class="tiny mono">${sec.autoban_action || ""}</span>
               <span class="badge ${sec.bans_active ? "badge-danger" : "badge-neutral"}">\u2068${sec.bans_active} ${t("health.banned")}\u2069</span>`
            : `<span class="badge badge-neutral">${t("health.off")}</span>`}</td></tr>
        </tbody>
      </table></div>
    </div>`;
}

/* The panel's own footprint.

   Published on purpose. A control panel that advises an operator about
   resources and will not say what it costs has no standing to do so, and
   RssAnon — the private heap, not VmRSS — is the honest number: VmRSS also
   counts the mapped binary and shared pages, which are not a per-process
   cost and make the panel look three times bigger than it is. */
function panelCard(r, t, Icons) {
  const p = r.panel || {}, tr = r.traffic || {};
  return `
    <div class="card hx-card">
      <h3 class="card-title">${Icons.svg("activity", 16)} ${t("health.panel")}</h3>
      <div class="hx-mini">
        ${[[t("health.p_ram"), bytes(p.rss_anon_bytes), t("health.hint_rssanon")],
           [t("health.p_uptime"), duration(p.uptime_sec, t), ""],
           [t("health.p_goroutines"), p.goroutines || 0, ""],
           [t("health.p_fds"), p.open_fds || 0, t("health.hint_fds")],
           [t("health.p_build"), p.build || "—", ""],
           [t("health.p_reqh"), (tr.requests_hour || 0).toLocaleString(), ""],
          ].map(([k, v, h]) => `
          <div class="hx-mini-item">
            <div class="hx-mini-k">${k}${h ? Icons.help(h) : ""}</div>
            <div class="hx-mini-v mono" dir="ltr">${v}</div>
          </div>`).join("")}
      </div>
      <p class="tiny muted">
        <span dir="ltr" class="mono">${r.host || "—"}</span> ·
        <span dir="ltr" class="mono">${r.kernel || "—"}</span> ·
        \u2068${t("health.sys_uptime")}: ${duration(r.uptime_sec, t)}\u2069</p>
    </div>`;
}

window.Pages.health = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;

    let timer = null;
    // Registered BEFORE the first await: if the operator navigates away
    // while the first request is still in flight, the shell must still be
    // able to stop this page's timer.
    container._shahragCleanup = () => { if (timer) clearInterval(timer); timer = null; };

    const paint = (r) => {
      const level = r.level || "ok";
      const worst = (r.checks || []).slice().sort((a, b) => {
        const rank = { bad: 0, warn: 1, ok: 2 };
        return rank[a.level] - rank[b.level];
      });

      container.innerHTML = `
        <div class="page-header">
          <h1>${Icons.svg("activity", 20)} ${t("status.tab_health")}</h1>
          <span class="badge hx-badge-${level}">
            ${Icons.svg(LEVEL_ICON[level], 14)} ${t("health.overall_" + level)}
          </span>
        </div>

        <div class="card hx-card">
          <div class="card-head">
            <h3 class="card-title">${Icons.svg("check", 16)} ${t("health.checks")}</h3>
            <button class="btn btn-ghost btn-sm" id="hx-refresh">
              ${Icons.svg("refresh", 14)} <span class="btn-label">${t("stats.refresh")}</span>
            </button>
          </div>
          <div class="hx-checks">${worst.map(c => checkRow(c, t, Icons)).join("")}</div>
        </div>

        ${swapCard(r, t, Icons)}
        ${resourceCard(r, t, Icons)}
        ${serviceCard(r, t, Icons)}
        ${panelCard(r, t, Icons)}`;

      const btn = document.getElementById("hx-refresh");
      if (btn) btn.onclick = () => load();
    };

    let inFlight = false;
    const load = async () => {
      // A slow answer must not stack requests behind it: on a loaded box a
      // 6-second report with a 5-second timer would queue for ever.
      if (inFlight) return;
      inFlight = true;
      try {
        paint(await api("/api/health/report"));
      } catch (e) {
        container.innerHTML = `<div class="card"><p style="color:var(--danger);
          display:flex;gap:8px;align-items:center">
          ${Icons.svg("warning", 18)} ${e.message}</p></div>`;
      } finally {
        inFlight = false;
      }
    };

    await load();
    timer = setInterval(load, REFRESH_MS);
  },
};

})();
