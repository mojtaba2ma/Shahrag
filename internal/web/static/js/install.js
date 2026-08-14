/* Shahrag installer wizard */
(function () {
  "use strict";

  const t = (key) => {
    const dict = window.I18N.fa || {};
    return key.split(".").reduce((o, k) => (o && typeof o === "object" ? o[k] : key), dict);
  };

  const STEPS = [
    { id: "domain", label: t("install.step_domain") },
    { id: "cert", label: t("install.step_cert") },
    { id: "port", label: t("install.step_port") },
    { id: "path", label: t("install.step_path") },
    { id: "auth", label: t("install.step_auth") },
    { id: "finish", label: t("install.step_finish") },
  ];

  const state = {
    step: 0,
    has_domain: true,
    domain: "",
    subdomain: "",
    use_custom_cert: false,
    cert_path: "",
    key_path: "",
    local_port: 0,
    panel_path: "",
    listen_port: 443,
    username: "admin",
    password: "",
    token_required: false,
    install_token: "",
  };

  function randomPort() {
    return 10000 + Math.floor(Math.random() * 55000);
  }
  function randomPath(len = 22) {
    const c = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
    return Array.from({ length: len }, () => c[Math.floor(Math.random() * c.length)]).join("");
  }

  function renderStepper() {
    document.getElementById("stepper").innerHTML = STEPS.map((s, i) => `
      <div class="step ${i === state.step ? "active" : ""} ${i < state.step ? "done" : ""}">
        <div class="step-dot">${i < state.step ? Icons.svg("check",14) : i + 1}</div>
        <span>${s.label}</span>
      </div>
    `).join("");
  }

  function renderStep() {
    const form = document.getElementById("install-form");
    const back = document.getElementById("btn-back");
    const next = document.getElementById("btn-next");
    back.disabled = state.step === 0;
    back.innerHTML = Icons.svg("chevron",14) + `<span style="margin-inline-start:6px">${t("install.back")}</span>`;

    switch (state.step) {
      case 0: // Domain
        form.innerHTML = `
          <div class="field">
            <label>${t("install.domain_question")}</label>
            <div style="display:flex;gap:16px;margin-top:8px">
              <label class="checkbox"><input type="radio" name="hasdomain" value="yes" ${state.has_domain ? "checked" : ""}> ${t("common.yes")}</label>
              <label class="checkbox"><input type="radio" name="hasdomain" value="no" ${!state.has_domain ? "checked" : ""}> ${t("common.no")}</label>
            </div>
          </div>
          <div id="domain-fields" ${!state.has_domain ? "hidden" : ""}>
            <div class="field">
              <label>${t("install.domain_name")}</label>
              <input type="text" id="f-domain" value="${state.domain}" placeholder="example.com" required>
            </div>
            <div class="field">
              <label>${t("install.subdomain")}</label>
              <input type="text" id="f-subdomain" value="${state.subdomain}" placeholder="panel" required>
            </div>
          </div>
        `;
        form.querySelectorAll('input[name=hasdomain]').forEach(r => {
          r.onchange = e => {
            state.has_domain = e.target.value === "yes";
            document.getElementById("domain-fields").hidden = !state.has_domain;
          };
        });
        if (state.domain) document.getElementById("f-domain").value = state.domain;
        if (state.subdomain) document.getElementById("f-subdomain").value = state.subdomain;
        next.innerHTML = `<span>${t("install.next")}</span> ${Icons.svg("chevron",14)}`;
        break;

      case 1: // Certificate
        if (!state.has_domain) { state.step = 2; renderStep(); return; }
        form.innerHTML = `
          <div class="field">
            <label>${t("install.custom_cert")}</label>
            <div style="display:flex;gap:16px;margin-top:8px">
              <label class="checkbox"><input type="radio" name="hascert" value="yes" ${state.use_custom_cert ? "checked" : ""}> ${t("common.yes")}</label>
              <label class="checkbox"><input type="radio" name="hascert" value="no" ${!state.use_custom_cert ? "checked" : ""}> ${t("common.no")} (Let's Encrypt / none)</label>
            </div>
          </div>
          <div id="cert-fields" ${!state.use_custom_cert ? "hidden" : ""}>
            <div class="field">
              <label>${t("install.cert_path")}</label>
              <input type="text" id="f-cert" dir="ltr" style="text-align:left" value="${state.cert_path}" placeholder="/etc/letsencrypt/live/example.com/fullchain.pem">
            </div>
            <div class="field">
              <label>${t("install.key_path")}</label>
              <input type="text" id="f-key" dir="ltr" style="text-align:left" value="${state.key_path}" placeholder="/etc/letsencrypt/live/example.com/privkey.pem">
            </div>
          </div>
        `;
        form.querySelectorAll('input[name=hascert]').forEach(r => {
          r.onchange = e => {
            state.use_custom_cert = e.target.value === "yes";
            document.getElementById("cert-fields").hidden = !state.use_custom_cert;
          };
        });
        next.innerHTML = `<span>${t("install.next")}</span> ${Icons.svg("chevron",14)}`;
        break;

      case 2: // Port
        if (!state.local_port) state.local_port = randomPort();
        form.innerHTML = `
          <div class="field">
            <label>${t("install.local_port")}</label>
            <div class="input-with-action">
              <input type="number" id="f-lport" value="${state.local_port}" min="1" max="65535" required>
              <button type="button" class="random-btn" id="rnd-lport">${Icons.svg("refresh",13)} ${t("install.random")}</button>
            </div>
            <div class="hint">پورتی که پنل روی سرور اجرا می‌شود (فقط لوکال)</div>
          </div>
        `;
        document.getElementById("rnd-lport").onclick = () => {
          state.local_port = randomPort();
          document.getElementById("f-lport").value = state.local_port;
        };
        next.innerHTML = `<span>${t("install.next")}</span> ${Icons.svg("chevron",14)}`;
        break;

      case 3: // Path
        if (!state.panel_path) state.panel_path = randomPath();
        form.innerHTML = `
          <div class="field">
            <label>${t("install.penal_path" || "install.panel_path" || "Panel Path")}</label>
            <div class="input-with-action">
              <input type="text" id="f-path" dir="ltr" style="text-align:left" value="${state.panel_path}" required pattern="[a-zA-Z0-9_-]+" minlength="8">
              <button type="button" class="random-btn" id="rnd-path">${Icons.svg("refresh",13)} ${t("install.random")}</button>
            </div>
            <div class="hint">۲۲ کاراکتر، حروف بزرگ و کوچک. این مسیر هسته پنل است.</div>
          </div>
        `;
        document.getElementById("rnd-path").onclick = () => {
          state.panel_path = randomPath();
          document.getElementById("f-path").value = state.panel_path;
        };
        next.innerHTML = `<span>${t("install.next")}</span> ${Icons.svg("chevron",14)}`;
        break;

      case 4: // Auth
        form.innerHTML = `
          <div class="field">
            <label>${t("install.username")}</label>
            <input type="text" id="f-username" value="${state.username}" required>
          </div>
          <div class="field">
            <label>${t("install.password")}</label>
            <input type="password" id="f-password" required minlength="6">
          </div>
          <div class="field">
            <label>${t("install.password")} (تکرار)</label>
            <input type="password" id="f-password2" required minlength="6">
          </div>
          ${state.token_required ? `
          <div class="field">
            <label>${t("install.install_token")}</label>
            <input type="text" id="f-token" dir="ltr" style="text-align:left;font-family:monospace" value="${state.install_token}" placeholder="••••••••••••••••••••••••" required>
            <div class="hint">${t("install.install_token_hint")}</div>
          </div>` : ""}
        `;
        next.innerHTML = `${Icons.svg("check",14)} <span>${t("install.install")}</span>`;
        break;

      case 5: // Finish — submit
        submitInstall();
        return;
    }
    renderStepper();
  }

  function collectStep() {
    const err = document.getElementById("install-error");
    err.hidden = true;
    try {
      switch (state.step) {
        case 0:
          state.has_domain = document.querySelector('input[name=hasdomain]:checked').value === "yes";
          if (state.has_domain) {
            state.domain = document.getElementById("f-domain").value.trim();
            state.subdomain = document.getElementById("f-subdomain").value.trim();
            if (!state.domain || !state.subdomain) throw new Error("Domain and subdomain required");
            if (!/^([a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}$/.test(state.domain)) throw new Error("Invalid domain");
          }
          break;
        case 1:
          if (state.has_domain) {
            state.use_custom_cert = document.querySelector('input[name=hascert]:checked').value === "yes";
            if (state.use_custom_cert) {
              state.cert_path = document.getElementById("f-cert").value.trim().replace(/\/+$/g, "");
              state.key_path = document.getElementById("f-key").value.trim().replace(/\/+$/g, "");
            }
          }
          break;
        case 2:
          state.local_port = parseInt(document.getElementById("f-lport").value);
          if (isNaN(state.local_port) || state.local_port < 1) throw new Error("Invalid port");
          break;
        case 3:
          state.panel_path = document.getElementById("f-path").value.trim().replace(/^\/+|\/+$/g, "");
          if (!/^[a-zA-Z0-9_-]{4,}$/.test(state.panel_path)) throw new Error("Path must be alphanumeric (min 4 chars)");
          break;
        case 4:
          state.username = document.getElementById("f-username").value.trim() || "admin";
          state.password = document.getElementById("f-password").value;
          const p2 = document.getElementById("f-password2").value;
          if (state.password.length < 6) throw new Error("Password must be at least 6 characters");
          if (state.password !== p2) throw new Error("Passwords do not match");
          if (state.token_required) {
            state.install_token = document.getElementById("f-token").value.trim();
            if (!state.install_token) throw new Error(t("install.install_token") + " required");
          }
          break;
      }
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      return false;
    }
    return true;
  }

  async function submitInstall() {
    const form = document.getElementById("install-form");
    form.innerHTML = `<div class="empty-state"><div class="loading-spinner" style="margin:0 auto 16px"></div><p>${t("install.installing")}</p></div>`;
    document.getElementById("btn-next").disabled = true;
    document.getElementById("btn-back").disabled = true;

    try {
      const headers = { "Content-Type": "application/json" };
      if (state.token_required && state.install_token) {
        headers["X-Install-Token"] = state.install_token;
      }
      const res = await fetch("/api/install/run", {
        method: "POST",
        headers,
        body: JSON.stringify({
          has_domain: state.has_domain,
          domain: state.domain,
          subdomain: state.subdomain,
          use_custom_cert: state.use_custom_cert,
          cert_path: state.cert_path,
          key_path: state.key_path,
          local_port: state.local_port,
          panel_path: state.panel_path,
          listen_port: 443,
          username: state.username,
          password: state.password,
        }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.detail || "Install failed");

      document.getElementById("install-card").hidden = true;
      const success = document.getElementById("install-success");
      success.hidden = false;
      document.getElementById("success-title").textContent = t("install.success");
      document.getElementById("success-desc").textContent = "";
      const target = data.panel.url;
      document.getElementById("success-url").innerHTML =
        `<a href="${target}" target="_blank">${target}</a>`;
      document.getElementById("success-hint").textContent = t("install.panel_url");
      setTimeout(() => { window.location.href = target; }, 2500);
    } catch (e) {
      document.getElementById("install-error").textContent = e.message;
      document.getElementById("install-error").hidden = false;
      document.getElementById("btn-next").disabled = false;
      state.step = 4;
      renderStep();
    }
  }

  document.getElementById("btn-next").onclick = () => {
    if (!collectStep()) return;
    if (state.step < STEPS.length - 1) {
      state.step++;
      renderStep();
    }
  };
  document.getElementById("btn-back").onclick = () => {
    if (state.step > 0) { state.step--; renderStep(); }
  };

  // Check if already installed
  fetch("/api/install/status").then(r => r.json()).then(s => {
    if (s.installed) {
      window.location.href = "/";
      return;
    }
    state.token_required = !!s.token_required;
    if (s.defaults) {
      state.local_port = s.defaults.local_port;
      state.panel_path = s.defaults.panel_path;
    }
    renderStep();
  });
})();
