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
      <!-- The advanced tuning form renders itself here, from
           window.Tuning. Kept in its own module because it is thirty
           fields with per-field auto-fill and reasoning, and inlining
           that would double the size of this file. -->
      <!-- Trusted proxies. Directly above the tuning form because it is
           the setting that decides whether the ban engine sees the real
           visitor at all — and getting it wrong bans the CDN. -->
      <div id="px-root"></div>

      <div id="tn-root"></div>

      <!-- The Telegram bot. In the Nginx tab because that is where the
           operational settings live; it reads the same health report the
           Status page does. -->
      <div class="card">
        <h3 class="card-title">${Icons.svg("activity",16)} ${t("tg.title")}</h3>
        <p class="muted tiny">${t("tg.lede")}</p>
        <label class="switch">
          <input type="checkbox" id="tg-on">
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("tg.enable")}</span>
        </label>
        <div id="tg-body" hidden>
          <div class="field field-wide">
            <label>${t("tg.token")}${Icons.help(t("tg.token_help"))}</label>
            <input id="tg-token" dir="ltr" class="mono" type="password"
                   placeholder="123456:ABC-DEF...">
            <p class="tiny muted" id="tg-token-note"></p>
          </div>
          <div class="field field-wide">
            <label>${t("tg.chats")}${Icons.help(t("tg.chats_help"))}</label>
            <input id="tg-chats" dir="ltr" class="mono" placeholder="123456789, -100987654321">
          </div>
          <label class="checkbox">
            <input type="checkbox" id="tg-alert-bans">
            <span class="check-box"></span><span>${t("tg.alert_bans")}</span>
          </label>
          <label class="checkbox">
            <input type="checkbox" id="tg-alert-nginx">
            <span class="check-box"></span><span>${t("tg.alert_nginx")}</span>
          </label>
          <p class="tiny muted" id="tg-state"></p>
        </div>
        <div class="btn-row">
          <button class="btn btn-primary" id="tg-save">${Icons.svg("check",14)} ${t("common.save")}</button>
          <button class="btn btn-ghost" id="tg-test">${Icons.svg("zap",14)} ${t("tg.test")}</button>
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

    // ── Advanced nginx tuning ──────────────────────────────
    // Loaded lazily: the operator has to open the Nginx tab to see it, and
    // fetching thirty fields' worth of recommendations for somebody who
    // only came to change their password is waste.
    const tnRoot = document.getElementById("tn-root");
    let tnLoaded = false;

    const paintTuning = (data) => {
      window.Tuning.render(tnRoot, data, t, Icons, {
        toast,
        applyProfile: async (profile) => {
          try {
            await api("/api/settings/tuning", {
              method: "PUT", body: JSON.stringify({ profile, enabled: true }),
            });
            toast(t("tune.profile_applied"), "success");
            loadTuning();
          } catch (e) { toast(e.message, "error"); }
        },
        measure: async () => {
          const note = document.getElementById("tn-measure-note");
          const btn = document.getElementById("tn-measure");
          btn.disabled = true;
          note.textContent = t("tune.measuring");
          try {
            const r = await api("/api/settings/tuning/measure", { method: "POST" });
            if (r.ok) {
              note.textContent = r.mbits + " Mbit/s";
              // Re-fetch so every recommendation is recomputed from the
              // measured figure rather than the kernel's guess.
              loadTuning();
            } else {
              note.textContent = t("tune.measure_failed");
            }
          } catch (e) {
            note.textContent = e.message;
          } finally {
            btn.disabled = false;
          }
        },
        save: async (body) => {
          const btn = document.getElementById("tn-save");
          btn.disabled = true;
          try {
            const res = await api("/api/settings/tuning", {
              method: "PUT", body: JSON.stringify(body),
            });
            if (res.main_error) {
              // The nginx.conf edit is the dangerous one and is reported
              // separately: it has already been rolled back, and saying
              // so is far more useful than a generic reload failure.
              toast(t("tune.main_failed") + ": " + res.main_error, "error");
            } else if (res.applied === false) {
              toast(t("services.apply_failed") +
                (res.apply_error ? ": " + res.apply_error : ""), "error");
            } else {
              toast(t("settings.saved"), "success");
            }
            loadTuning();
          } catch (e) {
            toast(e.message, "error");
          } finally {
            btn.disabled = false;
          }
        },
      });
    };

    // ── Trusted proxies and crawlers ───────────────────────
    const pxRoot = document.getElementById("px-root");

    const pxPaint = (d) => {
      const warn = (d.warnings || []).length
        ? `<div class="hp-conflict tn-warn">${Icons.svg("warning", 14)}
             <div>${(d.warnings || []).map(w =>
               `<div class="tiny">${t("proxies.warn_" + w)}</div>`).join("")}</div>
           </div>`
        : "";
      pxRoot.innerHTML = `
        <div class="card">
          <h3 class="card-title">${Icons.svg("globe",16)} ${t("proxies.title")}</h3>
          <p class="muted tiny">${t("proxies.lede")}</p>
          ${warn}
          <div class="px-stats">
            <span class="badge badge-neutral">\u2068${d.trusted_count} ${t("proxies.trusted_now")}\u2069</span>
            <span class="badge badge-neutral">\u2068${d.never_ban_count} ${t("proxies.never_ban_now")}\u2069</span>
          </div>
          <div class="px-lists">
            ${(d.lists || []).map(l => `
              <label class="px-item ${l.enabled ? "on" : ""}">
                <input type="checkbox" data-px="${l.id}" ${l.enabled ? "checked" : ""}>
                <span class="check-box"></span>
                <span class="px-body">
                  <span class="px-name">${l.id}</span>
                  <span class="px-meta tiny muted">
                    ${l.kind === "proxy" ? t("proxies.proxy_kind") : t("proxies.crawler_kind")}
                    · \u2068${l.count} ${t("proxies.ranges")}\u2069
                    ${l.header ? `· <code dir="ltr">${l.header}</code>` : ""}
                  </span>
                </span>
              </label>`).join("")}
          </div>
          <p class="tiny muted">${t("proxies.public_note")}</p>
          <div class="field field-wide">
            <label>${t("proxies.extra_trusted")}${Icons.help(t("proxies.extra_trusted_help"))}</label>
            <input id="px-trust" dir="ltr" class="mono"
                   value="${(d.extra_trusted_cidrs || []).join(", ")}"
                   placeholder="203.0.113.0/24">
          </div>
          <div class="field field-wide">
            <label>${t("proxies.extra_never_ban")}${Icons.help(t("proxies.extra_never_ban_help"))}</label>
            <input id="px-never" dir="ltr" class="mono"
                   value="${(d.extra_never_ban_cidrs || []).join(", ")}"
                   placeholder="10.0.0.0/8, 192.0.2.5">
          </div>
          <div class="btn-row">
            <button class="btn btn-primary" id="px-save">
              ${Icons.svg("check",14)} ${t("common.save")}
            </button>
          </div>
        </div>`;

      pxRoot.querySelectorAll("[data-px]").forEach(cb => {
        cb.onchange = () => cb.closest(".px-item").classList.toggle("on", cb.checked);
      });
      document.getElementById("px-save").onclick = async () => {
        const enabled = {};
        pxRoot.querySelectorAll("[data-px]").forEach(cb => { enabled[cb.dataset.px] = cb.checked; });
        const split = v => v.split(/[,\s]+/).map(x => x.trim()).filter(Boolean);
        try {
          await api("/api/settings/proxies", {
            method: "PUT",
            body: JSON.stringify({
              enabled,
              extra_trusted_cidrs: split(document.getElementById("px-trust").value),
              extra_never_ban_cidrs: split(document.getElementById("px-never").value),
            }),
          });
          toast(t("settings.saved"), "success");
          pxLoad();
        } catch (e) { toast(e.message, "error"); }
      };
    };

    const pxLoad = async () => {
      try { pxPaint(await api("/api/settings/proxies")); }
      catch (e) { pxRoot.innerHTML = `<div class="card"><p class="muted tiny">${e.message}</p></div>`; }
    };

    // ── Telegram bot ───────────────────────────────────────
    const tgPaint = (d) => {
      document.getElementById("tg-on").checked = !!d.enabled;
      document.getElementById("tg-body").hidden = !d.enabled;
      document.getElementById("tg-chats").value = (d.chat_ids || []).join(", ");
      document.getElementById("tg-alert-bans").checked = !!d.alert_bans;
      document.getElementById("tg-alert-nginx").checked = !!d.alert_nginx;
      // The token is never sent back to the browser — putting a bot token
      // in page history and on screen serves no purpose. The form only
      // reports whether one is stored, and an empty box means "keep it".
      document.getElementById("tg-token-note").textContent =
        d.token_set ? t("tg.token_stored") : t("tg.token_missing");
      const st = document.getElementById("tg-state");
      st.textContent = d.last_error
        ? t("tg.error") + ": " + d.last_error
        : (d.running ? t("tg.running") : t("tg.stopped"));
      st.className = "tiny " + (d.last_error ? "hp-warn" : "muted");
    };

    const tgLoad = async () => {
      try { tgPaint(await api("/api/settings/telegram")); } catch (e) {}
    };

    const tgSave = async (test) => {
      const chats = document.getElementById("tg-chats").value
        .split(/[,\s]+/).map(x => parseInt(x, 10)).filter(x => !isNaN(x));
      const body = {
        enabled: document.getElementById("tg-on").checked,
        chat_ids: chats,
        alert_bans: document.getElementById("tg-alert-bans").checked,
        alert_nginx: document.getElementById("tg-alert-nginx").checked,
        test: !!test,
      };
      const tok = document.getElementById("tg-token").value.trim();
      if (tok) body.token = tok;
      try {
        const r = await api("/api/settings/telegram", {
          method: "PUT", body: JSON.stringify(body),
        });
        document.getElementById("tg-token").value = "";
        toast(test && r.test === "sent" ? t("tg.test_sent") : t("settings.saved"),
              "success");
        tgLoad();
      } catch (e) { toast(e.message, "error"); }
    };

    document.getElementById("tg-on").onchange = (e) => {
      document.getElementById("tg-body").hidden = !e.target.checked;
    };
    document.getElementById("tg-save").onclick = () => tgSave(false);
    document.getElementById("tg-test").onclick = () => tgSave(true);

    const loadTuning = async () => {
      try {
        paintTuning(await api("/api/settings/tuning"));
      } catch (e) {
        tnRoot.innerHTML = `<div class="card"><p class="muted tiny">${e.message}</p></div>`;
      }
    };

    // Tabs.
    container.querySelectorAll("#set-tabs .tab").forEach(b => b.onclick = () => {
      if (b.dataset.pane === "nginx" && !tnLoaded) {
        tnLoaded = true; loadTuning(); tgLoad(); pxLoad();
      }
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
