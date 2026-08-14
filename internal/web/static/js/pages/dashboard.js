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
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("dashboard",20)} ${t("dashboard.title")}</h1>
        <span class="badge ${active?"badge-success":"badge-danger"}">
          ${active ? Icons.svg("shield",14)+" "+t("dashboard.nginx_active") : Icons.svg("warning",14)+" "+t("dashboard.nginx_inactive")}
        </span>
      </div>
      <div class="stat-grid">
        ${stat(Icons.svg("services",20), t("dashboard.total_services"), Object.keys(topo.services||{}).length)}
        ${stat(Icons.svg("domains",20), t("dashboard.total_domains"), Object.keys(topo.domains||{}).length)}
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
              ${Object.entries(topo.services||{}).map(([n,s])=>`
                <tr><td><strong>${n}</strong> ${s.is_panel?'<span class="badge badge-info">Panel</span>':''}</td>
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
      drawLine(document.getElementById("chart-req"), req.map(r=>r.total), cssVar("--chart-1"));
      drawLine(document.getElementById("chart-conn"), conn.map(r=>r.active), cssVar("--chart-2"));
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
function drawLine(canvas, data, color) {
  if (!canvas || !data || data.length<2) return;
  const ctx = canvas.getContext("2d"), dpr = window.devicePixelRatio||1;
  const w = canvas.clientWidth, h = canvas.clientHeight;
  canvas.width = w*dpr; canvas.height=h*dpr; ctx.scale(dpr,dpr);
  const max = Math.max(...data, 1), pad=24;
  ctx.strokeStyle = color; ctx.lineWidth=2; ctx.beginPath();
  data.forEach((v,i)=>{
    const x = pad+(w-pad*2)*i/(data.length-1);
    const y = h-pad-(h-pad*2)*v/max;
    i?ctx.lineTo(x,y):ctx.moveTo(x,y);
  });
  ctx.stroke();
  ctx.fillStyle = color+"22";
  ctx.lineTo(w-pad, h-pad); ctx.lineTo(pad, h-pad); ctx.closePath(); ctx.fill();
}
