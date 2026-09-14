/* The real site, per domain.

   The shape of this page follows the shape of the decision an operator is
   actually making, which is not "configure a website" but "which of my
   domains should look like an ordinary website, and what should each one
   look like".

   So the landing view is a LIST OF DOMAINS with a switch on each, not a
   settings form. The settings form is behind each row, because most visits
   are a switch flip and nothing else.

   Three things this page must never get wrong:

   Off means off. Switching a domain off writes mode="off", not a cleared
   Enabled flag — otherwise turning on the panel-wide default would switch
   it back on behind the operator's back, and the domain they turned off is
   usually the one hosting the panel.

   It must say when a service already owns "/". A service on the root path
   wins, and the real site is then only reachable under its own paths. That
   is surprising enough that the row says so rather than leaving the operator
   to discover it.

   Nothing can be enabled with no template. A site with no template renders
   nothing and silently falls back to the old fake page, which looks exactly
   like the feature not working.

   A wrapping IIFE, like every other page module. */
window.Pages = window.Pages || {};

(function () {
"use strict";

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function fmtBytes(n) {
  n = Number(n) || 0;
  if (n <= 0) return "—";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}

function pick(obj, lang) {
  if (!obj) return "";
  if (typeof obj === "string") return obj;
  return obj[lang] || obj.en || Object.values(obj)[0] || "";
}

window.Pages.realsite = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, confirmDialog, navigate } = ctx;
    const lang = (state && state.language) || document.documentElement.lang || "en";

    let data, tpls;
    try {
      [data, tpls] = await Promise.all([
        api("/api/realsite"),
        api("/api/templates").catch(() => ({ templates: [] })),
      ]);
    } catch (e) {
      container.innerHTML = `<div class="card"><p class="tp-err">${esc(e.message)}</p></div>`;
      return;
    }

    // Only INSTALLED templates may be chosen: picking a remote one would
    // save a name the renderer cannot resolve, and the domain would quietly
    // fall back to the fake page.
    const installed = (tpls.templates || []).filter(x => x.installed);
    const siteTpls = installed.filter(x => (x.kind || "site") === "site");
    const errTpls = installed.filter(x => (x.kind || "site") === "error");

    function tplOptions(list, selected, emptyLabel) {
      return `<option value="">${esc(emptyLabel)}</option>` + list.map(x =>
        `<option value="${esc(x.id)}" ${x.id === selected ? "selected" : ""}>${
          esc(pick(x.name, lang) || x.id)}</option>`).join("");
    }

    const d = data.settings || {};
    const defs = d.defaults || {};

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("globe", 20)} ${t("rs.title")}</h1>
        <div class="page-actions">
          <button class="btn btn-ghost" id="rs-gallery">
            ${Icons.svg("fakesite", 14)} ${t("rs.gallery")}</button>
        </div>
      </div>

      <div class="card">
        <p class="muted">${t("rs.lede")}</p>
        ${(data.warnings || []).length ? `<div class="rs-warns">${
          data.warnings.map(w => `<div class="rs-warn">${Icons.svg("warning", 13)} ${esc(w)}</div>`).join("")
        }</div>` : ""}
      </div>

      <div class="card">
        <h2 class="card-title">${t("rs.domains")}</h2>
        <div id="rs-list"></div>
      </div>

      <div class="card">
        <h2 class="card-title">${t("rs.defaults")}${Icons.help(t("rs.defaults_help"))}</h2>
        <label class="switch">
          <input type="checkbox" id="rs-def-on" ${defs.enabled ? "checked" : ""}>
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("rs.defaults_enable")}</span>
        </label>
        <div id="rs-def-body" ${defs.enabled ? "" : "hidden"}>
          <div class="field-row">
            <div class="field"><label>${t("rs.template")}</label>
              <select id="rs-def-tpl">${tplOptions(siteTpls, defs.template, t("rs.no_template"))}</select></div>
            <div class="field"><label>${t("rs.language")}</label>
              <input id="rs-def-lang" value="${esc((defs.content || {}).language || "")}" placeholder="fa"></div>
          </div>
          <div class="field-row">
            <div class="field"><label>${t("rs.site_name")}</label>
              <input id="rs-def-name" value="${esc((defs.content || {}).site_name || "")}"></div>
            <div class="field"><label>${t("rs.tagline")}</label>
              <input id="rs-def-tag" value="${esc((defs.content || {}).tagline || "")}"></div>
          </div>
          <div class="field-row">
            <div class="field"><label>${t("rs.email")}</label>
              <input id="rs-def-email" value="${esc((defs.content || {}).email || "")}"></div>
            <div class="field"><label>${t("rs.phone")}</label>
              <input id="rs-def-phone" value="${esc((defs.content || {}).phone || "")}"></div>
          </div>
          <div class="field field-wide"><label>${t("rs.address")}</label>
            <input id="rs-def-addr" value="${esc((defs.content || {}).address || "")}"></div>
          <label class="check">
            <input type="checkbox" id="rs-def-noindex" ${(defs.content || {}).noindex ? "checked" : ""}>
            <span class="check-box"></span>
            <span>${t("rs.noindex")}${Icons.help(t("rs.noindex_help"))}</span>
          </label>
        </div>
      </div>

      <div class="card">
        <h2 class="card-title">${t("rs.repo")}${Icons.help(t("rs.repo_help"))}</h2>
        <div class="field-row">
          <div class="field"><label>${t("rs.repo_addr")}</label>
            <input id="rs-repo" value="${esc(d.repo || "")}" placeholder="${esc(data.default_repo || "")}"></div>
          <div class="field"><label>${t("rs.repo_ref")}</label>
            <input id="rs-ref" value="${esc(d.ref || "")}" placeholder="main"></div>
        </div>
        <div class="field-row">
          <div class="field"><label>${t("rs.mirror")}${Icons.help(t("rs.mirror_help"))}</label>
            <input id="rs-mirror" value="${esc(d.mirror || "")}" placeholder="https://"></div>
          <div class="field"><label>${t("rs.cache_hours")}</label>
            <input id="rs-cache" type="number" min="0" max="720" value="${Number(d.cache_hours) || 0}"></div>
        </div>
        <div class="field field-wide"><label>${t("rs.sites_dir")}${Icons.help(t("rs.sites_dir_help"))}</label>
          <input id="rs-dir" value="${esc(d.sites_dir || "")}" placeholder="${esc(data.sites_dir || "")}"></div>
      </div>

      <div class="form-actions">
        <button class="btn btn-primary" id="rs-save">${Icons.svg("check", 14)} ${t("common.save")}</button>
      </div>

      <div class="tp-drawer" id="rs-drawer" hidden>
        <div class="tp-drawer-back" data-close></div>
        <div class="tp-drawer-panel" role="dialog" aria-modal="true">
          <div class="tp-drawer-head">
            <h2 id="rs-d-title"></h2>
            <button class="btn-icon" data-close aria-label="${t("common.close")}">
              ${Icons.svg("close", 16)}</button>
          </div>
          <div class="tp-drawer-body" id="rs-d-body"></div>
        </div>
      </div>`;

    /* ── the domain list ── */
    function drawList() {
      const rows = data.domains || [];
      window.ListView.create(container.querySelector("#rs-list"), {
        id: "realsite-domains",
        rows,
        t, Icons,
        rowKey: r => r.domain,
        empty: t("rs.no_domains"),
        columns: [
          { key: "domain", label: t("rs.col_domain"), sortable: true,
            plain: r => r.domain,
            render: r => `<span class="mono">${esc(r.domain)}</span>` +
              (r.has_cert ? "" : ` <span class="tag tag-warn">${t("rs.no_cert")}</span>`) },
          { key: "state", label: t("rs.col_state"), sortable: true,
            plain: r => (r.active ? "on" : "off"),
            render: r => r.active
              ? `<span class="tag tag-ok">${t("rs.on")}</span>`
              : `<span class="tag">${t("rs.off")}</span>` },
          { key: "mode", label: t("rs.col_mode"), sortable: true,
            plain: r => (r.site && r.site.mode) || "inherit",
            render: r => {
              const m = (r.site && r.site.mode) || "inherit";
              return `<span class="tiny">${esc(t("rs.mode_" + m))}</span>`;
            } },
          { key: "tpl", label: t("rs.col_template"), sortable: true,
            plain: r => r.effective_template || "",
            render: r => r.effective_template
              ? `<span class="mono tiny">${esc(r.effective_template)}</span>`
              : `<span class="muted">—</span>` },
          { key: "note", label: t("rs.col_note"),
            plain: r => r.root_taken_by || "",
            render: r => r.root_taken_by
              ? `<span class="tag tag-warn" title="${t("rs.root_taken_help")}">${
                  t("rs.root_taken")}: ${esc(r.root_taken_by)}</span>`
              : "" },
          { key: "act", label: "",
            render: r => `
              <div class="row-actions">
                <button class="btn-icon" data-toggle="${esc(r.domain)}"
                  title="${r.active ? t("rs.turn_off") : t("rs.turn_on")}">
                  ${Icons.svg(r.active ? "close" : "check", 14)}</button>
                <button class="btn-icon" data-edit="${esc(r.domain)}" title="${t("common.edit")}">
                  ${Icons.svg("edit", 14)}</button>
                ${r.active ? `<button class="btn-icon" data-files="${esc(r.domain)}"
                  title="${t("rs.files")}">${Icons.svg("copy", 14)}</button>` : ""}
              </div>` },
        ],
      });
      const host = container.querySelector("#rs-list");
      host.querySelectorAll("[data-toggle]").forEach(b => {
        b.onclick = () => toggle(b.dataset.toggle);
      });
      host.querySelectorAll("[data-edit]").forEach(b => {
        b.onclick = () => openEditor(b.dataset.edit);
      });
      host.querySelectorAll("[data-files]").forEach(b => {
        b.onclick = () => openFiles(b.dataset.files);
      });
    }

    async function toggle(domain) {
      const row = (data.domains || []).find(x => x.domain === domain);
      // Turning a site ON changes what the whole internet sees at a live
      // address, so it is confirmed. Turning it off is not: reverting to
      // the previous page is never the dangerous direction.
      // confirmDialog in this codebase is callback-style — (message, onConfirm,
      // opts) — and returns nothing. Treating it as a promise silently never
      // resolves, which leaves the modal open forever and makes the rest of
      // the page unclickable. Found by a browser test that then could not
      // reach the next tab.
      const doToggle = async () => {
        try {
          const res = await api(`/api/realsite/domains/${encodeURIComponent(domain)}/toggle`,
            { method: "POST" });
          toast(res.enabled ? t("rs.turned_on") : t("rs.turned_off"), "success");
          if (res.apply_error) toast(res.apply_error, "error");
          await refresh();
        } catch (e) { toast(e.message, "error"); }
      };
      if (row && !row.active) {
        confirmDialog(t("rs.confirm_on_body").replace("{d}", domain), doToggle,
          { label: t("rs.turn_on"), danger: false, icon: "check" });
        return;
      }
      await doToggle();
    }

    /* ── the per-domain editor ── */
    const drawer = container.querySelector("#rs-drawer");
    drawer.querySelectorAll("[data-close]").forEach(x => {
      x.onclick = () => { drawer.hidden = true; };
    });

    async function openEditor(domain) {
      const title = container.querySelector("#rs-d-title");
      const body = container.querySelector("#rs-d-body");
      title.textContent = domain;
      body.innerHTML = `<div class="tp-loading"><span class="spinner-sm"></span></div>`;
      drawer.hidden = false;

      let one;
      try {
        one = await api(`/api/realsite/domains/${encodeURIComponent(domain)}`);
      } catch (e) {
        body.innerHTML = `<p class="tp-err">${esc(e.message)}</p>`;
        return;
      }
      const s = one.site || {};
      const c = s.content || {};
      const mode = s.mode || "inherit";
      const emode = s.error_mode || "template";
      const codes = one.codes || [];
      const eps = s.error_pages || {};

      body.innerHTML = `
        <label class="switch">
          <input type="checkbox" id="rd-on" ${s.enabled ? "checked" : ""}>
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("rs.enable_for")} ${esc(domain)}</span>
        </label>

        <div class="field field-wide"><label>${t("rs.mode")}${Icons.help(t("rs.mode_help"))}</label>
          <select id="rd-mode">
            <option value="inherit" ${mode === "inherit" ? "selected" : ""}>${t("rs.mode_inherit")}</option>
            <option value="custom" ${mode === "custom" ? "selected" : ""}>${t("rs.mode_custom")}</option>
            <option value="off" ${mode === "off" ? "selected" : ""}>${t("rs.mode_off")}</option>
          </select></div>

        <div id="rd-custom" ${mode === "custom" ? "" : "hidden"}>
          <div class="field-row">
            <div class="field"><label>${t("rs.template")}</label>
              <select id="rd-tpl">${tplOptions(siteTpls, s.template, t("rs.inherit_value"))}</select></div>
            <div class="field"><label>${t("rs.language")}</label>
              <input id="rd-lang" value="${esc(c.language || "")}" placeholder="fa"></div>
          </div>
          <div class="field-row">
            <div class="field"><label>${t("rs.site_name")}</label>
              <input id="rd-name" value="${esc(c.site_name || "")}"></div>
            <div class="field"><label>${t("rs.tagline")}</label>
              <input id="rd-tag" value="${esc(c.tagline || "")}"></div>
          </div>
          <div class="field field-wide"><label>${t("rs.hero_title")}</label>
            <input id="rd-hero" value="${esc(c.hero_title || "")}"></div>
          <div class="field field-wide"><label>${t("rs.hero_text")}</label>
            <textarea id="rd-herotext" rows="3">${esc(c.hero_text || "")}</textarea></div>
          <div class="field-row">
            <div class="field"><label>${t("rs.email")}</label>
              <input id="rd-email" value="${esc(c.email || "")}"></div>
            <div class="field"><label>${t("rs.phone")}</label>
              <input id="rd-phone" value="${esc(c.phone || "")}"></div>
          </div>
          <div class="field field-wide"><label>${t("rs.address")}</label>
            <input id="rd-addr" value="${esc(c.address || "")}"></div>
          <label class="check">
            <input type="checkbox" id="rd-noindex" ${c.noindex ? "checked" : ""}>
            <span class="check-box"></span><span>${t("rs.noindex")}</span></label>

          <h3 class="tp-sub">${t("rs.errors")}${Icons.help(t("rs.errors_help"))}</h3>
          <div class="field-row">
            <div class="field"><label>${t("rs.error_mode")}</label>
              <select id="rd-emode">
                <option value="template" ${emode === "template" ? "selected" : ""}>${t("rs.emode_template")}</option>
                <option value="custom" ${emode === "custom" ? "selected" : ""}>${t("rs.emode_custom")}</option>
                <option value="off" ${emode === "off" ? "selected" : ""}>${t("rs.emode_off")}</option>
              </select></div>
            <div class="field"><label>${t("rs.error_template")}</label>
              <select id="rd-etpl">${tplOptions(errTpls, s.error_template, t("rs.same_as_site"))}</select></div>
          </div>
          <div class="rs-codes" id="rd-codes">
            ${codes.map(code => {
              const ep = eps[code] || {};
              return `<div class="rs-code">
                <label class="check">
                  <input type="checkbox" data-code-on="${code}" ${ep.disabled ? "" : "checked"}>
                  <span class="check-box"></span><span class="mono">${code}</span></label>
                <button class="btn-icon" data-code-html="${code}"
                  title="${t("rs.custom_html")}">${Icons.svg("edit", 13)}</button>
                ${ep.html ? `<span class="tag tag-ok tiny">${t("rs.has_html")}</span>` : ""}
              </div>`;
            }).join("")}
          </div>

          <h3 class="tp-sub">${t("rs.advanced")}</h3>
          <div class="field field-wide"><label>${t("rs.own_root")}${Icons.help(t("rs.own_root_help"))}</label>
            <input id="rd-root" value="${esc(s.root || "")}" placeholder="/var/www/mysite"></div>
          <label class="check">
            <input type="checkbox" id="rd-cache" ${s.cache_assets ? "checked" : ""}>
            <span class="check-box"></span>
            <span>${t("rs.cache_assets")}${Icons.help(t("rs.cache_assets_help"))}</span></label>
          <div class="field field-wide"><label>${t("rs.extra")}${Icons.help(t("rs.extra_help"))}</label>
            <textarea id="rd-extra" rows="4" class="mono">${esc(s.extra_config || "")}</textarea></div>
        </div>

        <div class="form-actions">
          <button class="btn btn-primary" id="rd-save">${Icons.svg("check", 14)} ${t("common.save")}</button>
        </div>`;

      // Custom HTML per status lives behind a button rather than eleven
      // always-open textareas, which would be 3,000 pixels of form.
      const htmlStore = {};
      codes.forEach(code => { htmlStore[code] = (eps[code] || {}).html || ""; });
      body.querySelectorAll("[data-code-html]").forEach(b => {
        b.onclick = async () => {
          const code = b.dataset.codeHtml;
          const val = await promptHTML(code, htmlStore[code]);
          if (val === null) return;
          htmlStore[code] = val;
          toast(t("rs.html_staged").replace("{c}", code), "info");
        };
      });

      const modeSel = body.querySelector("#rd-mode");
      modeSel.onchange = () => {
        body.querySelector("#rd-custom").hidden = modeSel.value !== "custom";
      };

      body.querySelector("#rd-save").onclick = async () => {
        const errorPages = {};
        codes.forEach(code => {
          const on = body.querySelector(`[data-code-on="${code}"]`);
          const disabled = on ? !on.checked : false;
          const html = htmlStore[code] || "";
          if (disabled || html) errorPages[code] = { disabled, html };
        });
        const payload = {
          enabled: body.querySelector("#rd-on").checked,
          mode: modeSel.value,
          template: body.querySelector("#rd-tpl").value,
          error_mode: body.querySelector("#rd-emode").value,
          error_template: body.querySelector("#rd-etpl").value,
          error_pages: errorPages,
          root: body.querySelector("#rd-root").value.trim(),
          extra_config: body.querySelector("#rd-extra").value,
          cache_assets: body.querySelector("#rd-cache").checked,
          content: {
            site_name: body.querySelector("#rd-name").value,
            tagline: body.querySelector("#rd-tag").value,
            language: body.querySelector("#rd-lang").value.trim(),
            email: body.querySelector("#rd-email").value.trim(),
            phone: body.querySelector("#rd-phone").value.trim(),
            address: body.querySelector("#rd-addr").value,
            hero_title: body.querySelector("#rd-hero").value,
            hero_text: body.querySelector("#rd-herotext").value,
            noindex: body.querySelector("#rd-noindex").checked,
          },
        };
        try {
          const res = await api(`/api/realsite/domains/${encodeURIComponent(domain)}`,
            { method: "PUT", body: JSON.stringify(payload) });
          toast(t("common.saved"), "success");
          if (res.apply_error) toast(res.apply_error, "error");
          drawer.hidden = true;
          await refresh();
        } catch (e) { toast(e.message, "error"); }
      };
    }

    /* A tiny modal for a status page's own HTML. */
    function promptHTML(code, current) {
      return new Promise(resolve => {
        const el = document.createElement("div");
        el.className = "tp-drawer";
        el.innerHTML = `
          <div class="tp-drawer-back"></div>
          <div class="tp-drawer-panel">
            <div class="tp-drawer-head"><h2>${t("rs.custom_html")} — ${esc(code)}</h2></div>
            <div class="tp-drawer-body">
              <p class="muted">${t("rs.custom_html_help")}</p>
              <textarea id="rh-t" rows="14" class="mono" style="width:100%">${esc(current || "")}</textarea>
              <div class="form-actions">
                <button class="btn btn-ghost" id="rh-x">${t("common.cancel")}</button>
                <button class="btn btn-primary" id="rh-ok">${t("common.save")}</button>
              </div>
            </div>
          </div>`;
        document.body.appendChild(el);
        const done = v => { el.remove(); resolve(v); };
        el.querySelector("#rh-x").onclick = () => done(null);
        el.querySelector(".tp-drawer-back").onclick = () => done(null);
        el.querySelector("#rh-ok").onclick = () => done(el.querySelector("#rh-t").value);
      });
    }

    /* ── what is actually on disk ──
       "It says it is on, but is it really serving anything" is the first
       question anyone asks, and the honest answer is a file list. */
    async function openFiles(domain) {
      const title = container.querySelector("#rs-d-title");
      const body = container.querySelector("#rs-d-body");
      title.textContent = domain;
      body.innerHTML = `<div class="tp-loading"><span class="spinner-sm"></span></div>`;
      drawer.hidden = false;
      try {
        const f = await api(`/api/realsite/domains/${encodeURIComponent(domain)}/files`);
        body.innerHTML = `
          <p class="muted">${t("rs.root_is")} <code class="mono">${esc(f.root)}</code></p>
          <p class="muted">${(f.files || []).length} ${t("rs.files")} · ${fmtBytes(f.bytes)}</p>
          <h3 class="tp-sub">${t("rs.preview")}</h3>
          <div class="tp-preview">
            <iframe sandbox="" loading="lazy" title="${esc(domain)}"
              src="api/templates/${encodeURIComponent(
                ((data.domains || []).find(x => x.domain === domain) || {}).effective_template || ""
              )}/preview?domain=${encodeURIComponent(domain)}"></iframe>
          </div>
          <ul class="tp-files">${(f.files || []).map(x =>
            `<li><span>${esc(x.path)}</span><span class="muted">${fmtBytes(x.bytes)}</span></li>`).join("")}</ul>`;
      } catch (e) {
        body.innerHTML = `<p class="tp-err">${esc(e.message)}</p>`;
      }
    }

    /* ── panel-wide save ── */
    const defOn = container.querySelector("#rs-def-on");
    defOn.onchange = () => {
      container.querySelector("#rs-def-body").hidden = !defOn.checked;
    };
    container.querySelector("#rs-gallery").onclick = () => navigate("templates");
    container.querySelector("#rs-save").onclick = async () => {
      const payload = {
        repo: container.querySelector("#rs-repo").value.trim(),
        ref: container.querySelector("#rs-ref").value.trim(),
        mirror: container.querySelector("#rs-mirror").value.trim(),
        cache_hours: Number(container.querySelector("#rs-cache").value) || 0,
        sites_dir: container.querySelector("#rs-dir").value.trim(),
        defaults: {
          enabled: defOn.checked,
          template: container.querySelector("#rs-def-tpl").value,
          content: {
            site_name: container.querySelector("#rs-def-name").value,
            tagline: container.querySelector("#rs-def-tag").value,
            language: container.querySelector("#rs-def-lang").value.trim(),
            email: container.querySelector("#rs-def-email").value.trim(),
            phone: container.querySelector("#rs-def-phone").value.trim(),
            address: container.querySelector("#rs-def-addr").value,
            noindex: container.querySelector("#rs-def-noindex").checked,
          },
        },
      };
      try {
        const res = await api("/api/realsite", { method: "PUT", body: JSON.stringify(payload) });
        toast(t("common.saved"), "success");
        if (res.apply_error) toast(res.apply_error, "error");
        await refresh();
      } catch (e) { toast(e.message, "error"); }
    };

    async function refresh() {
      try {
        data = await api("/api/realsite");
        drawList();
      } catch (e) { toast(e.message, "error"); }
    }

    drawList();
  }
};

})();
