/* SNI routing page.

   Routing here is decided purely by the TLS SNI the client sends, so the
   page is called "SNI routing" rather than "Reality" — Reality is just one
   thing you can point a rule at.

   Each rule sends matching traffic to one of three places:
     • this server        → 127.0.0.1:<port>          (the classic behaviour)
     • the real internet  → $ssl_preread_server_name  (unblock / exit routing)
     • another server     → host:<port>
   The config keys are unchanged, so existing setups load as-is. */
window.Pages = window.Pages || {};

// Wrapped in an IIFE: these files are loaded with plain <script> tags, so a
// top-level `const` would be a GLOBAL binding. Re-loading the module (or any
// other page declaring the same name) then throws "Identifier has already
// been declared" and the browser aborts the entire file — the page silently
// fails to register and clicking the nav item appears to do nothing.
(function () {
"use strict";

const PASSTHROUGH = "$passthrough";

function targetLabel(target, t) {
  const v = (target || "").trim();
  if (!v || v === "localhost" || v === "127.0.0.1") return t("reality.target_local");
  if (v === PASSTHROUGH) return t("reality.target_pass");
  return v;
}

function targetBadge(target, t) {
  const v = (target || "").trim();
  if (v === PASSTHROUGH) return `<span class="badge badge-info">${t("reality.target_pass")}</span>`;
  if (!v || v === "localhost" || v === "127.0.0.1") return `<span class="badge badge-neutral">${t("reality.target_local")}</span>`;
  return `<span class="badge badge-success mono">${v}</span>`;
}

window.Pages.reality = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog, toast, navigate } = ctx;
    const r = await api("/api/reality");
    const resolvers = (r.resolvers && r.resolvers.length ? r.resolvers : ["1.1.1.1", "8.8.8.8"]).join(", ");

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("reality", 20)} ${t("reality.title")}</h1>
        <button class="btn btn-primary" id="r-add">${Icons.svg("plus", 14)} ${t("reality.add_service")}</button>
      </div>
      <div class="card">
        <label class="switch">
          <input type="checkbox" id="r-en" ${r.enabled ? "checked" : ""}>
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("reality.enabled")}</span>
        </label>
        <div class="field-row">
          <div class="field field-port">
            <label>${t("reality.http_port")}</label>
            <input id="r-port" type="number" inputmode="numeric" min="1" max="65535" value="${r.http_port || 6038}">
          </div>
          <div class="field field-wide">
            <label>${t("reality.resolvers")}</label>
            <input id="r-res" dir="ltr" class="mono" value="${resolvers}" placeholder="1.1.1.1, 8.8.8.8">
            <span class="hint">${t("reality.resolvers_hint")}</span>
          </div>
        </div>
        <div class="btn-row"><button class="btn btn-primary" id="r-save">${Icons.svg("check", 14)} ${t("common.save")}</button></div>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr>
          <th>${t("services.name")}</th><th>SNI</th>
          <th>${t("reality.target")}</th>
          <th>${t("services.local_port")}</th><th>${t("ports.title")}</th><th></th>
        </tr></thead>
        <tbody>${Object.entries(r.services || {}).map(([n, s]) => `
          <tr>
            <td><strong>${n}</strong></td>
            <td><code>${s.sni}</code></td>
            <td>${targetBadge(s.target, t)}</td>
            <td class="num">${s.target === PASSTHROUGH ? "" : ":"}${s.local_port}</td>
            <td class="num">${(s.ports || []).join(", ")}</td>
            <td class="row-actions">
              <button class="btn btn-ghost btn-sm" data-edit="${n}">${Icons.svg("edit", 13)}</button>
              <button class="btn btn-danger btn-sm" data-del="${n}">${Icons.svg("trash", 13)}</button>
            </td>
          </tr>`).join("")}
        </tbody></table></div></div>`;

    document.getElementById("r-save").onclick = async () => {
      try {
        const res = document.getElementById("r-res").value
          .split(/[,\s]+/).map(x => x.trim()).filter(Boolean);
        await api("/api/reality", {
          method: "PUT",
          body: JSON.stringify({
            enabled: document.getElementById("r-en").checked,
            http_port: +document.getElementById("r-port").value,
            resolvers: res,
          }),
        });
        toast(t("settings.saved"), "success");
        navigate("reality");
      } catch (e) { toast(e.message, "error"); }
    };

    document.getElementById("r-add").onclick = () => sniForm(ctx, null, {});
    container.querySelectorAll("[data-edit]").forEach(b => b.onclick = () =>
      sniForm(ctx, b.dataset.edit, (r.services || {})[b.dataset.edit] || {}));
    container.querySelectorAll("[data-del]").forEach(b => b.onclick = () => {
      confirmDialog(t("reality.delete_confirm") + " (" + b.dataset.del + ")", async () => {
        try {
          await api("/api/reality/services/" + encodeURIComponent(b.dataset.del), { method: "DELETE" });
          toast(t("services.deleted"), "success");
          navigate("reality");
        } catch (e) { toast(e.message, "error"); }
      });
    });
  },
};

function sniForm(ctx, name, svc) {
  const { t, modal, api, toast, navigate } = ctx;
  const isEdit = !!name;
  const cur = (svc.target || "").trim();
  const mode = cur === PASSTHROUGH ? "pass"
    : (!cur || cur === "localhost" || cur === "127.0.0.1") ? "local" : "remote";

  modal(isEdit ? t("common.edit") + " — " + name : t("reality.add_service"), `
    <div class="form-error" id="r-err" hidden></div>
    <div class="field">
      <label>${t("services.name")}</label>
      <input id="r-n" value="${name || ""}" ${isEdit ? "disabled" : ""} placeholder="epicgames">
    </div>
    <div class="field field-wide">
      <label>SNI</label>
      <input id="r-sni" dir="ltr" class="mono" value="${svc.sni || ""}" placeholder="*.epicgames.com">
      <span class="hint">${t("reality.sni_hint")}</span>
    </div>
    <div class="field">
      <label>${t("reality.target")}</label>
      <select id="r-mode">
        <option value="local"  ${mode === "local" ? "selected" : ""}>${t("reality.target_local")}</option>
        <option value="pass"   ${mode === "pass" ? "selected" : ""}>${t("reality.target_pass")}</option>
        <option value="remote" ${mode === "remote" ? "selected" : ""}>${t("reality.target_remote")}</option>
      </select>
      <span class="hint">${t("reality.target_hint")}</span>
    </div>
    <div class="field field-wide" id="r-host-wrap" ${mode === "remote" ? "" : "hidden"}>
      <label>${t("reality.target_host")}</label>
      <input id="r-host" dir="ltr" class="mono" value="${mode === "remote" ? cur : ""}" placeholder="203.0.113.10">
    </div>
    <div class="field-row">
      <div class="field field-port">
        <label>${t("services.local_port")}</label>
        <input id="r-lp" type="number" inputmode="numeric" min="1" max="65535" value="${svc.local_port || 443}">
      </div>
      <div class="field field-port">
        <label>${t("reality.ports")}</label>
        <input id="r-p" type="number" inputmode="numeric" min="1" max="65535" value="${(svc.ports && svc.ports[0]) || 443}">
      </div>
    </div>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: t("common.save"), class: "btn-primary", icon: "check", keepOpen: true, onClick: async () => {
        const err = document.getElementById("r-err");
        err.hidden = true;
        try {
          const nm = document.getElementById("r-n").value.trim();
          const sni = document.getElementById("r-sni").value.trim();
          if (!nm) throw new Error(t("services.err_name"));
          if (!sni) throw new Error(t("reality.err_sni"));

          const m = document.getElementById("r-mode").value;
          let target = "";
          if (m === "pass") target = PASSTHROUGH;
          else if (m === "remote") {
            target = document.getElementById("r-host").value.trim();
            if (!target) throw new Error(t("reality.err_target"));
          }
          const body = {
            sni, target,
            local_port: +document.getElementById("r-lp").value,
            ports: [+document.getElementById("r-p").value],
          };
          if (isEdit) {
            await api("/api/reality/services/" + encodeURIComponent(nm),
              { method: "PUT", body: JSON.stringify(body) });
          } else {
            await api("/api/reality/services",
              { method: "POST", body: JSON.stringify({ name: nm, ...body }) });
          }
          window.closeModal();
          toast(t("reality.added"), "success");
          navigate("reality");
        } catch (e) {
          err.textContent = e.message;
          err.hidden = false;
        }
      } }]);

  // Show the host field only for an explicit remote target.
  const sel = document.getElementById("r-mode");
  if (sel) sel.onchange = () => {
    document.getElementById("r-host-wrap").hidden = sel.value !== "remote";
  };
}

})();
