/* Services — HTTP services and SNI routing rules in one list.

   Both are "how traffic reaches a backend", so they belong together; only
   the discriminator differs (an HTTP service matches on host+path, an SNI
   rule matches on the TLS SNI). The list shows a TYPE badge, and the add /
   edit dialog opens on the tab for the record's own type.

   This is purely a UI regrouping: the two record types keep their own config
   sections and their own API endpoints, so nothing about the core changes. */
window.Pages = window.Pages || {};

(function () {
"use strict";

const PASSTHROUGH = "$passthrough";
const LOCAL_ALIASES = ["", "localhost", "127.0.0.1"];

function isLocal(v) { return LOCAL_ALIASES.includes((v || "").trim()); }

/* The target column reads the same for both types: "this server" when the
   backend is local, the literal host when it is not, and a distinct badge
   for a pass-through rule, which goes to the real internet. */
function targetBadge(target, t) {
  const v = (target || "").trim();
  if (v === PASSTHROUGH) return `<span class="badge badge-info">${t("reality.target_pass")}</span>`;
  if (isLocal(v)) return `<span class="badge badge-neutral">${t("reality.target_local")}</span>`;
  return `<span class="badge badge-success mono">${v}</span>`;
}

/* A protected service must be obvious in the list: a silent shield is how an
   operator notices they left the gate off on the one panel that needed it. */
function gateBadge(svc, t, Icons) {
  const m = (svc.gate || "").trim();
  if (m !== "js" && m !== "secret") return "";
  const label = m === "secret" ? t("services.gate_secret") : t("services.gate_js");
  return ` <span class="badge badge-gate" title="${label}">${Icons.svg("shield", 11)} ${label}</span>`;
}

/* The on/off control.
   A switch, not a checkbox: this changes the running server the moment it
   is clicked, so it must read as an action rather than as a form field
   waiting to be saved. The state is carried in aria-checked so a screen
   reader announces it, and in a class so the row can grey itself out. */
function powerToggle(name, kind, enabled, t, Icons) {
  const label = enabled ? t("services.disable") : t("services.enable");
  return `<button type="button" role="switch" aria-checked="${enabled}"
    class="pw-toggle ${enabled ? "on" : "off"}"
    data-toggle="${name}" data-toggle-kind="${kind}"
    aria-label="${label}" data-tip="${label}">
    <span class="pw-track"><span class="pw-thumb">${Icons.svg("power", 10)}</span></span>
  </button>`;
}

function typeBadge(kind) {
  return kind === "sni"
    ? `<span class="badge badge-sni">SNI</span>`
    : `<span class="badge badge-http">HTTP</span>`;
}

window.Pages.services = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog, toast, navigate } = ctx;
    const [services, domains, reality] = await Promise.all([
      api("/api/services"),
      api("/api/domains"),
      api("/api/reality").catch(() => ({ services: {} })),
    ]);
    const sniRules = reality.services || {};
    const panelName = state.config?.shahrag?.panel?.service_name;

    // One row shape for both types keeps the list scannable; the path line
    // sits under the record because a long path is unreadable in a column.
    let rowNo = 0;
    const httpRows = Object.keys(services).sort().map(n => {
      const s = services[n];
      rowNo++;
      return `
        <tr class="row-main ${s.disabled ? "row-off" : ""}" data-kind="http" data-name="${n}">
          <td class="num-col">${rowNo}</td>
          <td>${powerToggle(n, "http", !s.disabled, t, Icons)}</td>
          <td>${typeBadge("http")}</td>
          <td><strong>${n}</strong> ${n === panelName ? '<span class="badge badge-info">Panel</span>' : ""}${gateBadge(s, t, Icons)}${s.disabled ? ` <span class="badge badge-off">${t("services.disabled")}</span>` : ""}</td>
          <td>${targetBadge(s.target, t)}</td>
          <td class="num">${s.local_port}</td>
          <td class="num">${s.listen_port}</td>
          <td>${(s.bindings || []).map(b =>
            `<span class="badge badge-neutral">${b.subdomain ? b.subdomain + "." : ""}${b.domain}</span>`).join(" ")}</td>
          <td class="row-actions">
            <button class="btn btn-sm btn-ghost" data-raw="${n}" data-raw-kind="http" title="${t("services.raw")}">${Icons.svg("copy", 15)}</button>
            <button class="btn btn-sm btn-edit" data-edit="${n}" data-kind="http" title="${t("common.edit")}">${Icons.svg("edit", 15)}</button>
            <button class="btn btn-danger btn-sm" data-del="${n}" data-kind="http" title="${t("common.delete")}">${Icons.svg("trash", 15)}</button>
          </td>
        </tr>
        <tr class="row-path"><td colspan="9">
          <div class="path-line"><span class="path-label">${t("services.path")}</span>
          <code>/${s.path === "/" ? "" : s.path}</code></div>
        </td></tr>`;
    }).join("");

    const sniRows = Object.keys(sniRules).sort().map(n => {
      const s = sniRules[n];
      rowNo++;
      return `
        <tr class="row-main ${s.disabled ? "row-off" : ""}" data-kind="sni" data-name="${n}">
          <td class="num-col">${rowNo}</td>
          <td>${powerToggle(n, "sni", !s.disabled, t, Icons)}</td>
          <td>${typeBadge("sni")}</td>
          <td><strong>${n}</strong>${s.disabled ? ` <span class="badge badge-off">${t("services.disabled")}</span>` : ""}</td>
          <td>${targetBadge(s.target, t)}</td>
          <td class="num">${s.local_port || ""}</td>
          <td class="num">${(s.ports || []).join(", ")}</td>
          <td class="muted">—</td>
          <td class="row-actions">
            <button class="btn btn-sm btn-ghost" data-raw="${n}" data-raw-kind="sni" title="${t("services.raw")}">${Icons.svg("copy", 15)}</button>
            <button class="btn btn-sm btn-edit" data-edit="${n}" data-kind="sni" title="${t("common.edit")}">${Icons.svg("edit", 15)}</button>
            <button class="btn btn-danger btn-sm" data-del="${n}" data-kind="sni" title="${t("common.delete")}">${Icons.svg("trash", 15)}</button>
          </td>
        </tr>
        <tr class="row-path"><td colspan="9">
          <div class="path-line"><span class="path-label">SNI</span>
          <code>${s.sni}</code></div>
        </td></tr>`;
    }).join("");

    const empty = !httpRows && !sniRows;

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("services", 20)} ${t("services.title")}</h1>
        <button class="btn btn-primary" id="add-svc">${Icons.svg("plus", 14)} ${t("services.add")}</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr>
          <th class="num-col">#</th>
          <th class="pw-col" title="${t("services.enabled")}"></th>
          <th>${t("services.type")}</th>
          <th>${t("services.name")}</th>
          <th>${t("reality.target")}</th>
          <th>${t("services.local_port")}</th>
          <th>${t("services.listen_port")}</th>
          <th>${t("services.bindings")}</th>
          <th></th>
        </tr></thead>
        <tbody>${httpRows}${sniRows}</tbody>
      </table></div>
      ${empty ? `<div class="log-empty">${t("services.empty")}</div>` : ""}
      </div>`;

    container.querySelector("#add-svc").onclick =
      () => serviceForm(ctx, domains, state.config, null, null, "http");

    container.querySelectorAll("[data-edit]").forEach(b => b.onclick = () => {
      const n = b.dataset.edit;
      if (b.dataset.kind === "sni") {
        serviceForm(ctx, domains, state.config, n, sniRules[n], "sni");
      } else {
        serviceForm(ctx, domains, state.config, n, services[n], "http");
      }
    });

    container.querySelectorAll("[data-raw]").forEach(b => b.onclick = () =>
      rawDialog(ctx, b.dataset.raw, b.dataset.rawKind));

    // Switching a service on or off.
    //
    // The row is updated from the SERVER's answer, not optimistically: if
    // nginx refuses the resulting config the panel must not show a state
    // the server is not actually in. The button is disabled while the
    // request is in flight so a double-click cannot queue two opposite
    // changes.
    container.querySelectorAll("[data-toggle]").forEach(b => b.onclick = async () => {
      if (b.disabled) return;
      const name = b.dataset.toggle;
      const sni = b.dataset.toggleKind === "sni";
      const want = b.getAttribute("aria-checked") !== "true";
      b.disabled = true;
      b.classList.add("busy");
      try {
        const url = (sni ? "/api/reality/services/" : "/api/services/")
          + encodeURIComponent(name) + "/toggle";
        const res = await api(url, { method: "POST", body: JSON.stringify({ enabled: want }) });
        applyToggleState(b, res.enabled);
        // The reload is reported separately from the save: the switch has
        // been stored either way, and hiding a config nginx rejected would
        // leave the operator believing traffic had moved when it had not.
        if (res.applied === false) {
          toast(t("services.apply_failed") + (res.apply_error ? ": " + res.apply_error : ""), "error");
        } else {
          toast(res.enabled ? t("services.enabled_ok") : t("services.disabled_ok"), "success");
        }
        navigate("services");
      } catch (e) {
        toast(e.message, "error");
        b.disabled = false;
        b.classList.remove("busy");
      }
    });

    container.querySelectorAll("[data-del]").forEach(b => b.onclick = () => {
      const n = b.dataset.del;
      const sni = b.dataset.kind === "sni";
      confirmDialog((sni ? t("reality.delete_confirm") : t("services.delete_confirm")) + " (" + n + ")",
        async () => {
          try {
            const url = sni
              ? "/api/reality/services/" + encodeURIComponent(n)
              : "/api/services/" + encodeURIComponent(n);
            await api(url, { method: "DELETE" });
            toast(t("services.deleted"), "success");
            navigate("services");
          } catch (e) { toast(e.message, "error"); }
        });
    });
  },
};

/* syncRowToggle mirrors a state change onto the row behind the dialog, so
   the list and the dialog can never disagree about whether a service is on.
   Scoped by BOTH name and kind: an HTTP service and an SNI rule are allowed
   to share a name. */
function syncRowToggle(name, kind, enabled, t) {
  const btn = document.querySelector(
    `#content [data-toggle="${cssEscape(name)}"][data-toggle-kind="${kind}"]`);
  if (!btn) return;
  applyToggleState(btn, enabled);
  const label = enabled ? t("services.disable") : t("services.enable");
  btn.setAttribute("aria-label", label);
  btn.setAttribute("data-tip", label);
  const row = btn.closest("tr");
  if (row) row.classList.toggle("row-off", !enabled);
}

/* A service name is operator-supplied, so it can carry characters that are
   meaningful in a selector. CSS.escape is not available on every browser
   the panel has to run on. */
function cssEscape(v) {
  if (window.CSS && CSS.escape) return CSS.escape(v);
  return String(v).replace(/["\\]/g, "\\$&");
}

/* applyToggleState paints a switch. Shared by the list and the edit dialog
   so the two representations of the same fact cannot drift apart. */
function applyToggleState(btn, enabled) {
  if (!btn) return;
  btn.setAttribute("aria-checked", enabled ? "true" : "false");
  btn.classList.toggle("on", !!enabled);
  btn.classList.toggle("off", !enabled);
  btn.classList.remove("busy");
  btn.disabled = false;
}

/* ── Add / edit ───────────────────────────────────────────────────────
   One dialog, two tabs. The tab picks WHICH kind of record is created, so
   each tab shows only its own fields and saves through its own endpoint.
   Editing locks the tab to the record's actual type: an HTTP service cannot
   become an SNI rule by switching a tab, they are different objects. */
function serviceForm(ctx, domains, config, editName, editRec, kind) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  const isEdit = !!editName;
  const rec = editRec || {};

  const domNames = Object.keys(domains);
  const bind = (rec.bindings && rec.bindings[0]) || {};
  const domOpts = domNames.map(d =>
    `<option value="${d}" ${bind.domain === d ? "selected" : ""}>${d}</option>`).join("");
  const ports = (config && config.listen_ports) || [443];
  const portOpts = ports.map(p =>
    `<option value="${p}" ${rec.listen_port === p ? "selected" : ""}>${p}</option>`).join("");

  // HTTP defaults; in edit mode every field starts from the stored value.
  const httpTarget = (kind === "http" && rec.target ? rec.target : "").trim() || "localhost";
  const httpPath = rec.path === "/" ? "" : (rec.path || "");

  // Bot shield. Off unless the record already has it, so adding a service
  // behaves exactly as it always did.
  const gateMode = (rec.gate || "").trim();
  const gateOn = gateMode === "js" || gateMode === "secret";

  // Enable/disable inside the dialog. Only in edit mode: a service that
  // does not exist yet cannot be switched, and offering the control would
  // imply the state is saved with the form when in fact it applies at once.
  const svcEnabled = !rec.disabled;

  // SNI defaults. The target is a free-text host exactly like the HTTP form,
  // plus one checkbox for the pass-through case, which is not a host at all.
  const sniTargetRaw = (kind === "sni" && rec.target ? rec.target : "").trim();
  const isPass = sniTargetRaw === PASSTHROUGH;
  const sniTarget = isPass ? "" : (sniTargetRaw || "localhost");

  const tabs = isEdit
    ? `<div class="tabs form-tabs"><button class="tab active" disabled>${kind === "sni" ? "SNI" : "HTTP"}</button></div>`
    : `<div class="tabs form-tabs" id="k-tabs">
         <button class="tab ${kind === "http" ? "active" : ""}" data-kind="http">${Icons.svg("globe", 13)} HTTP</button>
         <button class="tab ${kind === "sni" ? "active" : ""}" data-kind="sni">${Icons.svg("reality", 13)} SNI</button>
       </div>`;

  modal(isEdit ? `${t("common.edit")} — ${editName}` : t("services.add"), `
    ${tabs}
    <div class="form-error" id="s-err" hidden></div>

    <div class="field"><label>${t("services.name")}</label>
      <input id="s-name" value="${editName || ""}" ${isEdit ? "disabled" : ""} placeholder="myservice"></div>

    ${isEdit ? `<div class="power-row">
      <div class="power-text">
        <div class="power-title">${t("services.enabled")}${Icons.help(t("services.enabled_help"))}</div>
        <div class="power-sub" id="s-power-sub">${svcEnabled ? t("services.state_on") : t("services.state_off")}</div>
      </div>
      ${powerToggle(editName, kind, svcEnabled, t, Icons)}
    </div>` : ""}

    <div data-kind-body="http" ${kind === "http" ? "" : "hidden"}>
      <div class="field"><label>${t("services.subdomain")}</label>
        <input id="s-sub" value="${bind.subdomain || ""}" placeholder="app"></div>
      <div class="field"><label>${t("services.domain")}</label>
        <select id="s-dom">${domOpts}</select></div>
      <div class="field field-wide">
        <label>${t("reality.target")}${Icons.help(t("reality.target_hint"))}</label>
        <input id="s-target" dir="ltr" class="mono" value="${httpTarget}" placeholder="localhost">
      </div>
      <div class="field-row">
        <div class="field field-port"><label>${t("services.local_port")}</label>
          <input id="s-lport" type="number" inputmode="numeric" min="1" max="65535" value="${rec.local_port || 3000}"></div>
        <div class="field field-port"><label>${t("services.listen_port")}</label>
          <select id="s-liport">${portOpts}<option value="__new__">+ ${t("ports.add")}</option></select>
          <input id="s-liport-new" type="number" inputmode="numeric" min="1" max="65535" placeholder="443" hidden>
        </div>
      </div>
      <div class="field field-wide"><label>${t("services.path")}</label>
        <input id="s-path" dir="ltr" class="mono" value="${httpPath}" placeholder="/ (root)"></div>
      <label class="checkbox"><input type="checkbox" id="s-owned" ${(isEdit ? rec.path_owned : true) ? "checked" : ""}><span class="check-box"></span> <span>${t("services.path_owned")}</span></label>
      <label class="checkbox"><input type="checkbox" id="s-ssl" ${rec.ssl_backend ? "checked" : ""}><span class="check-box"></span> <span>${t("services.ssl_backend")}</span></label>

      <div class="field field-wide">
        <label>${t("services.allow_ips")}${Icons.help(t("services.allow_ips_help"))}</label>
        <input id="s-allow-ips" dir="ltr" class="mono"
               value="${(rec.allow_ips || []).join(", ")}"
               placeholder="${t("services.allow_ips_ph")}">
        <p class="tiny muted">${t("services.allow_ips_hint")}</p>
      </div>

      <label class="checkbox"><input type="checkbox" id="s-gate" ${gateOn ? "checked" : ""}><span class="check-box"></span> <span>${t("services.gate")}</span>${Icons.help(t("services.gate_help"))}</label>
      <div id="s-gate-opts" ${gateOn ? "" : "hidden"}>
        <div class="field field-wide">
          <label>${t("services.gate_mode")}${Icons.help(t("services.gate_mode_help"))}</label>
          <select id="s-gate-mode">
            <option value="js" ${gateMode !== "secret" ? "selected" : ""}>${t("services.gate_js")}</option>
            <option value="secret" ${gateMode === "secret" ? "selected" : ""}>${t("services.gate_secret")}</option>
          </select>
        </div>
        <div class="field field-wide" id="s-gate-key-wrap" ${gateMode === "secret" ? "" : "hidden"}>
          <label>${t("services.gate_key")}${Icons.help(t("services.gate_key_help"))}</label>
          <div class="input-row">
            <input id="s-gate-key" dir="ltr" class="mono" value="${gateMode === "secret" ? (rec.gate_secret || "") : ""}" placeholder="MyKey_2024">
            <button type="button" class="btn btn-ghost icon-btn" id="s-gate-gen"
                    data-tip="${t("services.gate_key_gen_hint")}"
                    aria-label="${t("services.gate_key_gen")}">${Icons.svg("dice", 15)}</button>
            <button type="button" class="btn btn-ghost icon-btn copy-btn" id="s-gate-copy"
                    data-copy-src="s-gate-key"
                    data-tip="${t("certs.copy")}"
                    aria-label="${t("certs.copy")}">${Icons.svg("copy", 15)}</button>
          </div>
        </div>

        <div class="gate-except">
          <div class="tiny">${t("services.gate_except")}</div>
          <div class="field field-wide">
            <label>${t("services.gate_allow_paths")}${Icons.help(t("services.gate_allow_paths_help"))}</label>
            <input id="s-gate-paths" dir="ltr" class="mono" value="${(rec.gate_allow_paths || []).join(", ")}" placeholder="/sitemap.xml, /robots.txt">
          </div>
          <div class="field field-wide">
            <label>${t("services.gate_allow_ips")}${Icons.help(t("services.gate_allow_ips_help"))}</label>
            <input id="s-gate-ips" dir="ltr" class="mono" value="${(rec.gate_allow_ips || []).join(", ")}" placeholder="10.0.0.0/24, 192.168.1.5">
          </div>
          <label class="checkbox"><input type="checkbox" id="s-gate-bots" ${rec.gate_allow_bots ? "checked" : ""}><span class="check-box"></span> <span>${t("services.gate_allow_bots")}</span>${Icons.help(t("services.gate_allow_bots_help"))}</label>
        </div>
      </div>
    </div>

    <div data-kind-body="sni" ${kind === "sni" ? "" : "hidden"}>
      <div class="field field-wide">
        <label>SNI${Icons.help(t("reality.sni_hint"))}</label>
        <input id="r-sni" dir="ltr" class="mono" value="${rec.sni || ""}" placeholder="*.epicgames.com">
      </div>
      <div class="field field-wide">
        <label>${t("reality.target")}${Icons.help(t("reality.target_hint"))}</label>
        <input id="r-target" dir="ltr" class="mono" value="${sniTarget}" placeholder="localhost" ${isPass ? "disabled" : ""}>
      </div>
      <label class="checkbox"><input type="checkbox" id="r-pass" ${isPass ? "checked" : ""}><span class="check-box"></span> <span>${t("reality.target_pass")}</span>${Icons.help(t("reality.target_pass_help"))}</label>
      <div class="field-row">
        <div class="field field-port"><label>${t("services.local_port")}</label>
          <input id="r-lp" type="number" inputmode="numeric" min="1" max="65535" value="${rec.local_port || 443}"></div>
        <div class="field field-port"><label>${t("reality.ports")}</label>
          <input id="r-p" type="number" inputmode="numeric" min="1" max="65535" value="${(rec.ports && rec.ports[0]) || 443}"></div>
      </div>
    </div>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: t("common.save"), class: "btn-primary", icon: "check", keepOpen: true, onClick: async () => {
        const err = document.getElementById("s-err");
        err.hidden = true;
        try {
          const active = currentKind(kind, isEdit);
          const name = document.getElementById("s-name").value.trim();
          if (!name) throw new Error(t("services.err_name"));

          if (active === "sni") {
            await saveSNI(ctx, name, isEdit, editName);
          } else {
            await saveHTTP(ctx, name, isEdit, editName);
          }
          window.closeModal();
          toast(isEdit ? t("settings.saved") : t("services.added"), "success");
          navigate("services");
        } catch (e) {
          err.textContent = e.message;
          err.hidden = false;
        }
      } }]);

  // Tab switching (create mode only).
  const tabBar = document.getElementById("k-tabs");
  if (tabBar) {
    tabBar.querySelectorAll(".tab").forEach(b => b.onclick = () => {
      tabBar.querySelectorAll(".tab").forEach(x => x.classList.remove("active"));
      b.classList.add("active");
      document.querySelectorAll("[data-kind-body]").forEach(p => {
        p.hidden = p.dataset.kindBody !== b.dataset.kind;
      });
    });
  }

  // Pass-through is not a host, so the host field is disabled while it is on.
  const pass = document.getElementById("r-pass");
  if (pass) {
    pass.onchange = () => {
      const host = document.getElementById("r-target");
      host.disabled = pass.checked;
      if (pass.checked) host.value = "";
      else if (!host.value) host.value = "localhost";
    };
  }

  // "+ add port" reveals a number field.
  // Bot shield: reveal the options only while it is on, and the key field
  // only in the mode that needs one.
  const gate = document.getElementById("s-gate");
  const gateOpts = document.getElementById("s-gate-opts");
  const gateModeSel = document.getElementById("s-gate-mode");
  const gateKeyWrap = document.getElementById("s-gate-key-wrap");
  if (gate && gateOpts) {
    gate.onchange = () => { gateOpts.hidden = !gate.checked; };
  }
  if (gateModeSel && gateKeyWrap) {
    gateModeSel.onchange = () => { gateKeyWrap.hidden = gateModeSel.value !== "secret"; };
  }

  // Generate a strong access key.
  //
  // Asked of the server rather than made in the browser: the key has to
  // satisfy exactly the validator that will guard it, and the panel is
  // routinely used over plain HTTP on a LAN address where the page is not
  // a secure context. 32 characters from a 64-symbol alphabet is 192 bits.
  const gen = document.getElementById("s-gate-gen");
  if (gen) {
    gen.onclick = async () => {
      if (gen.disabled) return;
      gen.disabled = true;
      gen.classList.add("busy");
      try {
        const r = await api("/api/services/gate-secret", { method: "POST", body: "{}" });
        const field = document.getElementById("s-gate-key");
        field.value = r.secret;
        // Show it: a key the operator cannot read is a key they cannot
        // give to anyone, and it is about to be saved in clear text anyway.
        field.type = "text";
        field.focus();
        field.select();
        gen.classList.add("done");
        setTimeout(() => gen.classList.remove("done"), 900);
      } catch (e) {
        toast(e.message, "error");
      }
      gen.classList.remove("busy");
      gen.disabled = false;
    };
  }

  // The dialog's own switch. It applies immediately, exactly like the one
  // in the list, and the list is re-rendered when the dialog closes — so
  // the two can never show different states.
  const dlgToggle = document.querySelector(".modal [data-toggle]");
  if (dlgToggle) {
    dlgToggle.onclick = async () => {
      if (dlgToggle.disabled) return;
      const want = dlgToggle.getAttribute("aria-checked") !== "true";
      dlgToggle.disabled = true;
      dlgToggle.classList.add("busy");
      try {
        const url = (kind === "sni" ? "/api/reality/services/" : "/api/services/")
          + encodeURIComponent(editName) + "/toggle";
        const res = await api(url, { method: "POST", body: JSON.stringify({ enabled: want }) });
        applyToggleState(dlgToggle, res.enabled);
        const sub = document.getElementById("s-power-sub");
        if (sub) sub.textContent = res.enabled ? t("services.state_on") : t("services.state_off");
        // Keep the row underneath in step. The dialog can be dismissed with
        // Cancel or the X, neither of which re-renders the page, so without
        // this the list would keep showing the old state until the next
        // navigation — two controls for one fact, disagreeing.
        syncRowToggle(editName, kind, res.enabled, t);
        if (res.applied === false) {
          toast(t("services.apply_failed") + (res.apply_error ? ": " + res.apply_error : ""), "error");
        }
      } catch (e) {
        toast(e.message, "error");
        dlgToggle.classList.remove("busy");
        dlgToggle.disabled = false;
      }
    };
  }

  if (window.ShahragWireCopy) window.ShahragWireCopy(document.querySelector(".modal"), t, toast);

  const liport = document.getElementById("s-liport");
  if (liport) {
    liport.onchange = () => {
      document.getElementById("s-liport-new").hidden = liport.value !== "__new__";
    };
  }
}

/* readGate turns the checkbox + mode into the two fields the API expects.
   Always returns an explicit gate value, including "off", so that UNticking
   the box really removes the protection instead of leaving it untouched. */
/* readAllowIPs reads the per-service address lock.
   Deliberately NOT inside readGate: the lock is independent of the bot
   shield, and readGate returns early when the shield is off — which would
   have silently discarded the lock every time the shield was disabled. */
function readAllowIPs(t) {
  const el = document.getElementById("s-allow-ips");
  if (!el) return [];
  const ips = (el.value || "").split(",").map(v => v.trim()).filter(Boolean);
  for (const ip of ips) {
    // Loose shape check only; the server does the authoritative parse.
    if (!/^[0-9a-fA-F:.]+(\/[0-9]{1,3})?$/.test(ip)) {
      throw new Error(t("services.err_gate_ip").replace("%s", ip));
    }
  }
  return ips;
}

function readGate(t) {
  const on = document.getElementById("s-gate");
  if (!on || !on.checked) {
    // Send the exception lists as empty too, so turning the shield off
    // really clears them instead of leaving them to reappear later.
    return { gate: "off", gate_secret: "",
             gate_allow_paths: [], gate_allow_ips: [], gate_allow_bots: false };
  }

  const list = id => (document.getElementById(id).value || "")
    .split(",").map(v => v.trim()).filter(Boolean);

  const paths = list("s-gate-paths");
  for (const p of paths) {
    if (/[$'"\s{};]/.test(p)) throw new Error(t("services.err_gate_path").replace("%s", p));
  }
  const ips = list("s-gate-ips");
  for (const ip of ips) {
    // Loose shape check only; the server does the authoritative parse.
    if (!/^[0-9a-fA-F:.]+(\/[0-9]{1,3})?$/.test(ip)) {
      throw new Error(t("services.err_gate_ip").replace("%s", ip));
    }
  }
  const extra = {
    gate_allow_paths: paths,
    gate_allow_ips: ips,
    gate_allow_bots: document.getElementById("s-gate-bots").checked,
  };

  const mode = document.getElementById("s-gate-mode").value === "secret" ? "secret" : "js";
  if (mode !== "secret") return Object.assign({ gate: "js", gate_secret: "" }, extra);
  const key = (document.getElementById("s-gate-key").value || "").trim();
  // Fail here rather than letting the server reject it, so the message lands
  // in the form next to the field instead of only in a toast.
  if (!/^[A-Za-z0-9_-]{4,64}$/.test(key)) throw new Error(t("services.err_gate_key"));
  return Object.assign({ gate: "secret", gate_secret: key }, extra);
}

function currentKind(fallback, isEdit) {
  if (isEdit) return fallback;
  const active = document.querySelector("#k-tabs .tab.active");
  return active ? active.dataset.kind : fallback;
}

async function saveHTTP(ctx, name, isEdit, editName) {
  const { t, api } = ctx;
  const sub = document.getElementById("s-sub").value.trim();
  const dom = document.getElementById("s-dom").value;
  if (!dom) throw new Error(t("services.no_domains"));
  if (!sub) throw new Error(t("services.err_subdomain"));

  let liport = document.getElementById("s-liport").value;
  if (liport === "__new__") {
    const np = +document.getElementById("s-liport-new").value;
    if (!(np >= 1 && np <= 65535)) throw new Error(t("services.err_listen_port"));
    await api("/api/ports", { method: "POST", body: JSON.stringify({ port: np }) });
    liport = np;
  }
  const body = {
    name, subdomain: sub, domain: dom,
    target: document.getElementById("s-target").value.trim(),
    local_port: +document.getElementById("s-lport").value,
    listen_port: +liport,
    path: document.getElementById("s-path").value.trim() || "/",
    path_owned: document.getElementById("s-owned").checked,
    ssl_backend: document.getElementById("s-ssl").checked,
  };
  Object.assign(body, readGate(t));
  body.allow_ips = readAllowIPs(t);
  if (!(body.local_port >= 1 && body.local_port <= 65535)) throw new Error(t("services.err_local_port"));

  if (isEdit) {
    await api("/api/services/" + encodeURIComponent(editName), {
      method: "PUT",
      body: JSON.stringify({
        target: body.target, local_port: body.local_port,
        listen_port: body.listen_port, path: body.path,
        path_owned: body.path_owned, ssl_backend: body.ssl_backend,
        gate: body.gate, gate_secret: body.gate_secret,
        gate_allow_paths: body.gate_allow_paths,
        gate_allow_ips: body.gate_allow_ips,
        gate_allow_bots: body.gate_allow_bots,
        allow_ips: body.allow_ips,
      }),
    });
    await api("/api/services/" + encodeURIComponent(editName) + "/bindings", {
      method: "PUT",
      body: JSON.stringify({ bindings: [{ domain: dom, subdomain: sub }] }),
    });
  } else {
    await api("/api/services", { method: "POST", body: JSON.stringify(body) });
  }
}

async function saveSNI(ctx, name, isEdit, editName) {
  const { t, api } = ctx;
  const sni = document.getElementById("r-sni").value.trim();
  if (!sni) throw new Error(t("reality.err_sni"));

  // The target is typed, not picked from a list: the panel decides whether it
  // is local or remote from the value itself, exactly like an HTTP service.
  let target = document.getElementById("r-target").value.trim();
  if (document.getElementById("r-pass").checked) {
    target = PASSTHROUGH;
  } else if (isLocal(target)) {
    target = "";               // stored compactly; means 127.0.0.1
  }

  const body = {
    sni, target,
    local_port: +document.getElementById("r-lp").value,
    ports: [+document.getElementById("r-p").value],
  };
  if (!(body.local_port >= 1 && body.local_port <= 65535)) throw new Error(t("services.err_local_port"));

  if (isEdit) {
    await api("/api/reality/services/" + encodeURIComponent(editName),
      { method: "PUT", body: JSON.stringify(body) });
  } else {
    await api("/api/reality/services",
      { method: "POST", body: JSON.stringify({ name, ...body }) });
  }
}

/* ── Raw view / edit for ONE record ───────────────────────────────────
   Shows the record's JSON and the nginx it generates, side by side in two
   tabs. Saving is transactional on the server: the file is snapshotted,
   nginx validates it, and a rejected edit is rolled back. */
async function rawDialog(ctx, name, kind) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  const base = kind === "sni"
    ? "/api/reality/services/" + encodeURIComponent(name) + "/raw"
    : "/api/services/" + encodeURIComponent(name) + "/raw";

  let data;
  try {
    data = await api(base);
  } catch (e) {
    toast(e.message, "error");
    return;
  }

  const noNginx = !data.nginx || !data.blocks;

  modal(`${t("services.raw")} — ${name}`, `
    <div class="tabs form-tabs" id="raw-tabs">
      <button class="tab active" data-raw-tab="json">${Icons.svg("settings", 13)} JSON</button>
      <button class="tab" data-raw-tab="nginx">${Icons.svg("zap", 13)} nginx</button>
    </div>
    <div class="form-error" id="raw-err" hidden></div>

    <div data-raw-body="json">
      <p class="hint"><code>config.json</code>${Icons.help(t("services.raw_json_hint"))}</p>
      <textarea id="raw-json" class="code-editor" spellcheck="false" dir="ltr" wrap="off">${escapeHTML(data.json || "")}</textarea>
    </div>

    <div data-raw-body="nginx" hidden>
      ${noNginx
        ? `<p class="muted">${t("services.raw_none")}</p>`
        : `<p class="hint"><code>${data.file}</code>${Icons.help(t("services.raw_nginx_hint"))}</p>
           <textarea id="raw-nginx" class="code-editor" spellcheck="false" dir="ltr" wrap="off">${escapeHTML(data.nginx)}</textarea>`}
    </div>

    <label class="checkbox" style="margin-top:10px">
      <input type="checkbox" id="raw-apply" checked><span class="check-box"></span>
      <span>${t("files.apply_now")}</span>
    </label>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: t("common.save"), class: "btn-primary", icon: "check", keepOpen: true, onClick: async () => {
        const err = document.getElementById("raw-err");
        err.hidden = true;
        const activeTab = document.querySelector("#raw-tabs .tab.active").dataset.rawTab;
        const apply = document.getElementById("raw-apply").checked;
        const payload = { reload: apply };
        if (activeTab === "json") {
          payload.json = document.getElementById("raw-json").value;
        } else {
          const el = document.getElementById("raw-nginx");
          if (!el) { err.textContent = t("services.raw_none"); err.hidden = false; return; }
          payload.nginx = el.value;
        }
        try {
          const res = await api(base, { method: "PUT", body: JSON.stringify(payload) });
          if (res.ok) {
            window.closeModal();
            toast(t("settings.saved"), "success");
            navigate("services");
          } else {
            err.innerHTML = `${res.detail || t("files.rejected")}` +
              (res.stderr ? `<pre class="log-view" style="margin-top:8px">${escapeHTML(res.stderr)}</pre>` : "");
            err.hidden = false;
          }
        } catch (e) {
          err.textContent = e.message;
          err.hidden = false;
        }
      } }], true);

  const tabBar = document.getElementById("raw-tabs");
  tabBar.querySelectorAll(".tab").forEach(b => b.onclick = () => {
    tabBar.querySelectorAll(".tab").forEach(x => x.classList.remove("active"));
    b.classList.add("active");
    document.querySelectorAll("[data-raw-body]").forEach(p => {
      p.hidden = p.dataset.rawBody !== b.dataset.rawTab;
    });
  });

  // Tab indents inside the editors instead of moving focus.
  ["raw-json", "raw-nginx"].forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    el.addEventListener("keydown", (e) => {
      if (e.key !== "Tab") return;
      e.preventDefault();
      const s = el.selectionStart, en = el.selectionEnd;
      el.value = el.value.slice(0, s) + "    " + el.value.slice(en);
      el.selectionStart = el.selectionEnd = s + 4;
    });
  });
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

})();
