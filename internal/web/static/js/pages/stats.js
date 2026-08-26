/* Stats page — request/connection charts, TCP/UDP protocol chart and
   live server resources (CPU/RAM/Disk/Swap). Everything below the range
   tabs updates live; resource charts refresh every 5s without a page
   reload. */
window.Pages = window.Pages || {};
window.Pages.stats = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;
    const ranges = [[2,"2m"],[5,"5m"],[15,"15m"],[30,"30m"],[60,"1h"],[360,"6h"],[1440,"24h"]];
    let mins = 60;
    let liveTimer = null;
    const stopLive = () => { if (liveTimer) { clearInterval(liveTimer); liveTimer = null; } };

    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("stats",20)} ${t("stats.title")}</h1>
        <div class="tabs" id="range-tabs">
          ${ranges.map(([v,l])=>`<button class="tab ${v===mins?"active":""}" data-m="${v}">${l}</button>`).join("")}
        </div></div>
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
      <div class="card-grid">
        <div class="card"><h3>${Icons.svg("globe",16)} Top IPs</h3><div id="top-ips" class="rank-list"></div></div>
        <div class="card"><h3>${Icons.svg("stats",16)} Top paths</h3><div id="top-paths" class="rank-list"></div></div>
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
        const r = await api(`/api/stats/resources?minutes=${mins}`);
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

    container.querySelectorAll("#range-tabs .tab").forEach(b=>b.onclick=()=>{
      container.querySelectorAll("#range-tabs .tab").forEach(x=>x.classList.remove("active"));
      b.classList.add("active"); mins=+b.dataset.m; load(); loadResources();
    });
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
  }
};
function row(x){ return `<div class="rank-row"><span class="rank-k">${x.ip||x.path}</span><span class="rank-v">${x.cnt}</span></div>`; }
function cssV(n){ return getComputedStyle(document.documentElement).getPropertyValue(n).trim()||""; }
function pct(v){ if (v==null) return "–"; return Math.round(v*10)/10 + "%"; }
