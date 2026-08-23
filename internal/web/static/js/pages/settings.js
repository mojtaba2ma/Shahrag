/* Settings page */
window.Pages = window.Pages || {};
window.Pages.settings = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal, toast, navigate } = ctx;
    const [panel, nginx, sec, ui] = await Promise.all([
      api("/api/settings/panel"), api("/api/settings/nginx"),
      api("/api/settings/security"), api("/api/settings/ui"),
    ]);
    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("settings",20)} ${t("settings.title")}</h1></div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("server",16)} ${t("settings.panel")}</h3>
        <div class="field-row">
          <div class="field"><label>${t("settings.domain")}</label><input dir="ltr" class="mono" id="p-dom" value="${panel.domain||""}"></div>
          <div class="field"><label>${t("settings.subdomain")}</label><input dir="ltr" class="mono" id="p-sub" value="${panel.subdomain||""}"></div>
        </div>
        <div class="field-row">
          <div class="field field-port"><label>${t("settings.local_port")}</label><input id="p-lp" type="number" value="${panel.local_port}" inputmode="numeric"></div>
          <div class="field field-port"><label>${t("settings.listen_port")}</label><input id="p-lip" type="number" value="${panel.listen_port}" inputmode="numeric"></div>
        </div>
        <div class="field field-wide"><label>${t("settings.path")}</label>
          <div class="input-action"><input dir="ltr" class="mono" id="p-path" value="${panel.path||""}">
          <button class="btn btn-ghost btn-sm" id="p-rand">${Icons.svg("refresh",13)}</button></div></div>
        <div class="field field-wide"><label>${t("settings.cert")}</label><input dir="ltr" class="mono" id="p-cert" value="${panel.cert||""}"></div>
        <div class="field field-wide"><label>${t("settings.key")}</label><input dir="ltr" class="mono" id="p-key" value="${panel.key||""}"></div>
        <button class="btn btn-primary" id="p-save">${Icons.svg("check",14)} Save</button>
      </div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("shield",16)} ${t("settings.security")}</h3>
        <div class="field"><label>${t("settings.allowed_ips")} <small>(CIDR, one per line)</small></label>
          <textarea id="s-ips" rows="3">${(sec.auth.allowed_ips||[]).join("\\n")}</textarea></div>
        <label class="switch"><input type="checkbox" id="s-wl" ${sec.auth.ip_whitelist_enabled?"checked":""}><span class="switch-track"><span class="switch-thumb"></span></span><span>${t("settings.ip_whitelist")}</span></label>
        <label class="switch"><input type="checkbox" id="s-rl" ${sec.security.rate_limit_enabled?"checked":""}><span class="switch-track"><span class="switch-thumb"></span></span><span>${t("settings.rate_limit")}</span></label>
        <div class="field"><label>${t("settings.session_timeout")}</label><input id="s-to" type="number" min="1" value="${sec.security.session_timeout_minutes}"></div>
        <label class="switch"><input type="checkbox" id="s-lock-en" ${(sec.security.lock_minutes??60)>=0?"checked":""}><span class="switch-track"><span class="switch-thumb"></span></span><span>${t("settings.lock_enabled")}</span></label>
        <div class="field"><label>${t("settings.lock_minutes")}</label><input id="s-lock" type="number" min="1" max="10080" value="${Math.max((sec.security.lock_minutes??60),1)}">
        <div class="hint" style="font-size:11px;color:var(--text-faint);margin-top:4px">${t("settings.lock_minutes_hint")}</div></div>
        <button class="btn btn-primary" id="s-save">${Icons.svg("check",14)} Save</button>
      </div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("globe",16)} ${t("settings.ui")}</h3>
        <div class="field-row">
          <div class="field"><label>${t("settings.language")}</label>
            <select id="u-lang">
              ${["fa","en","ar","tr","zh","ja","ko","pt","es","ru"].map(l=>`<option value="${l}" ${ui.language===l?"selected":""}>${l}</option>`).join("")}
            </select></div>
          <div class="field"><label>${t("settings.theme")}</label>
            <select id="u-theme">
              ${["midnight","aurora","sunset","forest","light","high-contrast"].map(th=>`<option value="${th}" ${ui.theme===th?"selected":""}>${th}</option>`).join("")}
            </select></div>
        </div>
        <div class="btn-row"><button class="btn btn-primary" id="u-save">${Icons.svg("check",14)} Save</button></div>
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
        <label class="switch"><input type="checkbox" id="n-cache" ${nginx.cache_enabled?"checked":""}><span class="switch-track"><span class="switch-thumb"></span></span><span>Cache enabled</span></label>
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
      try {
        await api("/api/settings/panel",{method:"PUT",body:JSON.stringify({
          domain:document.getElementById("p-dom").value.trim(),
          subdomain:document.getElementById("p-sub").value.trim(),
          local_port:+document.getElementById("p-lp").value,
          listen_port:+document.getElementById("p-lip").value,
          path:document.getElementById("p-path").value.trim(),
          cert:document.getElementById("p-cert").value.trim().replace(/\/+$/g, ""),
          key:document.getElementById("p-key").value.trim().replace(/\/+$/g, "")})});
        toast(t("settings.saved"),"success");
        navigate("settings");
      } catch(e) { toast(e.message,"error"); }
    };
    document.getElementById("u-save").onclick=async()=>{
      try {
        const lang = document.getElementById("u-lang").value;
        const theme = document.getElementById("u-theme").value;
        await api("/api/settings/ui",{method:"PUT",body:JSON.stringify({language:lang, theme:theme})});
        if (window.ShahragApplyUI) window.ShahragApplyUI(lang, theme);
        toast(t("settings.saved"),"success");
      } catch(e) { toast(e.message,"error"); }
    };
    const lockEn = document.getElementById("s-lock-en");
    const lockIn = document.getElementById("s-lock");
    const syncLockInput = () => { lockIn.disabled = !lockEn.checked; };
    lockEn.onchange = syncLockInput; syncLockInput();
    document.getElementById("s-save").onclick=async()=>{
      try {
        const savedLock = lockEn.checked ? (+lockIn.value||60) : -1;
        await api("/api/settings/security",{method:"PUT",body:JSON.stringify({
          allowed_ips:document.getElementById("s-ips").value.split("\\n").map(s=>s.trim()).filter(Boolean),
          ip_whitelist_enabled:document.getElementById("s-wl").checked,
          rate_limit_enabled:document.getElementById("s-rl").checked,
          session_timeout_minutes:+document.getElementById("s-to").value,
          lock_minutes: savedLock})});
        if (window.ShahragSetLockMinutes) window.ShahragSetLockMinutes(savedLock);
        toast(t("settings.saved"),"success");
        navigate("settings");
      } catch(e) { toast(e.message,"error"); }
    };
    document.getElementById("n-save").onclick=async()=>{
      try {
      await api("/api/settings/nginx/cache",{method:"PUT",body:JSON.stringify({enabled:document.getElementById("n-cache").checked})});
      await api("/api/settings/nginx/connections",{method:"PUT",body:JSON.stringify({worker_connections:+document.getElementById("n-wc").value})});
      await api("/api/settings/nginx/log-level",{method:"PUT",body:JSON.stringify({level:document.getElementById("n-ll").value})});
      await api("/api/settings/generate",{method:"POST"});
        toast(t("settings.saved"),"success");
        navigate("settings");
      } catch(e) { toast(e.message,"error"); }
    };
    document.getElementById("n-reload").onclick=()=>api("/api/settings/nginx/reload",{method:"POST"});
  }
};
