/* Settings page */
window.Pages = window.Pages || {};
window.Pages.settings = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal, toast, navigate } = ctx;
    const [panel, nginx, sec, ui, sni] = await Promise.all([
      api("/api/settings/panel"), api("/api/settings/nginx"),
      api("/api/settings/security"), api("/api/settings/ui"),
      api("/api/reality").catch(() => ({ enabled: false, http_port: 6038, resolvers: [] })),
    ]);
    const sniResolvers = (sni.resolvers && sni.resolvers.length
      ? sni.resolvers : ["1.1.1.1", "8.8.8.8"]).join(", ");
    // Panel settings and nginx settings are unrelated concerns that were
    // stacked in one long scroll. Tabs separate them without adding another
    // top-level menu entry.
    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("settings",20)} ${t("settings.title")}</h1></div>
      <div class="tabs" id="set-tabs" style="margin-bottom:16px">
        <button class="tab active" data-pane="panel">${Icons.svg("server",14)} ${t("settings.panel")}</button>
        <button class="tab" data-pane="nginx">${Icons.svg("zap",14)} Nginx</button>
      </div>
      <div data-pane-body="panel">
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
        <div class="field"><label>${t("settings.lock_minutes")}${Icons.help(t("settings.lock_minutes_hint"))}</label><input id="s-lock" type="number" min="1" max="10080" value="${Math.max((sec.security.lock_minutes??60),1)}"></div>
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
      </div>
      <div data-pane-body="nginx" hidden>
      <div class="card">
        <h3 class="card-title">${Icons.svg("reality",16)} ${t("reality.title")}</h3>
        <label class="switch">
          <input type="checkbox" id="sni-en" ${sni.enabled ? "checked" : ""}>
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("reality.enabled")}</span>
        </label>
        <div class="field-row">
          <div class="field field-port">
            <label>${t("reality.http_port")}</label>
            <input id="sni-port" type="number" inputmode="numeric" min="1" max="65535" value="${sni.http_port || 6038}">
          </div>
          <div class="field field-wide">
            <label>${t("reality.resolvers")}${Icons.help(t("reality.resolvers_hint"))}</label>
            <input id="sni-res" dir="ltr" class="mono" value="${sniResolvers}" placeholder="1.1.1.1, 8.8.8.8">
          </div>
        </div>
        <div class="btn-row"><button class="btn btn-primary" id="sni-save">${Icons.svg("check",14)} ${t("common.save")}</button></div>
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
      </div>
      </div>
      <div class="card">
        <h3 class="card-title">${Icons.svg("download",16)} ${t("settings.backup")}</h3>
        <p class="muted" style="margin-bottom:12px">${t("settings.backup_hint")}</p>
        <div class="btn-row">
          <button class="btn btn-ghost" id="b-export">${Icons.svg("download",14)} ${t("settings.export_config")}</button>
          <button class="btn btn-ghost" id="b-import">${Icons.svg("upload",14)} ${t("settings.import_config")}</button>
          <input type="file" id="b-file" accept="application/json,.json" hidden>
        </div>
      </div>`;

    // Tabs.
    container.querySelectorAll("#set-tabs .tab").forEach(b => b.onclick = () => {
      container.querySelectorAll("#set-tabs .tab").forEach(x => x.classList.remove("active"));
      b.classList.add("active");
      container.querySelectorAll("[data-pane-body]").forEach(p => {
        p.hidden = p.dataset.paneBody !== b.dataset.pane;
      });
    });

    // Backup: download the live config as a timestamped JSON file.
    document.getElementById("b-export").onclick = async () => {
      try {
        const cfg = await api("/api/settings/backup");
        const stamp = new Date().toISOString().slice(0,19).replace(/[:T]/g,"-");
        const blob = new Blob([JSON.stringify(cfg, null, 2)], { type: "application/json" });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = `shahrag-config-${stamp}.json`;
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
        toast(t("settings.saved"), "success");
      } catch (e) { toast(e.message, "error"); }
    };

    // Restore: confirm first, because it replaces the whole configuration
    // and regenerates nginx.
    document.getElementById("b-import").onclick = () => document.getElementById("b-file").click();
    document.getElementById("b-file").onchange = async (ev) => {
      const file = ev.target.files && ev.target.files[0];
      if (!file) return;
      let parsed;
      try {
        parsed = JSON.parse(await file.text());
      } catch (e) {
        toast(t("settings.restore_bad_file"), "error");
        ev.target.value = "";
        return;
      }
      if (!parsed || typeof parsed !== "object" || !parsed.domains || !parsed.services) {
        toast(t("settings.restore_bad_file"), "error");
        ev.target.value = "";
        return;
      }
      ctx.confirmDialog(t("settings.restore_confirm"), async () => {
        try {
          await api("/api/settings/restore", { method: "POST", body: JSON.stringify(parsed) });
          toast(t("settings.restored"), "success");
          navigate("settings");
        } catch (e) { toast(e.message, "error"); }
      });
      ev.target.value = "";
    };
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

    document.getElementById("sni-save").onclick = async () => {
      try {
        const res = document.getElementById("sni-res").value
          .split(/[,\s]+/).map(x => x.trim()).filter(Boolean);
        const out = await api("/api/reality", {
          method: "PUT",
          body: JSON.stringify({
            enabled: document.getElementById("sni-en").checked,
            http_port: +document.getElementById("sni-port").value,
            resolvers: res,
          }),
        });
        // The server probes each resolver and warns when one answers a
        // relayed domain with this machine's own address (a routing loop).
        if (out && out.warning) toast(out.warning, "error");
        else toast(t("settings.saved"), "success");
      } catch (e) { toast(e.message, "error"); }
    };
  }
};
