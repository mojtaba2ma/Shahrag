/* The template gallery.

   What this page has to get right, and why each one is not obvious:

   It must render when the repository is unreachable. That is the NORMAL
   state on the network this panel is written for, not the exception. So the
   built-in templates are drawn from the same list as the downloadable ones,
   a blocked repository produces a calm line of text rather than an error
   toast, and the page never shows a spinner that can outlive the request.

   It must not make the BROWSER talk to jsDelivr. Thumbnails come through
   /api/templates/{id}/asset, which the server fetches. Linking directly
   would leak that a Shahrag panel is open to anyone watching the browser's
   traffic, and on a filtered link the images would simply never load while
   the server can still reach them.

   It must download exactly one template. The gallery is a JSON index; the
   archive only moves when the operator presses the button.

   A wrapping IIFE, like every other page module: these load through plain
   <script> tags, so a top-level const would become a global. */
window.Pages = window.Pages || {};

(function () {
"use strict";

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* Sizes are shown before downloading so someone on a metered or slow link
   can decide. Bytes are useless to a human; "1.2 MB" is not. */
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n <= 0) return "—";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}

/* Pick a localised string out of the repository's {en:..., fa:...} shape. */
function pick(obj, lang) {
  if (!obj) return "";
  if (typeof obj === "string") return obj;
  return obj[lang] || obj.en || Object.values(obj)[0] || "";
}

window.Pages.templates = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, confirmDialog, navigate } = ctx;
    const lang = (state && state.language) || document.documentElement.lang || "en";

    let data = null;
    let loadError = "";
    try {
      data = await api("/api/templates");
    } catch (e) {
      // Even a hard failure must leave a usable page: the built-ins are
      // still installed and still selectable from the real-site editor.
      loadError = e.message || String(e);
      data = { templates: [], categories: [] };
    }

    let kind = "site";
    let cat = "";
    let term = "";

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("fakesite", 20)} ${t("tpl.title")}</h1>
        <div class="page-actions">
          <button class="btn btn-ghost" id="tp-refresh">
            ${Icons.svg("refresh", 14)} ${t("tpl.refresh")}</button>
        </div>
      </div>

      <div class="card">
        <p class="muted">${t("tpl.lede")}</p>
        <div id="tp-repo-state" class="tp-state"></div>
      </div>

      <div class="card">
        <div class="tp-bar">
          <div class="tp-kinds" role="tablist">
            <button class="tp-kind active" data-kind="site">${t("tpl.kind_site")}</button>
            <button class="tp-kind" data-kind="error">${t("tpl.kind_error")}</button>
          </div>
          <label class="lv-search">
            ${Icons.svg("search", 14)}
            <input id="tp-search" placeholder="${t("list.search")}">
          </label>
        </div>
        <div class="tp-cats" id="tp-cats"></div>
        <div class="tp-grid" id="tp-grid"></div>
      </div>

      <div class="tp-drawer" id="tp-drawer" hidden>
        <div class="tp-drawer-back" data-close></div>
        <div class="tp-drawer-panel" role="dialog" aria-modal="true">
          <div class="tp-drawer-head">
            <h2 id="tp-d-title"></h2>
            <button class="btn-icon" data-close aria-label="${t("common.close")}">
              ${Icons.svg("close", 16)}</button>
          </div>
          <div class="tp-drawer-body" id="tp-d-body"></div>
        </div>
      </div>`;

    const grid = container.querySelector("#tp-grid");
    const catsEl = container.querySelector("#tp-cats");
    const stateEl = container.querySelector("#tp-repo-state");

    /* ── repository state line ──
       Three distinct situations that must read differently, because the
       action an operator should take is different in each:
         live      — nothing to say beyond where it came from;
         stale     — the list is real but old and the repo is unreachable;
         no list   — only the built-ins are available right now. */
    function drawState() {
      const repo = esc(data.repo || "");
      if (loadError || (data.error && !(data.templates || []).some(x => !x.local))) {
        const onlyBuiltins = (data.templates || []).every(x => x.local);
        stateEl.className = "tp-state tp-state-warn";
        stateEl.innerHTML = `
          ${Icons.svg("warning", 14)}
          <span>${t(onlyBuiltins ? "tpl.state_offline" : "tpl.state_error")}</span>
          <code>${repo}</code>
          <span class="muted tp-why">${esc(loadError || data.error || "")}</span>`;
        return;
      }
      if (data.stale) {
        const when = data.fetched_at ? new Date(data.fetched_at).toLocaleString() : "";
        stateEl.className = "tp-state tp-state-warn";
        stateEl.innerHTML = `${Icons.svg("clock", 14)}
          <span>${t("tpl.state_stale")}</span> <code>${esc(when)}</code>
          <span class="muted tp-why">${esc(data.error || "")}</span>`;
        return;
      }
      stateEl.className = "tp-state tp-state-ok";
      stateEl.innerHTML = `${Icons.svg("check", 14)}
        <span>${t("tpl.state_ok")}</span> <code>${repo}</code>`;
    }

    function visible() {
      const q = term.trim().toLowerCase();
      return (data.templates || []).filter(x => {
        const k = x.kind || "site";
        if (k !== kind) return false;
        if (cat && (x.category || "") !== cat) return false;
        if (!q) return true;
        return (x.id + " " + pick(x.name, lang) + " " + pick(x.description, lang))
          .toLowerCase().includes(q);
      });
    }

    function drawCats() {
      const present = new Set((data.templates || [])
        .filter(x => (x.kind || "site") === kind)
        .map(x => x.category || ""));
      const list = (data.categories || []).filter(c =>
        (c.kind || "site") === kind && present.has(c.id));
      catsEl.innerHTML = `
        <button class="tp-cat ${cat === "" ? "active" : ""}" data-cat="">${t("list.all")}</button>` +
        list.map(c => `<button class="tp-cat ${cat === c.id ? "active" : ""}"
          data-cat="${esc(c.id)}">${esc(pick(c.name, lang))}</button>`).join("");
      catsEl.querySelectorAll("[data-cat]").forEach(b => {
        b.onclick = () => { cat = b.dataset.cat; drawCats(); drawGrid(); };
      });
    }

    function drawGrid() {
      const rows = visible();
      if (!rows.length) {
        grid.innerHTML = `<p class="muted tp-empty">${t("tpl.empty")}</p>`;
        return;
      }
      grid.innerHTML = rows.map(x => {
        const name = esc(pick(x.name, lang) || x.id);
        const desc = esc(pick(x.description, lang));
        const size = x.installed ? fmtBytes(x.on_disk || x.size) : fmtBytes(x.size);
        const badge = x.builtin
          ? `<span class="tp-badge tp-badge-builtin">${t("tpl.builtin")}</span>`
          : (x.installed
            ? `<span class="tp-badge tp-badge-in">${t("tpl.installed")}</span>`
            : `<span class="tp-badge">${t("tpl.remote")}</span>`);
        return `
          <article class="tp-card" data-id="${esc(x.id)}">
            <div class="tp-thumb">
              <img loading="lazy" alt="" src="api/templates/${encodeURIComponent(x.id)}/asset${
                x.thumb && !x.installed ? "?f=" + encodeURIComponent(x.thumb) : ""}">
            </div>
            <div class="tp-body">
              <div class="tp-name">${name} ${badge}</div>
              <p class="tp-desc">${desc}</p>
              <div class="tp-meta">
                <span>${Icons.svg("copy", 12)} ${esc(size)}</span>
                ${x.rtl ? `<span class="tp-rtl">RTL</span>` : ""}
              </div>
            </div>
            <div class="tp-actions">
              <button class="btn btn-ghost btn-sm" data-detail="${esc(x.id)}">
                ${t("tpl.details")}</button>
              ${x.installed
                ? (x.builtin ? "" : `<button class="btn btn-ghost btn-sm danger"
                      data-remove="${esc(x.id)}">${Icons.svg("trash", 12)}</button>`)
                : `<button class="btn btn-primary btn-sm" data-install="${esc(x.id)}">
                      ${Icons.svg("download", 12)} ${t("tpl.add")}</button>`}
            </div>
          </article>`;
      }).join("");

      grid.querySelectorAll("[data-detail]").forEach(b => {
        b.onclick = () => openDetail(b.dataset.detail);
      });
      grid.querySelectorAll("[data-install]").forEach(b => {
        b.onclick = () => install(b.dataset.install, b);
      });
      grid.querySelectorAll("[data-remove]").forEach(b => {
        b.onclick = () => remove(b.dataset.remove);
      });
      // A thumbnail that fails leaves a broken-image icon, which looks like
      // the panel is broken. The endpoint draws a placeholder rather than
      // 404ing, so this is only a last resort.
      grid.querySelectorAll(".tp-thumb img").forEach(img => {
        img.onerror = () => { img.closest(".tp-thumb").classList.add("tp-thumb-none"); };
      });
    }

    async function install(id, btn) {
      const original = btn.innerHTML;
      btn.disabled = true;
      btn.innerHTML = `<span class="spinner-sm"></span> ${t("tpl.adding")}`;
      try {
        const res = await api(`/api/templates/${encodeURIComponent(id)}/install`,
          { method: "POST" });
        toast(t("tpl.added") + " · " + fmtBytes(res.bytes || 0), "success");
        await reload();
      } catch (e) {
        toast(e.message, "error");
        btn.disabled = false;
        btn.innerHTML = original;
      }
    }

    async function remove(id) {
      const ok = await confirmDialog({
        title: t("tpl.remove_title"),
        body: t("tpl.remove_body").replace("{id}", id),
        confirm: t("common.delete"), danger: true,
      });
      if (!ok) return;
      try {
        await api(`/api/templates/${encodeURIComponent(id)}`, { method: "DELETE" });
        toast(t("tpl.removed"), "success");
        await reload();
      } catch (e) { toast(e.message, "error"); }
    }

    /* ── details drawer ──
       A live preview of the actual page, not a screenshot: the template is
       already on this server for an installed one, so showing the real thing
       costs one request and cannot be out of date. Sandboxed, because it is
       arbitrary HTML from a repository. */
    const drawer = container.querySelector("#tp-drawer");
    async function openDetail(id) {
      const title = container.querySelector("#tp-d-title");
      const body = container.querySelector("#tp-d-body");
      title.textContent = id;
      body.innerHTML = `<div class="tp-loading"><span class="spinner-sm"></span></div>`;
      drawer.hidden = false;
      let d;
      try {
        d = await api(`/api/templates/${encodeURIComponent(id)}`);
      } catch (e) {
        body.innerHTML = `<p class="tp-err">${esc(e.message)}</p>`;
        return;
      }
      const m = d.meta || {};
      title.textContent = pick(m.name, lang) || id;
      const rows = [
        [t("tpl.d_id"), id],
        [t("tpl.d_version"), m.version || "—"],
        [t("tpl.d_author"), m.author || "—"],
        [t("tpl.d_license"), m.license || "—"],
        [t("tpl.d_size"), fmtBytes(d.on_disk || m.size)],
        [t("tpl.d_pages"), (m.pages || []).length || "—"],
        [t("tpl.d_errors"), (m.error_pages || []).join(" ") || "—"],
      ];
      body.innerHTML = `
        <p class="tp-desc">${esc(pick(m.description, lang))}</p>
        <table class="tp-table">
          ${rows.map(r => `<tr><th>${esc(r[0])}</th><td>${esc(r[1])}</td></tr>`).join("")}
        </table>
        ${d.installed ? `
          <h3 class="tp-sub">${t("tpl.d_preview")}</h3>
          <div class="tp-preview">
            <iframe sandbox="" loading="lazy" title="${esc(id)}"
              src="api/templates/${encodeURIComponent(id)}/preview"></iframe>
          </div>
          ${(d.files || []).length ? `
            <h3 class="tp-sub">${t("tpl.d_files")}</h3>
            <ul class="tp-files">${d.files.map(f => `<li>${esc(f)}</li>`).join("")}</ul>` : ""}
        ` : `
          <p class="muted">${t("tpl.d_not_installed")}</p>
          <button class="btn btn-primary" data-install-d="${esc(id)}">
            ${Icons.svg("download", 14)} ${t("tpl.add")}</button>`}`;
      const ib = body.querySelector("[data-install-d]");
      if (ib) ib.onclick = () => install(id, ib).then(() => { drawer.hidden = true; });
    }
    drawer.querySelectorAll("[data-close]").forEach(x => {
      x.onclick = () => { drawer.hidden = true; };
    });

    async function reload(force) {
      try {
        data = await api("/api/templates" + (force ? "?refresh=1" : ""));
        loadError = "";
      } catch (e) {
        loadError = e.message || String(e);
      }
      drawState(); drawCats(); drawGrid();
    }

    container.querySelectorAll(".tp-kind").forEach(b => {
      b.onclick = () => {
        kind = b.dataset.kind;
        cat = "";
        container.querySelectorAll(".tp-kind").forEach(x =>
          x.classList.toggle("active", x === b));
        drawCats(); drawGrid();
      };
    });
    const search = container.querySelector("#tp-search");
    search.oninput = () => { term = search.value; drawGrid(); };
    container.querySelector("#tp-refresh").onclick = async (e) => {
      const b = e.currentTarget;
      b.disabled = true;
      await reload(true);
      b.disabled = false;
      toast(t("tpl.refreshed"), "success");
    };

    drawState();
    drawCats();
    drawGrid();
  }
};

})();
