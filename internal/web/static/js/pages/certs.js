/* Certificates — issue, renew and inspect TLS certificates.

   The list is the honest view: it describes what nginx will actually serve,
   including certificates the operator installed by hand, ones that expired,
   and ones whose key does not match. A row is only "managed" when this panel
   issued it, because that is what decides whether Renew can run unattended. */
window.Pages = window.Pages || {};

(function () {
"use strict";

/* Issuance takes minutes, so the dialog polls a job rather than holding a
   request open. This is also what makes the manual DNS flow possible: the
   job parks in "waiting_dns" until the operator confirms the record. */
let pollTimer = null;
function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

function fmtDate(v) {
  if (!v) return "—";
  const d = new Date(v);
  if (isNaN(d)) return "—";
  return d.toISOString().slice(0, 10);
}

/* One badge that answers "can I rely on this certificate right now?".
   Order matters: a broken pair is worse news than an approaching expiry. */
/* Self-signed and near-expiry are INDEPENDENT facts, so both are shown.
   Collapsing them hid the more urgent one: a self-signed certificate that
   expires in five days was reported only as "self-signed", and the operator
   had no way to see the deadline from the list. */
function stateBadge(c, t) {
  if (c.error) return `<span class="badge badge-danger">${c.error}</span>`;

  const out = [];
  if (c.expired) {
    out.push(`<span class="badge badge-danger">${t("certs.expired")}</span>`);
  } else if (c.due_renew) {
    out.push(`<span class="badge badge-warning">${t("certs.due_soon")} (${c.days_left}d)</span>`);
  } else {
    out.push(`<span class="badge badge-success">${c.days_left} ${t("certs.days_left")}</span>`);
  }
  if (c.self_signed) {
    out.push(`<span class="badge badge-warning">${t("certs.self_signed")}</span>`);
  }
  return out.join(" ");
}

window.Pages.certs = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog, toast, navigate } = ctx;
    stopPolling();

    let data;
    try {
      data = await api("/api/certs");
    } catch (e) {
      container.innerHTML = `<div class="card"><p style="color:var(--danger)">${e.message}</p></div>`;
      return;
    }
    const list = data.certs || [];
    const acme = data.acme || {};

    const rows = list.map(c => `
      <tr class="row-main" data-domain="${c.domain}">
        <td><strong>${c.domain}</strong>
          ${c.managed ? `<span class="badge badge-info">${t("certs.managed")}</span>` : ""}
          ${c.wildcard ? `<span class="badge badge-neutral mono">*</span>` : ""}
          ${c.acme && c.acme.staging ? `<span class="badge badge-warning">staging</span>` : ""}
        </td>
        <td>${stateBadge(c, t)}</td>
        <td class="mono" dir="ltr">${fmtDate(c.not_after)}</td>
        <td class="muted">${c.issuer || "—"}</td>
        <td class="row-actions">
          <button class="btn btn-sm btn-ghost" data-view="${c.domain}" title="${t("certs.details")}">${Icons.svg("eye", 13)}</button>
          <button class="btn btn-sm btn-primary" data-issue="${c.domain}" title="${c.cert_path ? t("certs.renew") : t("certs.issue")}">
            ${Icons.svg(c.cert_path ? "refresh" : "plus", 13)}</button>
          ${c.cert_path ? `<button class="btn btn-danger btn-sm" data-del="${c.domain}" title="${t("certs.detach")}">${Icons.svg("trash", 13)}</button>` : ""}
        </td>
      </tr>
      <tr class="row-path"><td colspan="5">
        <div class="path-line"><span class="path-label">${t("certs.names")}</span>
        <code dir="ltr">${(c.names || []).join(", ") || "—"}</code></div>
      </td></tr>`).join("");

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("lock", 20)} ${t("certs.title")}</h1>
        <div class="btn-row">
          <button class="btn btn-primary" id="add-domain">${Icons.svg("plus", 14)} ${t("certs.add_domain")}</button>
          <button class="btn btn-ghost" id="acme-settings">${Icons.svg("settings", 14)} ${t("certs.acme_settings")}</button>
        </div>
      </div>

      ${acme.staging ? `<div class="card" style="border-color:var(--warning)">
        <p style="margin:0">${Icons.svg("warning", 14)} ${t("certs.staging_warning")}</p></div>` : ""}

      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr>
          <th>${t("certs.domain")}</th>
          <th>${t("certs.status")}</th>
          <th>${t("certs.expires")}</th>
          <th>${t("certs.issuer")}</th>
          <th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table></div>
      ${list.length ? "" : `<div class="log-empty">${t("certs.empty")}</div>`}
      </div>`;

    container.querySelector("#acme-settings").onclick = () => acmeDialog(ctx, acme);

    // A certificate is always FOR a domain, so the domain has to exist
    // first. Making the operator leave for the Domains page to discover
    // that is a pointless detour, so the same step is offered here.
    container.querySelector("#add-domain").onclick = () => addDomainDialog(ctx);
    wireCopyButtons(container, t, toast);

    container.querySelectorAll("[data-issue]").forEach(b =>
      b.onclick = () => issueDialog(ctx, b.dataset.issue, acme,
        list.find(c => c.domain === b.dataset.issue)));

    container.querySelectorAll("[data-view]").forEach(b =>
      b.onclick = () => detailsDialog(ctx, list.find(c => c.domain === b.dataset.view)));

    container.querySelectorAll("[data-del]").forEach(b => b.onclick = () => {
      const n = b.dataset.del;
      confirmDialog(t("certs.detach_confirm").replace("%s", n), async () => {
        try {
          await api("/api/certs/" + encodeURIComponent(n), { method: "DELETE" });
          toast(t("certs.detached"), "success");
          navigate("certs");
        } catch (e) { toast(e.message, "error"); }
      });
    });

    // The stats page pattern: stop timers when the user navigates away.
    container._shahragCleanup = stopPolling;
  },
};

/* Register a domain without leaving the Certificates page, then go straight
   into issuing for it — which is what the operator wanted in the first
   place. */
function addDomainDialog(ctx) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  modal(t("certs.add_domain"), `
    <div class="form-error" id="nd-err" hidden></div>
    <div class="field field-wide">
      <label>${t("certs.domain")}${Icons.help(t("certs.add_domain_help"))}</label>
      <input id="nd-name" dir="ltr" class="mono" placeholder="example.com" autofocus>
      <span class="hint">${t("certs.add_domain_hint")}</span>
    </div>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: t("common.add"), class: "btn-primary", icon: "plus", keepOpen: true,
       onClick: async () => {
         const err = document.getElementById("nd-err");
         err.hidden = true;
         // Accept a pasted wildcard or a trailing dot; the certificate is
         // requested for the base name either way.
         let n = document.getElementById("nd-name").value.trim().toLowerCase();
         n = n.replace(/^\*\./, "").replace(/\.$/, "");
         if (!n || !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(n)) {
           err.textContent = t("certs.err_domain");
           err.hidden = false;
           return;
         }
         try {
           await api("/api/domains", {
             method: "POST",
             body: JSON.stringify({ name: n, cert: "", key: "" }),
           });
           window.closeModal();
           toast(t("certs.domain_added"), "success");
           navigate("certs");
         } catch (e) { err.textContent = e.message; err.hidden = false; }
       }}]);
}

/* ── ACME account settings ─────────────────────────────────────────── */
function acmeDialog(ctx, acme) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  modal(t("certs.acme_settings"), `
    <div class="form-error" id="a-err" hidden></div>
    <div class="field field-wide">
      <label>${t("certs.email")}${Icons.help(t("certs.email_help"))}</label>
      <input id="a-email" type="email" dir="ltr" value="${acme.email || ""}" placeholder="you@example.com">
    </div>
    <div class="field field-wide">
      <label>${t("certs.cf_token")}${Icons.help(t("certs.cf_token_help"))}</label>
      <input id="a-cf" type="password" dir="ltr" class="mono" autocomplete="off"
        placeholder="${acme.cloudflare_configured ? t("certs.cf_stored") : "cloudflare API token"}">
      ${acme.cloudflare_configured ? `<span class="hint">${t("certs.cf_leave_blank")}</span>` : ""}
    </div>
    <label class="switch"><input type="checkbox" id="a-auto" ${acme.auto_renew ? "checked" : ""}>
      <span class="switch-track"><span class="switch-thumb"></span></span>
      <span>${t("certs.auto_renew")}</span>${Icons.help(t("certs.auto_renew_help"))}</label>
    <label class="switch"><input type="checkbox" id="a-staging" ${acme.staging ? "checked" : ""}>
      <span class="switch-track"><span class="switch-thumb"></span></span>
      <span>${t("certs.staging")}</span>${Icons.help(t("certs.staging_help"))}</label>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: t("common.save"), class: "btn-primary", icon: "check", keepOpen: true, onClick: async () => {
        const err = document.getElementById("a-err");
        err.hidden = true;
        const body = {
          email: document.getElementById("a-email").value.trim(),
          staging: document.getElementById("a-staging").checked,
          auto_renew: document.getElementById("a-auto").checked,
        };
        // An empty box means "keep the stored token", never "erase it".
        const tok = document.getElementById("a-cf").value.trim();
        if (tok) body.cloudflare_token = tok;
        try {
          await api("/api/certs/acme", { method: "PUT", body: JSON.stringify(body) });
          window.closeModal();
          toast(t("settings.saved"), "success");
          navigate("certs");
        } catch (e) { err.textContent = e.message; err.hidden = false; }
     }}]);
}

/* ── Issue / renew ─────────────────────────────────────────────────── */
function issueDialog(ctx, domain, acme, current) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  const renewing = !!(current && current.cert_path);
  // Repeat what was asked for last time, so Renew does not silently change
  // the shape of the certificate.
  const wasWildcard = current && current.acme ? !!current.acme.wildcard : true;

  /* Credentials are per-domain, because several domains can live in
     DIFFERENT Cloudflare accounts. The fields start from this domain's own
     override if it has one, otherwise from the account-wide default, and
     the reset button next to each puts the default back. */
  const meta = (current && current.acme) || {};
  const defEmail = acme.email || "";
  const domEmail = meta.email || "";
  const startEmail = domEmail || defEmail;
  const hasDomToken = !!meta.has_token;

  modal(`${renewing ? t("certs.renew") : t("certs.issue")} — ${domain}`, `
    <div class="form-error" id="i-err" hidden></div>

    <div class="field field-wide">
      <label>${t("certs.scope")}${Icons.help(t("certs.scope_help"))}</label>
      <select id="i-scope">
        <option value="wildcard" ${wasWildcard ? "selected" : ""}>${t("certs.scope_wildcard").replace("%s", domain)}</option>
        <option value="single" ${wasWildcard ? "" : "selected"}>${t("certs.scope_single").replace("%s", domain)}</option>
      </select>
    </div>

    <div class="field field-wide">
      <label>${t("certs.method")}${Icons.help(t("certs.method_help"))}</label>
      <select id="i-method">
        <option value="cloudflare" ${(acme.cloudflare_configured || hasDomToken) ? "" : "disabled"}>
          ${t("certs.method_cf")}${(acme.cloudflare_configured || hasDomToken) ? "" : " — " + t("certs.method_cf_missing")}</option>
        <option value="manual" ${(acme.cloudflare_configured || hasDomToken) ? "" : "selected"}>${t("certs.method_manual")}</option>
      </select>
    </div>

    <div class="field field-wide">
      <label>${t("certs.email")}${Icons.help(t("certs.email_domain_help"))}</label>
      <div class="input-row">
        <input id="i-email" type="email" dir="ltr" value="${startEmail}" placeholder="you@example.com">
        <button type="button" class="icon-btn" id="i-email-reset"
          title="${t("certs.reset_default")}">${Icons.svg("refresh", 15)}</button>
      </div>
      ${domEmail && domEmail !== defEmail
        ? `<span class="hint">${t("certs.using_override")}</span>` : ""}
    </div>

    <div class="field field-wide" id="i-token-wrap">
      <label>${t("certs.cf_token")}${Icons.help(t("certs.cf_token_domain_help"))}</label>
      <div class="input-row">
        <input id="i-token" type="password" dir="ltr" class="mono" autocomplete="off"
          placeholder="${hasDomToken ? t("certs.token_domain_stored")
                        : acme.cloudflare_configured ? t("certs.token_default_stored")
                        : "cloudflare API token"}">
        <button type="button" class="icon-btn" id="i-token-reset"
          title="${t("certs.reset_default")}">${Icons.svg("refresh", 15)}</button>
      </div>
      <span class="hint">${t("certs.token_blank_hint")}</span>
    </div>

    <div class="field field-wide">
      <label>${t("certs.extra_names")}${Icons.help(t("certs.extra_names_help"))}</label>
      <input id="i-extra" dir="ltr" class="mono" value="${(meta.extra_names || []).join(", ")}"
        placeholder="*.app.${domain}, vpn.eu.${domain}">
      <span class="hint">${t("certs.extra_names_hint")}</span>
      <div class="hint" id="i-extra-tip" hidden></div>
    </div>

    <label class="checkbox"><input type="checkbox" id="i-remember" checked>
      <span class="check-box"></span> <span>${t("certs.remember")}</span>${Icons.help(t("certs.remember_help"))}</label>

    <div id="i-http-note" hidden>
      <p class="hint">${t("certs.http_note")}</p>
    </div>

    <div id="i-progress" hidden>
      <div class="tiny" id="i-state"></div>
      <pre class="log-view" id="i-log" style="max-height:200px"></pre>
      <div id="i-record" hidden>
        <div class="tiny">${t("certs.manual_record")}</div>
        <table class="data-table"><tbody>
          <tr><td>${t("certs.rec_type")}</td><td class="mono">TXT</td></tr>
          <tr><td>${t("certs.rec_name")}</td><td class="mono" id="i-rec-name" dir="ltr"></td></tr>
          <tr><td>${t("certs.rec_value")}</td><td class="mono" id="i-rec-value" dir="ltr" style="overflow-wrap:anywhere"></td></tr>
        </tbody></table>
        <button class="btn btn-primary btn-block" id="i-confirm" style="margin-top:10px">
          ${Icons.svg("check", 14)} ${t("certs.rec_done")}</button>
      </div>
    </div>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: renewing ? t("certs.renew") : t("certs.issue"), class: "btn-primary",
       icon: "lock", keepOpen: true, onClick: () => start() }]);

  // Reset puts the account-wide default back, which is the way out when
  // someone typed the wrong credentials into this dialog.
  const emailReset = document.getElementById("i-email-reset");
  if (emailReset) {
    emailReset.onclick = () => {
      document.getElementById("i-email").value = defEmail;
      toast(t("certs.reset_done"), "info");
    };
  }
  const tokenReset = document.getElementById("i-token-reset");
  if (tokenReset) {
    tokenReset.onclick = () => {
      // An empty box means "use the stored default", so clearing IS the
      // reset. The placeholder already says which one that will be.
      const el = document.getElementById("i-token");
      el.value = "";
      el.dataset.reset = "1";
      toast(t("certs.reset_done"), "info");
    };
  }
  /* Live guidance. A host two levels deep is the exact case people get
     wrong, so instead of only rejecting it later we point at the nested
     wildcard that would cover the whole level. */
  const extraEl = document.getElementById("i-extra");
  const tipEl = document.getElementById("i-extra-tip");
  if (extraEl && tipEl) {
    extraEl.oninput = () => {
      const names = splitNames(extraEl.value);
      const tips = [];
      for (const n of names) {
        if (n.startsWith("*.")) continue;
        const w = suggestParentWildcard(domain, n);
        if (w && !names.includes(w)) {
          tips.push(t("certs.extra_tip").replace("%s", n).replace("%w", w));
        }
      }
      tipEl.innerHTML = tips.join("<br>");
      tipEl.hidden = tips.length === 0;
    };
  }

  // The token only matters for the Cloudflare path.
  const methodSel = document.getElementById("i-method");
  const tokenWrap = document.getElementById("i-token-wrap");
  function syncMethod() {
    tokenWrap.hidden = methodSel.value !== "cloudflare";
  }
  methodSel.onchange = syncMethod;
  syncMethod();

  async function start() {
    const err = document.getElementById("i-err");
    err.hidden = true;
    const scope = document.getElementById("i-scope").value;
    const method = document.getElementById("i-method").value;
    const body = {
      domain,
      wildcard: scope === "wildcard",
      // A wildcard can only be validated over DNS; the server enforces this
      // too, but choosing correctly here avoids a pointless round trip.
      challenge: "dns-01",
      method,
      remember: document.getElementById("i-remember").checked,
    };

    // Only send what the operator actually set. An empty field means "keep
    // using the stored value", never "erase it".
    const em = document.getElementById("i-email").value.trim();
    if (em) body.email = em;
    const tk = document.getElementById("i-token").value.trim();
    if (tk) body.cloudflare_token = tk;
    body.extra_names = splitNames(document.getElementById("i-extra").value);

    const btns = document.querySelectorAll(".modal-footer .btn");
    btns.forEach(b => b.disabled = true);
    document.getElementById("i-progress").hidden = false;

    let job;
    try {
      job = await api("/api/certs/issue", { method: "POST", body: JSON.stringify(body) });
    } catch (e) {
      err.textContent = e.message; err.hidden = false;
      btns.forEach(b => b.disabled = false);
      return;
    }
    poll(job.job);
  }

  function poll(id) {
    const logEl = document.getElementById("i-log");
    const stateEl = document.getElementById("i-state");
    const recEl = document.getElementById("i-record");

    stopPolling();
    pollTimer = setInterval(async () => {
      let j;
      try {
        j = await api("/api/certs/jobs/" + encodeURIComponent(id));
      } catch (e) { return; }

      logEl.textContent = (j.log || []).join("\n");
      logEl.scrollTop = logEl.scrollHeight;
      stateEl.textContent = t("certs.state_" + j.state) || j.state;

      if (j.state === "waiting_dns" && j.record) {
        recEl.hidden = false;
        document.getElementById("i-rec-name").textContent = j.record.name;
        document.getElementById("i-rec-value").textContent = j.record.value;
        const btn = document.getElementById("i-confirm");
        if (!btn.dataset.wired) {
          btn.dataset.wired = "1";
          btn.onclick = async () => {
            btn.disabled = true;
            try {
              await api("/api/certs/jobs/" + encodeURIComponent(id) + "/confirm",
                { method: "POST" });
              recEl.hidden = true;
            } catch (e) { toast(e.message, "error"); btn.disabled = false; }
          };
        }
      } else {
        recEl.hidden = true;
      }

      if (j.state === "done") {
        stopPolling();
        toast(t("certs.issued"), "success");
        window.closeModal();
        navigate("certs");
      } else if (j.state === "error") {
        stopPolling();
        const err = document.getElementById("i-err");
        err.textContent = j.error || "failed";
        err.hidden = false;
        document.querySelectorAll(".modal-footer .btn").forEach(b => b.disabled = false);
      }
    }, 1500);
  }
}

function splitNames(v) {
  return (v || "").split(",").map(x => x.trim().toLowerCase()).filter(Boolean);
}

/* Mirrors certs.SuggestParentWildcard on the server: for a host more than
   one label below the domain, the wildcard that would cover its whole
   level. Returns "" when *.domain already covers it. */
function suggestParentWildcard(base, host) {
  base = (base || "").toLowerCase().replace(/^\*\./, "");
  host = (host || "").toLowerCase().replace(/^\*\./, "");
  if (!base || !host || host === base || !host.endsWith("." + base)) return "";
  const prefix = host.slice(0, -(base.length + 1));
  const labels = prefix.split(".");
  if (labels.length < 2) return "";
  return "*." + labels.slice(1).join(".") + "." + base;
}

/* ── Details ───────────────────────────────────────────────────────── */
/* copyRow renders a path with a copy button beside it. Paths are long and
   easy to mistype, and the operator often needs them for another tool. */
function copyRow(label, value, Icons, t) {
  if (!value) return `<tr><td>${label}</td><td class="muted">—</td></tr>`;
  const esc = String(value).replace(/"/g, "&quot;");
  return `<tr><td>${label}</td><td>
      <div class="copy-cell">
        <code class="mono" dir="ltr">${value}</code>
        <button type="button" class="icon-btn copy-btn" data-copy="${esc}"
          title="${t("certs.copy")}">${Icons.svg("copy", 14)}</button>
      </div></td></tr>`;
}

function detailsDialog(ctx, c) {
  const { t, modal, toast, Icons } = ctx;
  if (!c) return;
  const row = (k, v) => `<tr><td>${k}</td><td class="mono" dir="ltr" style="overflow-wrap:anywhere">${v || "—"}</td></tr>`;
  modal(`${t("certs.details")} — ${c.domain}`, `
    <table class="data-table"><tbody>
      ${row(t("certs.names"), (c.names || []).join("<br>"))}
      ${row(t("certs.issuer"), c.issuer)}
      ${row(t("certs.not_before"), fmtDate(c.not_before))}
      ${row(t("certs.expires"), fmtDate(c.not_after))}
      ${copyRow(t("certs.cert_path"), c.cert_path, Icons, t)}
      ${copyRow(t("certs.key_path"), c.key_path, Icons, t)}
      ${c.error ? row(t("certs.problem"), c.error) : ""}
    </tbody></table>`,
    [{ label: t("common.close"), class: "btn-ghost" }]);

  wireCopyButtons(document.getElementById("modal"), t, toast);
}

/* Copying needs a fallback: navigator.clipboard is only available on a
   secure origin, and a panel reached over plain HTTP on a LAN address is
   not one — which is exactly how many people first open Shahrag. */
function wireCopyButtons(root, t, toast) {
  if (!root) return;
  root.querySelectorAll(".copy-btn").forEach(b => {
    b.onclick = async () => {
      const text = b.dataset.copy || "";
      let ok = false;
      try {
        if (navigator.clipboard && window.isSecureContext) {
          await navigator.clipboard.writeText(text);
          ok = true;
        }
      } catch (_) { ok = false; }
      if (!ok) {
        // execCommand is deprecated but still the only thing that works
        // without a secure context.
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.setAttribute("readonly", "");
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        try { ok = document.execCommand("copy"); } catch (_) { ok = false; }
        ta.remove();
      }
      toast(ok ? t("certs.copied") : t("certs.copy_failed"), ok ? "success" : "error");
    };
  });
}

})();
