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
  const w = canvas.clientWidth || 320, h = canvas.clientHeight || 160;
  canvas.width = Math.round(w*dpr); canvas.height = Math.round(h*dpr);
  ctx.scale(dpr,dpr);
  const max = Math.max(...data, 1), pad = 12;
  const xs = data.map((_, i) => pad + (w-pad*2)*i/(data.length-1));
  const ys = data.map(v => h-pad-(h-pad*2)*v/max);
  // Translucent area under the line. Use globalAlpha with the RAW color
  // string so oklch()/rgb()/hex all work — appending "22" (as before) is
  // only valid for hex and made fillStyle fall back to BLACK for oklch
  // variables, painting a giant black slab over the whole chart.
  ctx.globalAlpha = 0.14;
  ctx.fillStyle = color;
  ctx.beginPath();
  ctx.moveTo(xs[0], h-pad);
  xs.forEach((x,i)=>ctx.lineTo(x, ys[i]));
  ctx.lineTo(xs[xs.length-1], h-pad);
  ctx.closePath();
  ctx.fill();
  ctx.globalAlpha = 1;
  // The line itself.
  ctx.strokeStyle = color;
  ctx.lineWidth = 2;
  ctx.lineJoin = "round";
  ctx.lineCap = "round";
  ctx.beginPath();
  xs.forEach((x,i)=> i ? ctx.lineTo(x, ys[i]) : ctx.moveTo(x, ys[i]));
  ctx.stroke();
}
