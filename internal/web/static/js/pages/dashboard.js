/* Dashboard page */
window.Pages = window.Pages || {};
window.Pages.dashboard = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;
    const [summary, topo, nginx] = await Promise.all([
      api("/api/stats/summary"), api("/api/stats/topology"), api("/api/settings/nginx"),
    ]);
    const fmt = n => (n||0).toLocaleString(state.lang === "fa" ? "fa-IR" : undefined);
    const fmtB = b => { if (!b) return "0 B"; const u=["B","KB","MB","GB"]; let i=0; while(b>=1024&&i<u.length-1){b/=1024;i++;} return b.toFixed(1)+" "+u[i]; };
    const active = nginx.status && nginx.status.active;
    // /api/stats/topology returns ARRAYS of objects, each carrying its own
    // `name`. Treating them as maps (Object.entries) made the services
    // table show the array INDEX ("0", "1", "2"…) as the service name and
    // the counters count array slots instead of entries. Normalise once so
    // both an array and a legacy name→service map render correctly.
    const asList = (v) => Array.isArray(v)
      ? v
      : Object.entries(v || {}).map(([name, o]) => ({ name, ...o }));
    const services = asList(topo.services);
    const domainList = asList(topo.domains);
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("dashboard",20)} ${t("dashboard.title")}</h1>
        <span class="badge ${active?"badge-success":"badge-danger"}">
          ${active ? Icons.svg("shield",14)+" "+t("dashboard.nginx_active") : Icons.svg("warning",14)+" "+t("dashboard.nginx_inactive")}
        </span>
      </div>
      <div class="stat-grid">
        ${stat(Icons.svg("services",20), t("dashboard.total_services"), services.length)}
        ${stat(Icons.svg("domains",20), t("dashboard.total_domains"), domainList.length)}
        ${stat(Icons.svg("activity",20), t("dashboard.active_connections"), summary.connections ? summary.connections.active : 0)}
        ${stat(Icons.svg("zap",20), t("dashboard.requests_hour"), fmt(summary.last_hour ? summary.last_hour.requests : 0))}
        ${stat(Icons.svg("stats",20), t("dashboard.requests_24h"), fmt(summary.last_24h ? summary.last_24h.requests : 0))}
        ${stat(Icons.svg("warning",20), t("dashboard.error_rate"),
          ((summary.last_hour&&summary.last_hour.error_rate_pct)||0).toFixed(1)+"%",
          (summary.last_hour&&summary.last_hour.error_rate_pct)>5 ? "danger" : "ok")}
      </div>
      <div class="card-grid">
        <div class="card">
          <div class="card-head"><h3>${Icons.svg("stats",16)} ${t("stats.requests")}</h3></div>
          <canvas id="chart-req" height="160"></canvas>
        </div>
        <div class="card">
          <div class="card-head"><h3>${Icons.svg("activity",16)} ${t("stats.connections")}</h3></div>
          <canvas id="chart-conn" height="160"></canvas>
        </div>
      </div>
      <div class="card">
        <div class="card-head"><h3>${Icons.svg("services",16)} ${t("nav.services")}</h3></div>
        <div class="table-wrap">
          <table class="data-table">
            <thead><tr><th>${t("services.name")}</th><th>${t("services.local_port")}</th><th>${t("services.listen_port")}</th><th>${t("services.path")}</th><th>${t("services.bindings")}</th></tr></thead>
            <tbody>
              ${services.map(s=>`
                <tr><td><strong>${s.name}</strong> ${s.is_panel?'<span class="badge badge-info">Panel</span>':''}</td>
                <td>${s.local_port}</td><td>${s.listen_port}</td>
                <td><code>/${s.path==="/"?"":s.path}</code></td>
                <td>${(s.bindings||[]).map(b=>`<span class="badge badge-neutral">${b.fqdn}</span>`).join(" ")}</td></tr>`).join("")}
            </tbody>
          </table>
        </div>
      </div>`;
    try {
      const [req, conn] = await Promise.all([
        api("/api/stats/requests/timeseries?minutes=60"),
        api("/api/stats/connections/timeseries?minutes=60"),
      ]);
      ShahragCharts.line(document.getElementById("chart-req"), req, { key: "total", color: cssVar("--chart-1") });
      ShahragCharts.line(document.getElementById("chart-conn"), conn, { key: "active", color: cssVar("--chart-2") });
    } catch(e){}
  }
};
function stat(icon, label, value, tone) {
  return `<div class="stat-card ${tone||""}">
    <div class="stat-icon">${icon}</div>
    <div class="stat-body">
      <div class="stat-label">${label}</div>
      <div class="stat-value">${value}</div>
    </div></div>`;
}
function cssVar(n){ return getComputedStyle(document.documentElement).getPropertyValue(n).trim() || "#7c9eff"; }
