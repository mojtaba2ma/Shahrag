/* Stats page — request/connection charts, TCP/UDP protocol chart and
   live server resources (CPU/RAM/Disk/Swap). Everything below the range
   tabs updates live; resource charts refresh every 5s without a page
   reload. */
window.Pages = window.Pages || {};

/* The windows the traffic breakdown offers.

   Kept as data with translation KEYS rather than written inline: an
   abbreviation for seven days written in English is meaningless in eight
   of the ten languages the panel ships, and statsrange_test.go enforces
   that with a substring scan over this file. The API takes plain
   minutes, so nothing here is a language-specific token either. */
const DIM_RANGES = [
  { hours: 1, label: "stats.range_1h" },
  { hours: 6, label: "stats.range_6h" },
  { hours: 24, label: "stats.range_24h", def: true },
  { hours: 24 * 7, label: "stats.range_7d" },
].map(r => Object.assign(r, { minutes: r.hours * 60 }));
window.Pages.stats = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;
    // Retention is tiered on the server (5s -> 1m -> 15m -> 1h), so a year
    // of history costs a couple of megabytes and these long ranges are
    // genuinely available rather than aspirational.
    // Twelve options as twelve tab buttons filled the header and wrapped
    // onto a second row on anything narrower than a laptop. One dropdown
    // with a clock icon says the same thing in one control, and it scales
    // if more ranges are ever added.
    //
    // Grouped so the list reads as two ideas rather than twelve numbers:
    // what is happening now, and what happened over time.
    const groups = [
      [t("stats.range_recent"), [
        [2, t("stats.minutes_2")], [5, t("stats.minutes_5")],
        [15, t("stats.minutes_15")], [30, t("stats.minutes_30")],
        [60, t("stats.hour_1")], [360, t("stats.hours_6")],
        [1440, t("stats.hours_24")],
      ]],
      [t("stats.range_history"), [
        [10080, t("stats.days_7")], [43200, t("stats.days_30")],
        [129600, t("stats.days_90")], [259200, t("stats.months_6")],
        [525600, t("stats.year_1")],
      ]],
    ];
    let mins = 60;
    let liveTimer = null;
    const stopLive = () => { if (liveTimer) { clearInterval(liveTimer); liveTimer = null; } };

    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("stats",20)} ${t("stats.title")}</h1>
        <span class="range-pick" title="${t("stats.timeframe")}">
          <span class="range-icon">${Icons.svg("clock",14)}</span>
          <select id="range-select" aria-label="${t("stats.timeframe")}">
            ${groups.map(([label, opts]) => `
              <optgroup label="${label}">
                ${opts.map(([v,l])=>`<option value="${v}" ${v===mins?"selected":""}>${l}</option>`).join("")}
              </optgroup>`).join("")}
          </select>
        </span></div>
      <div class="stat-grid">
        <div class="stat-card"><div class="stat-label">${t("stats.requests")}</div><div class="stat-value" id="s-req">-</div></div>
        <div class="stat-card"><div class="stat-label">2xx</div><div class="stat-value" id="s-2xx">-</div></div>
        <div class="stat-card"><div class="stat-label">4xx</div><div class="stat-value" id="s-4xx">-</div></div>
        <div class="stat-card"><div class="stat-label">5xx</div><div class="stat-value" id="s-5xx">-</div></div>
      </div>
      <div class="card-grid">
        <div class="card"><h3>${Icons.svg("stats",16)} ${t("stats.requests")}</h3><canvas id="c-req"></canvas></div>
        <div class="card"><h3>${Icons.svg("activity",16)} ${t("stats.connections")}</h3><canvas id="c-conn"></canvas></div>
      </div>
      <div class="card"><h3>${Icons.svg("globe",16)} ${t("stats.tcp_udp")}</h3><canvas id="c-proto"></canvas></div>
      <div class="card">
        <h3>${Icons.svg("server",16)} ${t("stats.resources")}</h3>
        <div class="resource-grid">
          <div class="resource-cell"><div class="resource-label">CPU <span id="v-cpu" class="resource-val">–</span></div><canvas id="c-cpu"></canvas></div>
          <div class="resource-cell"><div class="resource-label">RAM <span id="v-ram" class="resource-val">–</span></div><canvas id="c-ram"></canvas></div>
          <div class="resource-cell"><div class="resource-label">${t("stats.disk")} <span id="v-disk" class="resource-val">–</span></div><canvas id="c-disk"></canvas></div>
          <div class="resource-cell"><div class="resource-label">Swap <span id="v-swap" class="resource-val">–</span></div><canvas id="c-swap"></canvas></div>
        </div>
      </div>
      <!-- Bans over time.

           TWO series, because they answer different questions: the ACTIVE
           count is a gauge that falls as bans expire, while the cumulative
           TOTAL only rises and its slope is what says "am I under attack?".
           A chart of the gauge alone makes a wave that ended an hour ago
           look like nothing ever happened. -->
      <div class="card">
        <div class="card-head">
          <h3>${Icons.svg("shield",16)} ${t("stats.bans")}</h3>
          <span id="ban-figs" class="tiny muted"></span>
        </div>
        <div class="resource-grid">
          <div class="resource-cell">
            <div class="resource-label">${t("stats.bans_active")}
              <span id="v-ban-active" class="resource-val">–</span></div>
            <canvas id="c-ban-active"></canvas>
          </div>
          <div class="resource-cell">
            <div class="resource-label">${t("stats.bans_total")}
              <span id="v-ban-total" class="resource-val">–</span></div>
            <canvas id="c-ban-total"></canvas>
          </div>
        </div>
        <p class="tiny muted" id="ban-note"></p>
      </div>
      <div class="card-grid">
        <div class="card"><h3>${Icons.svg("globe",16)} Top IPs</h3><div id="top-ips" class="rank-list"></div></div>
        <div class="card"><h3>${Icons.svg("stats",16)} Top paths</h3><div id="top-paths" class="rank-list"></div></div>
      </div>

      <!-- ── The traffic breakdown ────────────────────────────
           Which service, host, path, port, status and method, over a
           chosen window. The two cards above are lifetime totals with no
           time window at all; this answers "what was busy last night". -->
      <div class="card" id="dim-card">
        <div class="card-head">
          <h3 class="card-title">${Icons.svg("activity",16)} ${t("stats.breakdown")}</h3>
          <div class="dim-ranges" id="dim-ranges">
            ${DIM_RANGES.map(r =>
              `<button class="btn btn-sm ${r.def ? "btn-primary" : "btn-ghost"}"
                       data-range="${r.minutes}">${t(r.label)}</button>`).join("")}
          </div>
        </div>
        <p class="tiny muted" id="dim-note"></p>
        <div class="dim-totals" id="dim-totals"></div>
        <div class="dim-tabs" id="dim-tabs"></div>
        <div id="dim-body"></div>
      </div>`;

    const colors = {
      c1: cssV("--chart-1") || "#7c9eff",
      c2: cssV("--chart-2") || "#4fd1a5",
      warn: cssV("--warning") || "#e8b45a",
      danger: cssV("--danger") || "#e06a6a",
    };

    // Configure canvases once (interaction handlers).
    ShahragCharts.line(document.getElementById("c-req"), [], { key: "total", color: colors.c1 });
    ShahragCharts.line(document.getElementById("c-conn"), [], { key: "active", color: colors.c2 });
    ShahragCharts.multi(document.getElementById("c-proto"), [], {
      series: [
        { key: "tcp", color: colors.c1, label: "TCP" },
        { key: "udp", color: colors.c2, label: "UDP" },
      ],
    });
    ShahragCharts.line(document.getElementById("c-ban-active"), [],
      { key: "active", color: colors.c4 || colors.c1 });
    ShahragCharts.line(document.getElementById("c-ban-total"), [],
      { key: "total", color: colors.c3 || colors.c2 });
    ShahragCharts.line(document.getElementById("c-cpu"), [], { key: "cpu", color: colors.c1 });
    ShahragCharts.line(document.getElementById("c-ram"), [], { key: "ram", color: colors.c2 });
    ShahragCharts.line(document.getElementById("c-disk"), [], { key: "disk", color: colors.warn });
    ShahragCharts.line(document.getElementById("c-swap"), [], { key: "swap", color: colors.danger });

    const load = async ()=>{
      try {
        const [s,c,ips,paths,dist,p] = await Promise.all([
          api(`/api/stats/requests/timeseries?minutes=${mins}`),
          api(`/api/stats/connections/timeseries?minutes=${mins}`),
          api(`/api/stats/top/ips?minutes=${mins}&limit=10`),
          api(`/api/stats/top/paths?minutes=${mins}&limit=10`),
          api(`/api/stats/status-distribution?minutes=${mins}`),
          api(`/api/stats/proto/timeseries?minutes=${mins}`),
        ]);
        document.getElementById("s-req").textContent = dist["2xx"]+dist["3xx"]+dist["4xx"]+dist["5xx"]||0;
        document.getElementById("s-2xx").textContent = (dist["2xx"]||0)+(dist["3xx"]||0);
        document.getElementById("s-4xx").textContent = dist["4xx"]||0;
        document.getElementById("s-5xx").textContent = dist["5xx"]||0;
        ShahragCharts.update(document.getElementById("c-req"), s);
        ShahragCharts.update(document.getElementById("c-conn"), c);
        ShahragCharts.update(document.getElementById("c-proto"), p);
        document.getElementById("top-ips").innerHTML = (ips||[]).map(x=>row(x)).join("")||"<p class='muted'>—</p>";
        document.getElementById("top-paths").innerHTML = (paths||[]).map(x=>row(x)).join("")||"<p class='muted'>—</p>";
      } catch(e) { /* ignore transient errors */ }
    };

    // Live server resources: poll every 5 seconds, no page reload.
    const loadResources = async ()=>{
      try {
        // Must follow the selected range. It was pinned to 60 minutes, so
        // picking "12h" updated every chart EXCEPT server resources, which
        // silently kept showing the last hour.
        // _poll=1 marks this as a background heartbeat. Without it the
        // 5-second refresh counted as user activity on every tick, so the
        // inactivity lock could never fire while this page was open — the
        // panel stayed logged in for days.
        // Bans follow the same range and the same poll marker.
        try {
          const b = await api(`/api/stats/bans?minutes=${mins}&_poll=1`);
          const series = b.series || [];
          const sum = b.summary || {};
          ShahragCharts.update(document.getElementById("c-ban-active"), series);
          ShahragCharts.update(document.getElementById("c-ban-total"), series);
          document.getElementById("v-ban-active").textContent = sum.active ?? "–";
          document.getElementById("v-ban-total").textContent = sum.total ?? "–";
          const figs = document.getElementById("ban-figs");
          const note = document.getElementById("ban-note");
          if (!sum.running) {
            figs.textContent = t("stats.bans_off");
            note.textContent = "";
          } else {
            figs.textContent =
              `\u2068${sum.new_last_hour || 0} ${t("stats.bans_new_hour")}\u2069 · ` +
              `\u2068${sum.new_last_24h || 0} ${t("stats.bans_new_day")}\u2069 · ` +
              `\u2068${t("stats.bans_peak")} ${sum.peak_active_24h || 0}\u2069`;
            note.textContent = (sum.pending || 0) > 0
              ? t("stats.bans_pending").replace("%n", sum.pending) : "";
          }
        } catch (e) { /* the ban engine may not be running */ }

        const r = await api(`/api/stats/resources?minutes=${mins}&_poll=1`);
        const res = (r && r.resources) || [];
        ShahragCharts.update(document.getElementById("c-cpu"), res);
        ShahragCharts.update(document.getElementById("c-ram"), res);
        ShahragCharts.update(document.getElementById("c-disk"), res);
        ShahragCharts.update(document.getElementById("c-swap"), res);
        if (res.length) {
          const last = res[res.length-1];
          document.getElementById("v-cpu").textContent = pct(last.cpu);
          document.getElementById("v-ram").textContent = pct(last.ram);
          document.getElementById("v-disk").textContent = pct(last.disk);
          document.getElementById("v-swap").textContent = pct(last.swap);
        }
      } catch(e) {}
    };

    const sel = container.querySelector("#range-select");
    sel.onchange = () => { mins = +sel.value; load(); loadResources(); };
    load();
    loadResources();
    liveTimer = setInterval(loadResources, 5000);
    // Stop the live timer when navigating away (any nav click).
    const navHandler = (e) => {
      if (e.target.closest && e.target.closest(".nav-item")) stopLive();
    };
    document.addEventListener("click", navHandler);
    container._shahragCleanup = () => {
      stopLive();
      document.removeEventListener("click", navHandler);
    };

    initBreakdown(ctx);
  }
};

/* The per-dimension breakdown. Seven dimensions, one window, one request. */
function initBreakdown(ctx) {
  const { api, t, Icons, toast } = ctx;
  const DIMS = ["service", "host", "path", "port", "status", "method", "ip"];
  let range = (DIM_RANGES.find(r => r.def) || DIM_RANGES[0]).minutes;
  let active = "service";
  let cache = null;

  const card = document.getElementById("dim-card");
  if (!card) return;

  const fmtBytes = n => {
    if (n == null) return "—";
    if (n < 1024) return n + " B";
    if (n < 1048576) return (n / 1024).toFixed(1) + " KB";
    if (n < 1073741824) return (n / 1048576).toFixed(1) + " MB";
    return (n / 1073741824).toFixed(2) + " GB";
  };
  const fmtNum = n => (n == null ? "—" : String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ","));

  const paintTotals = (sum, hours) => {
    const el = document.getElementById("dim-totals");
    if (!sum) { el.innerHTML = ""; return; }
    const cells = [
      ["stats.total_requests", fmtNum(sum.requests)],
      ["stats.total_traffic", fmtBytes(sum.bytes)],
      ["stats.total_errors", fmtNum(sum.errors)],
      ["stats.error_rate", (Math.round((sum.error_rate || 0) * 10) / 10) + "%"],
    ];
    el.innerHTML = cells.map(([k, v]) =>
      `<div class="dim-total"><span class="dim-total-v" dir="ltr">${v}</span>
       <span class="dim-total-k">${t(k)}</span></div>`).join("");
    document.getElementById("dim-note").textContent =
      hours ? t("stats.covers_hours").replace("%n", hours) : t("stats.no_data_yet");
  };

  const paintTabs = () => {
    document.getElementById("dim-tabs").innerHTML = DIMS.map(d =>
      `<button class="dim-tab ${d === active ? "active" : ""}" data-dim="${d}">
         ${t("stats.dim_" + d)}</button>`).join("");
    document.querySelectorAll("#dim-tabs .dim-tab").forEach(b =>
      b.onclick = () => { active = b.dataset.dim; paintTabs(); paintRows(); });
  };

  /* One dimension's rows.

     A table with the bar drawn BEHIND the key rather than in a column of
     its own: a top-N list is read as "which is biggest and by how much",
     and a bar in the row answers that without the eye leaving the name.
     It also costs no horizontal space, which matters on a phone. */
  const paintRows = () => {
    const body = document.getElementById("dim-body");
    const rows = (cache && cache.dimensions && cache.dimensions[active]) || [];
    if (!rows.length) {
      body.innerHTML = `<p class="muted tiny">${t("stats.no_data_yet")}</p>`;
      return;
    }
    const max = Math.max.apply(null, rows.map(r => r.count));
    body.innerHTML = `
      <div class="table-wrap"><table class="data-table dim-table">
        <thead><tr>
          <th class="lv-num">#</th>
          <th>${t("stats.dim_" + active)}</th>
          <th class="num">${t("stats.requests")}</th>
          <th class="num">${t("stats.share")}</th>
          <th class="num">${t("stats.traffic")}</th>
          <th class="num">${t("stats.errors")}</th>
          <th></th>
        </tr></thead>
        <tbody>${rows.map((r, i) => `
          <tr>
            <td class="lv-num">${i + 1}</td>
            <td class="dim-key">
              <span class="dim-bar" style="width:${max ? (r.count * 100 / max) : 0}%"></span>
              <code dir="ltr">${escapeHTML(r.key)}</code>
            </td>
            <td class="num" dir="ltr">${fmtNum(r.count)}</td>
            <td class="num" dir="ltr">${(Math.round((r.share || 0) * 10) / 10)}%</td>
            <td class="num" dir="ltr">${fmtBytes(r.bytes)}</td>
            <td class="num" dir="ltr">${r.errors
              ? `<span class="badge badge-danger">${fmtNum(r.errors)}</span>` : "0"}</td>
            <td class="row-actions">
              <button class="btn btn-sm btn-ghost" data-expand="${encodeURIComponent(r.key)}"
                      title="${t("stats.show_over_time")}">${Icons.svg("activity", 13)}</button>
            </td>
          </tr>
          <tr class="dim-chart-row" data-chart="${encodeURIComponent(r.key)}" hidden>
            <td colspan="7"><div class="dim-chart"><canvas></canvas></div></td>
          </tr>`).join("")}
        </tbody>
      </table></div>`;

    body.querySelectorAll("[data-expand]").forEach(btn => btn.onclick = async () => {
      const key = btn.dataset.expand;
      const tr = body.querySelector(`[data-chart="${key}"]`);
      if (!tr) return;
      if (!tr.hidden) { tr.hidden = true; return; }
      tr.hidden = false;
      try {
        const r = await api(`/api/stats/dimension-series?dim=${encodeURIComponent(active)}` +
                            `&key=${key}&range=${range}`);
        const pts = (r.points || []).map(p => ({ ts: p.ts, count: p.count }));
        /* configure(cv, items, opts) — the items go in at configure
           time; update() is for pushing later samples into a canvas that
           is already configured. */
        window.ShahragCharts.configure(tr.querySelector("canvas"), pts, {
          key: "count",
          color: cssV("--chart-1") || "#7c9eff",
          label: t("stats.requests"),
        });
      } catch (e) { toast(e.message, "error"); }
    });
  };

  const load = async () => {
    try {
      cache = await api(`/api/stats/dimensions?range=${range}&limit=15`);
      paintTotals(cache.summary, cache.covers_hours);
      paintRows();
    } catch (e) {
      document.getElementById("dim-body").innerHTML =
        `<p class="muted tiny">${escapeHTML(e.message)}</p>`;
    }
  };

  document.querySelectorAll("#dim-ranges [data-range]").forEach(b =>
    b.onclick = () => {
      range = +b.dataset.range;
      document.querySelectorAll("#dim-ranges [data-range]").forEach(x => {
        x.classList.toggle("btn-primary", x === b);
        x.classList.toggle("btn-ghost", x !== b);
      });
      load();
    });

  paintTabs();
  load();
}

function escapeHTML(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function row(x){ return `<div class="rank-row"><span class="rank-k">${x.ip||x.path}</span><span class="rank-v">${x.cnt}</span></div>`; }
function cssV(n){ return getComputedStyle(document.documentElement).getPropertyValue(n).trim()||""; }
function pct(v){ if (v==null) return "–"; return Math.round(v*10)/10 + "%"; }
