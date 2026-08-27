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

  // t(key) — look the key up in the active language, then fall back to
  // English, then to the key itself.
  //
  // The English fallback matters now that explanations live in tooltips: the
  // smaller dictionaries do not carry every hint key, and without a fallback
  // the bubble showed the raw dotted path ("settings.lock_minutes_hint")
  // instead of a sentence. A readable English line beats a debug string.
  function lookup(dict, parts) {
    let val = dict;
    for (const p of parts) {
      if (val && typeof val === "object") val = val[p];
      else return undefined;
    }
    return typeof val === "string" ? val : undefined;
  }

  function t(key) {
    const parts = key.split(".");
    return lookup(window.I18N[state.lang], parts)
      || lookup(window.I18N.en, parts)
      || key;
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
    fetch(apiURL("/api/settings/ui"), {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ theme: state.theme, language: state.lang }),
    }).catch(() => {});
  }

  // Absolute URL for an API path, always through the panel base path
  // (/<panel-path>/). Never rely on the patched window.fetch alone: if the
  // bootstrap in index.html did not run (cached old page, script order),
  // an unprefixed /api/... call is answered by the FAKE SITE with 200 HTML,
  // res.json() throws and the SPA thinks the session died. That was the
  // real cause of "I have to log in again after every refresh".
  function apiURL(path) {
    const base = window.SHAHRAG_BASE || "/";
    if (base !== "/" && path.indexOf("/api/") === 0) return base + path.slice(1);
    return path;
  }

  async function api(path, opts = {}) {
    const res = await fetch(apiURL(path), {
      headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
      credentials: "same-origin", ...opts,
    });
    // A non-JSON 2xx answer means the request did NOT reach the panel
    // (it hit nginx's fake site or another service). Treat it as a
    // routing error — never as a logout.
    const ctype = res.headers.get("content-type") || "";
    if (res.ok && ctype.indexOf("json") === -1) {
      throw new Error("routing error: " + path + " did not reach the panel (got " + (ctype || "no content-type") + ")");
    }
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

  // ── Hover help (tooltips) ───────────────────────────────
  //
  // Explanations used to sit permanently under the fields as grey paragraphs.
  // They made every form long and noisy, so they moved behind a small "?"
  // icon (Icons.help). The bubble is appended to <body>, not next to the
  // icon: inside `.modal-body` (overflow-y:auto) it would be clipped by the
  // scroll box, and inside a table cell it would be clipped by `.table-wrap`.
  // One delegated listener covers every icon, including ones rendered later.
  let tipEl = null, tipAnchor = null;
  function hideTip() {
    if (tipEl) { tipEl.remove(); tipEl = null; }
    tipAnchor = null;
  }

  function showTip(btn) {
    hideTip();
    const text = btn.getAttribute("data-tip");
    if (!text) return;
    tipAnchor = btn;
    tipEl = document.createElement("div");
    tipEl.className = "tip-bubble";
    tipEl.textContent = text;
    tipEl.dir = document.documentElement.dir || "rtl";
    document.body.appendChild(tipEl);
    placeTip(btn);
    tipEl.classList.add("visible");
  }

  function placeTip(btn) {
    if (!tipEl) return;
    const r = btn.getBoundingClientRect();
    const tw = tipEl.offsetWidth, th = tipEl.offsetHeight;
    const pad = 8;
    // Prefer above the icon; flip below when there is no room up there.
    let top = r.top - th - 8;
    if (top < pad) { top = r.bottom + 8; tipEl.classList.add("below"); }
    // Keep the bubble fully on screen horizontally at any viewport width.
    let left = r.left + r.width / 2 - tw / 2;
    left = Math.max(pad, Math.min(left, window.innerWidth - tw - pad));
    top = Math.max(pad, Math.min(top, window.innerHeight - th - pad));
    tipEl.style.top = `${top}px`;
    tipEl.style.left = `${left}px`;
  }

  document.addEventListener("mouseover", e => {
    const btn = e.target.closest && e.target.closest(".help-tip");
    if (btn) showTip(btn);
  });
  document.addEventListener("mouseout", e => {
    const btn = e.target.closest && e.target.closest(".help-tip");
    if (btn) hideTip();
  });
  document.addEventListener("focusin", e => {
    const btn = e.target.closest && e.target.closest(".help-tip");
    if (btn) showTip(btn);
  });
  document.addEventListener("focusout", hideTip);
  // Touch devices have no hover: a tap toggles the bubble instead. The click
  // must not submit anything, hence type="button" on the icon.
  document.addEventListener("click", e => {
    const btn = e.target.closest && e.target.closest(".help-tip");
    if (btn) { e.preventDefault(); e.stopPropagation(); tipEl ? hideTip() : showTip(btn); }
    else hideTip();
  }, true);
  // Scrolling must not destroy a bubble that is still being pointed at.
  // The bubble is position:fixed while the icon moves with the page, so it
  // is re-anchored on every scroll and only dropped once the icon itself has
  // left the viewport. (Hiding outright looked like the tooltip "did not
  // work" whenever the browser scrolled the field into view first — exactly
  // what happens with keyboard focus and on short screens.)
  function repositionTip() {
    if (!tipEl || !tipAnchor) return;
    if (!tipAnchor.isConnected) { hideTip(); return; }
    const r = tipAnchor.getBoundingClientRect();
    if (r.bottom < 0 || r.top > window.innerHeight) { hideTip(); return; }
    tipEl.classList.remove("below");
    placeTip(tipAnchor);
  }
  window.addEventListener("scroll", repositionTip, true);
  window.addEventListener("resize", repositionTip);

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
    // Lock the page behind the dialog. Without this, scrolling inside the
    // modal scrolls the panel underneath as soon as the modal's own content
    // reaches its end, so the page shifts around while you fill in a form.
    // The scroll position is restored on close.
    lockBodyScroll();
  }

  let scrollLockY = 0;
  function lockBodyScroll() {
    if (document.body.classList.contains("modal-open")) return;
    scrollLockY = window.scrollY || document.documentElement.scrollTop || 0;
    document.body.classList.add("modal-open");
    // position:fixed is what actually stops iOS Safari, which ignores
    // overflow:hidden on <body>.
    document.body.style.top = `-${scrollLockY}px`;
  }
  function unlockBodyScroll() {
    if (!document.body.classList.contains("modal-open")) return;
    document.body.classList.remove("modal-open");
    document.body.style.top = "";
    window.scrollTo(0, scrollLockY);
  }

  window.closeModal = () => {
    document.getElementById("modal-overlay").hidden = true;
    hideTip();          // a bubble lives on <body>, so it outlives the modal
    unlockBodyScroll();
  };
  document.getElementById("modal-overlay").addEventListener("click", e => {
    if (e.target.id === "modal-overlay") closeModal();
  });
  document.addEventListener("keydown", e => { if (e.key === "Escape") closeModal(); });

  // confirmDialog(message, onConfirm, opts)
  //
  // The confirm button used to always say "Delete" with a red bin, which is
  // wrong (and alarming) for a save or a restore. `opts` lets the caller name
  // the action; it still defaults to the destructive styling, because most
  // confirmations here really are deletions.
  function confirmDialog(message, onConfirm, opts = {}) {
    const label = opts.label || t("common.delete");
    const cls = opts.danger === false ? "btn-primary" : "btn-danger";
    const icon = opts.icon || (opts.danger === false ? "check" : "trash");
    modal(t("common.confirm"), `<p>${message}</p>`, [
      { label: t("common.cancel"), class: "btn-ghost" },
      { label, class: cls, icon, onClick: onConfirm },
    ]);
  }

  // ── Navigation ──────────────────────────────────────────
  const NAV = [
    { id: "dashboard", icon: "dashboard" },
    { id: "services", icon: "services" },
    { id: "domains", icon: "domains" },
    { id: "ports", icon: "ports" },
    { id: "fakesite", icon: "fakesite" },
    { id: "stats", icon: "stats" },
    { id: "logs", icon: "logs" },
    { id: "files", icon: "copy" },
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
    // Resolve relative to the current base (which includes /<panel-path>/).
    // The version query string is what guarantees a NEW build never reuses a
    // cached page module: the URL itself changes.
    const v = window.SHAHRAG_VERSION_TAG || window.SHAHRAG_VERSION || "";
    s.src = "static/js/pages/" + name + ".js" + (v ? "?v=" + encodeURIComponent(v) : "");
    document.head.appendChild(s);
    await new Promise((res, rej) => { s.onload = res; s.onerror = rej; });
    // A script can load successfully and still fail to register its page —
    // e.g. a duplicate top-level `const` aborts the file, which the browser
    // reports only in the console. Surfacing it here turns a silent
    // "clicking the menu does nothing" into a real error message.
    if (!(window.Pages && window.Pages[name])) {
      throw new Error(`Page "${name}" failed to load (check the browser console).`);
    }
  }

  // Every render gets a ticket. A page's render() is async (it awaits its
  // API calls), so a SLOW page that the user has already navigated away from
  // would finish later and paint its markup over the page now on screen —
  // the new page looked like it simply never opened. Only the newest ticket
  // is allowed to touch the DOM.
  let renderTicket = 0;

  async function renderPage(page) {
    const ticket = ++renderTicket;
    const content = document.getElementById("content");
    const titleEl = document.getElementById("page-title");
    titleEl.textContent = t(`nav.${page}`);
    const sub = document.getElementById("page-subtitle");
    if (sub) sub.textContent = "";

    // Let the outgoing page stop its timers/listeners (the stats page polls).
    if (typeof content._shahragCleanup === "function") {
      try { content._shahragCleanup(); } catch (_) {}
      content._shahragCleanup = null;
    }

    content.innerHTML = `<div class="empty-state"><div class="loading-spinner"></div><p>${t("common.loading")}</p></div>`;
    try {
      await loadPageScript(page);
      if (ticket !== renderTicket) return;   // superseded while loading
      const mod = window.Pages[page];
      if (mod) {
        await mod.render(content, state, { api, t, toast, modal, confirmDialog, navigate, Icons });
        if (ticket !== renderTicket) return; // superseded while rendering
      } else {
        content.innerHTML = `<div class="card"><p>Page not found</p></div>`;
      }
    } catch (e) {
      if (ticket !== renderTicket) return;
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

  // checkAuth runs on every page load (including refresh). It must ONLY
  // show the login screen when the server really says 401. A transient
  // network hiccup or a routing problem previously landed here and looked
  // exactly like a logout.
  async function checkAuth(retry = 0) {
    let res;
    try {
      res = await fetch(apiURL("/api/auth/me"), { credentials: "same-origin" });
    } catch (e) {
      // Network error (server restarting, mobile network switch…): retry a
      // couple of times before giving up, and never wipe the session.
      if (retry < 2) { setTimeout(() => checkAuth(retry + 1), 700); return; }
      showLogin();
      return;
    }
    if (res.status === 401) { showLogin(); return; }
    let me = null;
    try { me = await res.json(); } catch (_) {}
    if (!me || !me.authenticated) {
      if (retry < 2) { setTimeout(() => checkAuth(retry + 1), 700); return; }
      showLogin();
      return;
    }
    state.lockMinutes = (typeof me.lock_minutes === "number") ? me.lock_minutes : 0;
    state.sessionTimeout = me.session_timeout_minutes || 60;
    showApp();
    await initApp();
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
      const r = await fetch(apiURL("/api/auth/login"), {
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
