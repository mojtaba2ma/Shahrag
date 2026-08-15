/* Stats page */
window.Pages = window.Pages || {};
window.Pages.stats = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;
    const ranges = [[2,"2m"],[5,"5m"],[15,"15m"],[30,"30m"],[60,"1h"],[360,"6h"],[1440,"24h"]];
    let mins = 60;
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
      <div class="card"><h3>${t("stats.requests")}</h3><canvas id="c-req" height="180"></canvas></div>
      <div class="card"><h3>${t("stats.connections")}</h3><canvas id="c-conn" height="180"></canvas></div>
      <div class="card-grid">
        <div class="card"><h3>${Icons.svg("globe",16)} Top IPs</h3><div id="top-ips" class="rank-list"></div></div>
        <div class="card"><h3>${Icons.svg("stats",16)} Top paths</h3><div id="top-paths" class="rank-list"></div></div>
      </div>`;
    const load = async ()=>{
      const [s,c,ips,paths,dist] = await Promise.all([
        api(`/api/stats/requests/timeseries?minutes=${mins}`),
        api(`/api/stats/connections/timeseries?minutes=${mins}`),
        api(`/api/stats/top/ips?minutes=${mins}&limit=10`),
        api(`/api/stats/top/paths?minutes=${mins}&limit=10`),
        api(`/api/stats/status-distribution?minutes=${mins}`),
      ]);
      document.getElementById("s-req").textContent = dist["2xx"]+dist["3xx"]+dist["4xx"]+dist["5xx"]||0;
      document.getElementById("s-2xx").textContent = (dist["2xx"]||0)+(dist["3xx"]||0);
      document.getElementById("s-4xx").textContent = dist["4xx"]||0;
      document.getElementById("s-5xx").textContent = dist["5xx"]||0;
      ShahragCharts.line(document.getElementById("c-req"), s, { key: "total", color: cssV("--chart-1") });
      ShahragCharts.line(document.getElementById("c-conn"), c, { key: "active", color: cssV("--chart-2") });
      document.getElementById("top-ips").innerHTML = (ips||[]).map(x=>row(x)).join("")||"<p class='muted'>—</p>";
      document.getElementById("top-paths").innerHTML = (paths||[]).map(x=>row(x)).join("")||"<p class='muted'>—</p>";
    };
    container.querySelectorAll("#range-tabs .tab").forEach(b=>b.onclick=()=>{
      container.querySelectorAll("#range-tabs .tab").forEach(x=>x.classList.remove("active"));
      b.classList.add("active"); mins=+b.dataset.m; load();
    });
    load();
  }
};
function row(x){ return `<div class="rank-row"><span class="rank-k">${x.ip||x.path}</span><span class="rank-v">${x.cnt}</span></div>`; }
function cssV(n){ return getComputedStyle(document.documentElement).getPropertyValue(n).trim()||"#7c9eff"; }
