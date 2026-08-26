/* Services page — mirrors the CLI panel's service flow exactly:
   name → subdomain → domain (must exist) → local port → listen port
   (choose or add) → path (optional, root default) → path_owned
   (default yes) → ssl_backend (default no). */
window.Pages = window.Pages || {};
window.Pages.services = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog, toast, navigate } = ctx;
    const [services, domains] = await Promise.all([api("/api/services"), api("/api/domains")]);
    // The path gets its own full-width row UNDER the rest of the record.
    // Inside a narrow column a long path is unreadable no matter how it
    // wraps, so it is given the whole width instead.
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("services",20)} ${t("services.title")}</h1>
        <button class="btn btn-primary" id="add-svc">${Icons.svg("plus",14)} ${t("services.add")}</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr><th>${t("services.name")}</th><th>${t("reality.target")}</th><th>${t("services.local_port")}</th><th>${t("services.listen_port")}</th><th>${t("services.bindings")}</th><th></th></tr></thead>
        <tbody>${Object.keys(services).map(n=>{const s=services[n]; return `
          <tr class="row-main"><td><strong>${n}</strong> ${n===state.config?.shahrag?.panel?.service_name?'<span class="badge badge-info">Panel</span>':''}</td>
          <td>${svcTargetBadge(s.target, t)}</td>
          <td class="num">${s.local_port}</td><td class="num">${s.listen_port}</td>
          <td>${(s.bindings||[]).map(b=>`<span class="badge badge-neutral">${b.subdomain?b.subdomain+".":""}${b.domain}</span>`).join(" ")}</td>
          <td class="row-actions">
            <button class="btn btn-sm btn-edit" data-edit="${n}" title="${t("common.edit")}">${Icons.svg("edit",13)}</button>
            <button class="btn btn-danger btn-sm" data-del="${n}" title="${t("common.delete")}">${Icons.svg("trash",13)}</button>
          </td></tr>
          <tr class="row-path"><td colspan="6">
            <div class="path-line"><span class="path-label">${t("services.path")}</span>
            <code>/${s.path==="/"?"":s.path}</code></div>
          </td></tr>`}).join("")}
        </tbody></table></div></div>`;
    container.querySelector("#add-svc").onclick = ()=>serviceForm(ctx, domains, state.config);
    container.querySelectorAll("[data-edit]").forEach(b=>b.onclick=()=>{
      const n = b.dataset.edit;
      serviceForm(ctx, domains, state.config, n, services[n]);
    });
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=()=>{
      confirmDialog(t("services.delete_confirm")+" ("+b.dataset.del+")", async()=>{
        try {
          await api("/api/services/"+encodeURIComponent(b.dataset.del),{method:"DELETE"});
          toast(t("services.deleted"),"success");
          navigate("services");
        } catch(e) { toast(e.message,"error"); }
      });
    });
  }
};

/* An empty / localhost / 127.0.0.1 target means "a backend on this server",
   which is what almost every service uses; anything else is shown verbatim
   so an off-box upstream is obvious at a glance. */
function svcTargetBadge(target, t) {
  const v = (target || "").trim();
  if (!v || v === "localhost" || v === "127.0.0.1") {
    return `<span class="badge badge-neutral">${t("reality.target_local")}</span>`;
  }
  return `<span class="badge badge-success mono">${v}</span>`;
}

/* One form for both create and edit. In edit mode the name is fixed (it is
   the record's key) and every field starts from the stored value. */
function serviceForm(ctx, domains, config, editName, editSvc) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  const domNames = Object.keys(domains);
  if (domNames.length === 0) {
    toast(t("services.no_domains"), "error");
    return;
  }
  const isEdit = !!editName;
  const svc = editSvc || {};
  const bind = (svc.bindings && svc.bindings[0]) || {};
  const curTarget = (svc.target || "").trim() || "localhost";
  const curPath = svc.path === "/" ? "" : (svc.path || "");

  const domOpts = domNames.map(d=>
    `<option value="${d}" ${bind.domain===d?"selected":""}>${d}</option>`).join("");
  const ports = (config && config.listen_ports) || [443];
  const portOpts = ports.map(p=>
    `<option value="${p}" ${svc.listen_port===p?"selected":""}>${p}</option>`).join("");

  modal(isEdit ? `${t("common.edit")} — ${editName}` : t("services.add"), `
    <div class="form-error" id="s-err" hidden></div>
    <div class="field"><label>${t("services.name")}</label><input id="s-name" value="${editName||""}" ${isEdit?"disabled":""} placeholder="sanei"></div>
    <div class="field"><label>${t("services.subdomain")}</label><input id="s-sub" value="${bind.subdomain||""}" placeholder="app"></div>
    <div class="field"><label>${t("services.domain")}</label><select id="s-dom">${domOpts}</select></div>
    <div class="field field-wide">
      <label>${t("reality.target")}</label>
      <input id="s-target" dir="ltr" class="mono" value="${curTarget}" placeholder="localhost">
      <span class="hint">${t("reality.target_hint")}</span>
    </div>
    <div class="field-row">
      <div class="field field-port"><label>${t("services.local_port")}</label><input id="s-lport" type="number" inputmode="numeric" min="1" max="65535" value="${svc.local_port||3000}"></div>
      <div class="field field-port"><label>${t("services.listen_port")}</label>
        <select id="s-liport">${portOpts}<option value="__new__">+ ${t("ports.add")}</option></select>
        <input id="s-liport-new" type="number" inputmode="numeric" min="1" max="65535" placeholder="443" hidden>
      </div>
    </div>
    <div class="field field-wide"><label>${t("services.path")}</label><input id="s-path" dir="ltr" class="mono" value="${curPath}" placeholder="/ (root)"></div>
    <label class="checkbox"><input type="checkbox" id="s-owned" ${(isEdit? svc.path_owned : true)?"checked":""}><span class="check-box"></span> <span>${t("services.path_owned")}</span></label>
    <label class="checkbox"><input type="checkbox" id="s-ssl" ${svc.ssl_backend?"checked":""}><span class="check-box"></span> <span>${t("services.ssl_backend")}</span></label>`,
    [{label:t("common.cancel"),class:"btn-ghost"},
     {label:t("common.save"),class:"btn-primary",icon:"check",keepOpen:true,onClick:async()=>{
       const err = document.getElementById("s-err");
       err.hidden = true;
       try {
         const name = document.getElementById("s-name").value.trim();
         const sub = document.getElementById("s-sub").value.trim();
         const dom = document.getElementById("s-dom").value;
         if (!name) throw new Error(t("services.err_name"));
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
         if (!(body.local_port >= 1 && body.local_port <= 65535)) throw new Error(t("services.err_local_port"));
         if (isEdit) {
           // The name is the record key, so editing updates the fields and
           // replaces the binding rather than creating a second service.
           await api("/api/services/" + encodeURIComponent(editName), {
             method: "PUT",
             body: JSON.stringify({
               target: body.target, local_port: body.local_port,
               listen_port: body.listen_port, path: body.path,
               path_owned: body.path_owned, ssl_backend: body.ssl_backend,
             }),
           });
           await api("/api/services/" + encodeURIComponent(editName) + "/bindings", {
             method: "PUT",
             body: JSON.stringify({ bindings: [{ domain: dom, subdomain: sub }] }),
           });
         } else {
           await api("/api/services", { method: "POST", body: JSON.stringify(body) });
         }
         window.closeModal();
         toast(isEdit ? t("settings.saved") : t("services.added"), "success");
         navigate("services");
       } catch(e) {
         err.textContent = e.message;
         err.hidden = false;
       }
     }}]);
}
