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
        <button class="btn btn-ghost" id="acme-settings">${Icons.svg("settings", 14)} ${t("certs.acme_settings")}</button>
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
        <option value="cloudflare" ${acme.cloudflare_configured ? "" : "disabled"}>
          ${t("certs.method_cf")}${acme.cloudflare_configured ? "" : " — " + t("certs.method_cf_missing")}</option>
        <option value="manual" ${acme.cloudflare_configured ? "" : "selected"}>${t("certs.method_manual")}</option>
      </select>
    </div>

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
    };

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

/* ── Details ───────────────────────────────────────────────────────── */
function detailsDialog(ctx, c) {
  const { t, modal } = ctx;
  if (!c) return;
  const row = (k, v) => `<tr><td>${k}</td><td class="mono" dir="ltr" style="overflow-wrap:anywhere">${v || "—"}</td></tr>`;
  modal(`${t("certs.details")} — ${c.domain}`, `
    <table class="data-table"><tbody>
      ${row(t("certs.names"), (c.names || []).join("<br>"))}
      ${row(t("certs.issuer"), c.issuer)}
      ${row(t("certs.not_before"), fmtDate(c.not_before))}
      ${row(t("certs.expires"), fmtDate(c.not_after))}
      ${row(t("certs.cert_path"), c.cert_path)}
      ${row(t("certs.key_path"), c.key_path)}
      ${c.error ? row(t("certs.problem"), c.error) : ""}
    </tbody></table>`,
    [{ label: t("common.close"), class: "btn-ghost" }]);
}

})();
