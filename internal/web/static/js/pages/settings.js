/* Settings page */
window.Pages = window.Pages || {};
window.Pages.settings = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal } = ctx;
    const [panel, nginx, sec, ui] = await Promise.all([
      api("/api/settings/panel"), api("/api/settings/nginx"),
      api("/api/settings/security"), api("/api/settings/ui"),
    ]);
    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("settings",20)} ${t("settings.title")}</h1></div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("server",16)} ${t("settings.panel")}</h3>
        <div class="field-row">
          <div class="field"><label>${t("settings.domain")}</label><input dir="ltr" style="text-align:left" id="p-dom" value="${panel.domain||""}"></div>
          <div class="field"><label>${t("settings.subdomain")}</label><input dir="ltr" style="text-align:left" id="p-sub" value="${panel.subdomain||""}"></div>
        </div>
        <div class="field-row">
          <div class="field"><label>${t("settings.local_port")}</label><input id="p-lp" type="number" value="${panel.local_port}"></div>
          <div class="field"><label>${t("settings.listen_port")}</label><input id="p-lip" type="number" value="${panel.listen_port}"></div>
        </div>
        <div class="field"><label>${t("settings.path")}</label>
          <div class="input-action"><input dir="ltr" style="text-align:left" id="p-path" value="${panel.path||""}">
          <button class="btn btn-ghost btn-sm" id="p-rand">${Icons.svg("refresh",13)}</button></div></div>
        <div class="field"><label>${t("settings.cert")}</label><input dir="ltr" style="text-align:left" id="p-cert" value="${panel.cert||""}"></div>
        <div class="field"><label>${t("settings.key")}</label><input dir="ltr" style="text-align:left" id="p-key" value="${panel.key||""}"></div>
        <button class="btn btn-primary" id="p-save">${Icons.svg("check",14)} Save</button>
      </div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("shield",16)} ${t("settings.security")}</h3>
        <div class="field"><label>${t("settings.allowed_ips")} <small>(CIDR, one per line)</small></label>
          <textarea id="s-ips" rows="3">${(sec.auth.allowed_ips||[]).join("\\n")}</textarea></div>
        <label class="switch"><input type="checkbox" id="s-wl" ${sec.auth.ip_whitelist_enabled?"checked":""}> ${t("settings.ip_whitelist")}</label>
        <label class="switch"><input type="checkbox" id="s-rl" ${sec.security.rate_limit_enabled?"checked":""}> ${t("settings.rate_limit")}</label>
        <div class="field"><label>${t("settings.session_timeout")}</label><input id="s-to" type="number" value="${sec.security.session_timeout_minutes}"></div>
        <button class="btn btn-primary" id="s-save">${Icons.svg("check",14)} Save</button>
      </div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("zap",16)} Nginx</h3>
        <div class="field-row">
          <div class="field"><label>worker_connections</label><input id="n-wc" type="number" value="${nginx.worker_connections}"></div>
          <div class="field"><label>log level</label>
            <select id="n-ll"><option>${nginx.log_level}</option>
              ${["error","warn","info","debug"].filter(l=>l!==nginx.log_level).map(l=>`<option>${l}</option>`).join("")}
            </select></div>
        </div>
        <label class="switch"><input type="checkbox" id="n-cache" ${nginx.cache_enabled?"checked":""}> Cache enabled</label>
        <div class="btn-row">
          <button class="btn btn-ghost" id="n-reload">${Icons.svg("refresh",14)} Reload</button>
          <button class="btn btn-primary" id="n-save">${Icons.svg("check",14)} Save</button>
        </div>
      </div>`;
    document.getElementById("p-rand").onclick=()=>{
      const c="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
      document.getElementById("p-path").value=Array.from({length:22},()=>c[Math.random()*c.length|0]).join("");
    };
    document.getElementById("p-save").onclick=async()=>{
      await api("/api/settings/panel",{method:"PUT",body:JSON.stringify({
        domain:document.getElementById("p-dom").value.trim(),
        subdomain:document.getElementById("p-sub").value.trim(),
        local_port:+document.getElementById("p-lp").value,
        listen_port:+document.getElementById("p-lip").value,
        path:document.getElementById("p-path").value.trim(),
        cert:document.getElementById("p-cert").value.trim().replace(/\/+$/g, ""),
        key:document.getElementById("p-key").value.trim().replace(/\/+$/g, "")})});
      location.reload();
    };
    document.getElementById("s-save").onclick=async()=>{
      await api("/api/settings/security",{method:"PUT",body:JSON.stringify({
        allowed_ips:document.getElementById("s-ips").value.split("\\n").map(s=>s.trim()).filter(Boolean),
        ip_whitelist_enabled:document.getElementById("s-wl").checked,
        rate_limit_enabled:document.getElementById("s-rl").checked,
        session_timeout_minutes:+document.getElementById("s-to").value})});
      location.reload();
    };
    document.getElementById("n-save").onclick=async()=>{
      await api("/api/settings/nginx/cache",{method:"PUT",body:JSON.stringify({enabled:document.getElementById("n-cache").checked})});
      await api("/api/settings/nginx/connections",{method:"PUT",body:JSON.stringify({worker_connections:+document.getElementById("n-wc").value})});
      await api("/api/settings/nginx/log-level",{method:"PUT",body:JSON.stringify({level:document.getElementById("n-ll").value})});
      await api("/api/settings/generate",{method:"POST"});
      location.reload();
    };
    document.getElementById("n-reload").onclick=()=>api("/api/settings/nginx/reload",{method:"POST"});
  }
};
