/* Shahrag v1 — main application */
(function () {
  "use strict";

  const state = {
    config: null, currentPage: "dashboard", theme: "midnight", lang: "fa",
    authed: false, panelPath: "", lockMinutes: 0, sessionTimeout: 60,
  };
  let idleTimer = null;

  const LANGUAGES = [
    { code: "fa", label: "فارسی" },
    { code: "en", label: "English" },
    { code: "ar", label: "العربية" },
    { code: "tr", label: "Türkçe" },
    { code: "zh", label: "中文" },
    { code: "ja", label: "日本語" },
    { code: "ko", label: "한국어" },
    { code: "pt", label: "Português" },
    { code: "es", label: "Español" },
    { code: "ru", label: "Русский" },
  ];
  const THEMES = [
    { id: "midnight", label: "Midnight" },
    { id: "aurora", label: "Aurora" },
    { id: "sunset", label: "Sunset" },
    { id: "forest", label: "Forest" },
    { id: "light", label: "Light" },
    { id: "high-contrast", label: "High Contrast" },
  ];

  function t(key) {
    const parts = key.split(".");
    let val = window.I18N[state.lang] || window.I18N.en;
    for (const p of parts) {
      if (val && typeof val === "object") val = val[p];
      else return key;
    }
    return val || key;
  }

  function setLang(lang) {
    state.lang = lang;
    const dict = window.I18N[lang] || window.I18N.en;
    document.documentElement.lang = lang;
    document.documentElement.dir = dict._dir || "ltr";
    document.querySelectorAll("[data-i18n]").forEach(el => { el.textContent = t(el.dataset.i18n); });
    document.querySelectorAll("[data-i18n-ph]").forEach(el => { el.placeholder = t(el.dataset.i18nPh); });
    savePrefs();
    populateSelects();
    renderNav();
    if (state.authed) renderPage(state.currentPage);
  }

  function setTheme(theme) {
    state.theme = theme;
    document.documentElement.setAttribute("data-theme", theme);
    savePrefs();
  }

  function savePrefs() {
    fetch("/api/settings/ui", {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ theme: state.theme, language: state.lang }),
    }).catch(() => {});
  }

  async function api(path, opts = {}) {
    const res = await fetch(path, {
      headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
      credentials: "same-origin", ...opts,
    });
    if (res.status === 401) {
      const wasAuthed = state.authed;
      showLogin();
      let msg = t("login.session_expired");
      try {
        const b = await res.json();
        if (b && b.detail && b.detail.toLowerCase().includes("inactivity")) msg = t("login.locked");
      } catch (_) {}
      if (wasAuthed) toast(msg, "info");
      throw new Error(msg);
    }
    if (!res.ok) {
      let detail = res.statusText;
      try { const b = await res.json(); detail = b.detail || detail; } catch (_) {}
      throw new Error(detail);
    }
    return res.json();
  }

  // ── Toast ───────────────────────────────────────────────
  function toast(msg, type = "info") {
    const c = document.getElementById("toast-container");
    const el = document.createElement("div");
    el.className = `toast ${type}`;
    const icons = { success: "check", error: "warning", info: "info" };
    el.innerHTML = `<span class="toast-icon">${Icons.svg(icons[type] || "info", 16)}</span><span>${msg}</span>`;
    c.appendChild(el);
    setTimeout(() => { el.classList.add("removing"); setTimeout(() => el.remove(), 250); }, 3500);
  }

  // ── Modal ───────────────────────────────────────────────
  function modal(title, contentHTML, actions = [], wide = false) {
    const overlay = document.getElementById("modal-overlay");
    const m = document.getElementById("modal");
    m.className = wide ? "modal wide" : "modal";
    m.innerHTML = `
      <div class="modal-header">
        <h3 class="modal-title">${title}</h3>
        <button class="modal-close" id="modal-close-btn">${Icons.svg("close", 18)}</button>
      </div>
      <div class="modal-body">${contentHTML}</div>
      <div class="modal-footer" id="modal-footer"></div>`;
    document.getElementById("modal-close-btn").onclick = closeModal;
    const footer = m.querySelector("#modal-footer");
    actions.forEach(a => {
      const btn = document.createElement("button");
      btn.className = `btn ${a.class || "btn-ghost"}`;
      if (a.icon) btn.innerHTML = `<span class="btn-icon">${Icons.svg(a.icon, 15)}</span>`;
      btn.innerHTML += `<span>${a.label}</span>`;
      btn.onclick = () => { if (a.onClick) a.onClick(); if (!a.keepOpen) closeModal(); };
      footer.appendChild(btn);
    });
    overlay.hidden = false;
  }
  window.closeModal = () => { document.getElementById("modal-overlay").hidden = true; };
  document.getElementById("modal-overlay").addEventListener("click", e => {
    if (e.target.id === "modal-overlay") closeModal();
  });
  document.addEventListener("keydown", e => { if (e.key === "Escape") closeModal(); });

  function confirmDialog(message, onConfirm) {
    modal(t("common.confirm"), `<p>${message}</p>`, [
      { label: t("common.cancel"), class: "btn-ghost" },
      { label: t("common.delete"), class: "btn-danger", icon: "trash", onClick: onConfirm },
    ]);
  }

  // ── Navigation ──────────────────────────────────────────
  const NAV = [
    { id: "dashboard", icon: "dashboard" },
    { id: "services", icon: "services" },
    { id: "domains", icon: "domains" },
    { id: "ports", icon: "ports" },
    { id: "fakesite", icon: "fakesite" },
    { id: "reality", icon: "reality" },
    { id: "stats", icon: "stats" },
    { id: "logs", icon: "logs" },
    { id: "settings", icon: "settings" },
  ];

  function renderNav() {
    const nav = document.getElementById("sidebar-nav");
    nav.innerHTML = NAV.map(item => `
      <button class="nav-item ${state.currentPage === item.id ? "active" : ""}" data-page="${item.id}">
        ${Icons.svg(item.icon, 18)}
        <span>${t(`nav.${item.id}`)}</span>
      </button>`).join("");
    nav.querySelectorAll(".nav-item").forEach(b => {
      b.onclick = () => navigate(b.dataset.page);
    });
  }

  function navigate(page) {
    state.currentPage = page;
    renderNav();
    renderPage(page);
    document.getElementById("sidebar").classList.remove("open");
  }


  // Load a page module on demand.
  async function loadPageScript(name) {
    if (window.Pages && window.Pages[name]) return;
    const s = document.createElement("script");
    // Resolve relative to the current base (which includes /<panel-path>/)
    s.src = "static/js/pages/" + name + ".js";
    document.head.appendChild(s);
    await new Promise((res, rej) => { s.onload = res; s.onerror = rej; });
  }

  async function renderPage(page) {
    const content = document.getElementById("content");
    const titleEl = document.getElementById("page-title");
    titleEl.textContent = t(`nav.${page}`);
    const sub = document.getElementById("page-subtitle");
    if (sub) sub.textContent = "";
    content.innerHTML = `<div class="empty-state"><div class="loading-spinner"></div><p>${t("common.loading")}</p></div>`;
    try {
      await loadPageScript(page);
      const mod = window.Pages[page];
      if (mod) await mod.render(content, state, { api, t, toast, modal, confirmDialog, navigate, Icons });
      else content.innerHTML = `<div class="card"><p>Page not found</p></div>`;
    } catch (e) {
      console.error(e);
      content.innerHTML = `<div class="card"><p style="color:var(--danger);display:flex;gap:8px;align-items:center">${Icons.svg("warning", 18)} ${e.message}</p></div>`;
    }
  }

  // ── Populate selects ────────────────────────────────────
  function populateSelects() {
    const langSel = document.getElementById("lang-select");
    langSel.innerHTML = LANGUAGES.map(l => `<option value="${l.code}">${l.label}</option>`).join("");
    langSel.value = state.lang;
    const themeSel = document.getElementById("theme-select");
    themeSel.innerHTML = THEMES.map(th => `<option value="${th.id}">${th.label}</option>`).join("");
    themeSel.value = state.theme;
  }

  // ── Auth screens ────────────────────────────────────────
  function showLogin() {
    stopIdleTimer();
    document.getElementById("app-shell").hidden = true;
    document.getElementById("login-screen").hidden = false;
    state.authed = false;
  }
  function showApp() {
    document.getElementById("login-screen").hidden = true;
    document.getElementById("app-shell").hidden = false;
    state.authed = true;
    startIdleTimer();
  }

  // ── Inactivity auto-lock ─────────────────────────────────
  // Mirrors the server-side sliding window: after lockMinutes without
  // user interaction the panel locks itself and a fresh login is
  // required. lockMinutes = -1 disables the lock.
  function startIdleTimer() {
    stopIdleTimer();
    if (state.lockMinutes > 0) {
      idleTimer = setTimeout(lockPanel, state.lockMinutes * 60 * 1000);
    }
  }
  function stopIdleTimer() {
    if (idleTimer) { clearTimeout(idleTimer); idleTimer = null; }
  }
  function lockPanel() {
    if (!state.authed) return;
    showLogin();
    toast(t("login.locked"), "info");
  }
  function resetIdleTimer() {
    if (state.authed) startIdleTimer();
  }
  // Called from the settings page when the lock window changes, so the
  // idle timer starts using the new value immediately.
  window.ShahragSetLockMinutes = (mins) => {
    state.lockMinutes = (typeof mins === "number") ? mins : 0;
    startIdleTimer();
  };
  ["pointerdown", "keydown", "touchstart", "scroll"].forEach(ev => {
    document.addEventListener(ev, resetIdleTimer, { passive: true });
  });

  async function checkAuth() {
    try {
      const me = await api("/api/auth/me");
      state.lockMinutes = (typeof me.lock_minutes === "number") ? me.lock_minutes : 0;
      state.sessionTimeout = me.session_timeout_minutes || 60;
      showApp(); await initApp();
    }
    catch { showLogin(); }
  }

  // ── Init ────────────────────────────────────────────────
  function set(id, html) { const el = document.getElementById(id); if (el) el.innerHTML = html; }
  function injectStaticIcons() {
    set("login-brand", Icons.brandMark(64));
    set("sidebar-brand", Icons.brand(28));
    set("icon-user", Icons.svg("key", 18));
    set("icon-lock", Icons.svg("lock", 18));
    set("icon-login", Icons.svg("logout", 15));
    set("sidebar-toggle", Icons.svg("menu", 20));
    set("icon-generate", Icons.svg("refresh", 15));
    set("icon-globe", Icons.svg("globe", 14));
    set("icon-palette", Icons.svg("reality", 14));
    set("btn-logout", Icons.svg("logout", 18));
    set("lang-btn", Icons.svg("globe", 18));
    set("install-brand", Icons.brandMark(48));
    set("success-icon", Icons.svg("check", 56));
    const gl = document.getElementById("gen-label");
    if (gl) gl.textContent = t("dashboard.generate");
  }

  async function initApp() {
    try {
      state.config = await api("/api/panel/config");
      const ui = state.config.shahrag?.ui || {};
      state.theme = ui.theme || "midnight";
      state.lang = ui.language || "fa";
      state.panelPath = state.config.shahrag?.panel?.path || "";
      setTheme(state.theme);
      setLang(state.lang);
      injectStaticIcons();
      populateSelects();
      renderNav();
      navigate("dashboard");
      checkNginxStatus();
      setInterval(checkNginxStatus, 30000);
    } catch (e) { toast(e.message, "error"); }
  }

  async function checkNginxStatus() {
    try {
      // _poll=1: background heartbeat — must NOT count as user activity
      // for the inactivity lock.
      const s = await api("/api/settings/nginx?_poll=1");
      const dot = document.getElementById("nginx-status");
      const txt = document.getElementById("nginx-status-text");
      if (s.status?.active) { dot.classList.remove("offline"); txt.textContent = "nginx running"; }
      else { dot.classList.add("offline"); txt.textContent = "nginx stopped"; }
    } catch {}
  }

  // ── Events ──────────────────────────────────────────────
  document.getElementById("btn-generate").onclick = async () => {
    const btn = document.getElementById("btn-generate");
    btn.disabled = true;
    try {
      const r = await api("/api/settings/generate", { method: "POST" });
      if (r.ok) toast(t("dashboard.generated"), "success");
      else toast(t("dashboard.generate_failed"), "error");
    } catch (e) { toast(e.message, "error"); }
    btn.disabled = false;
  };
  document.getElementById("btn-logout").onclick = async () => {
    await api("/api/auth/logout", { method: "POST" }); showLogin();
  };
  const langBtn = document.getElementById("lang-btn");
  if (langBtn) {
    langBtn.onclick = () => {
      modal(t("settings.language"), "", LANGUAGES.map(l => ({
        label: l.label + (l.code === state.lang ? "  ✓" : ""),
        class: l.code === state.lang ? "btn-primary" : "btn-ghost",
        onClick: () => { setLang(l.code); window.closeModal(); },
      })));
    };
  }
  // Used by the Settings page to apply language/theme changes live.
  window.ShahragApplyUI = (lang, theme) => {
    if (lang && LANGUAGES.some(l => l.code === lang)) { state.lang = lang; }
    if (theme && THEMES.some(t => t.id === theme)) { state.theme = theme; }
    setTheme(state.theme);
    setLang(state.lang);
    populateSelects();
  };
  document.getElementById("sidebar-toggle").onclick = (e) => {
    e.stopPropagation();
    document.getElementById("sidebar").classList.toggle("open");
  };
  // Close the mobile sidebar when tapping anywhere outside it.
  document.addEventListener("click", (e) => {
    const sb = document.getElementById("sidebar");
    if (sb && sb.classList.contains("open")) {
      if (!sb.contains(e.target)) sb.classList.remove("open");
    }
  });
  document.getElementById("theme-select").onchange = e => setTheme(e.target.value);
  document.getElementById("lang-select").onchange = e => setLang(e.target.value);

  document.getElementById("login-form").onsubmit = async e => {
    e.preventDefault();
    const form = new FormData(e.target);
    const errEl = document.getElementById("login-error");
    const btn = document.getElementById("login-btn");
    btn.disabled = true; errEl.textContent = "";
    try {
      const r = await fetch("/api/auth/login", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: form.get("username"), password: form.get("password") }),
      });
      if (!r.ok) throw new Error("bad creds");
      showApp(); await initApp();
    } catch { errEl.textContent = t("login.error"); }
    btn.disabled = false;
  };

  // boot
  injectStaticIcons();
  populateSelects();
  document.documentElement.lang = state.lang;
  checkAuth();
})();
