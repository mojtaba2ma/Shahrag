/* Services page — mirrors the CLI panel's service flow exactly:
   name → subdomain → domain (must exist) → local port → listen port
   (choose or add) → path (optional, root default) → path_owned
   (default yes) → ssl_backend (default no). */
window.Pages = window.Pages || {};
window.Pages.services = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog, toast, navigate } = ctx;
    const [services, domains] = await Promise.all([api("/api/services"), api("/api/domains")]);
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("services",20)} ${t("services.title")}</h1>
        <button class="btn btn-primary" id="add-svc">${Icons.svg("plus",14)} ${t("services.add")}</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr><th>${t("services.name")}</th><th>${t("services.local_port")}</th><th>${t("services.listen_port")}</th><th>${t("services.path")}</th><th>${t("services.bindings")}</th><th></th></tr></thead>
        <tbody>${Object.keys(services).map(n=>{const s=services[n]; return `
          <tr><td><strong>${n}</strong> ${n===state.config?.shahrag?.panel?.service_name?'<span class="badge badge-info">Panel</span>':''}</td>
          <td>${s.local_port}</td><td>${s.listen_port}</td>
          <td><code>/${s.path==="/"?"":s.path}</code></td>
          <td>${(s.bindings||[]).map(b=>`<span class="badge badge-neutral">${b.subdomain?b.subdomain+".":""}${b.domain}</span>`).join(" ")}</td>
          <td class="row-actions">
            <button class="btn btn-danger btn-sm" data-del="${n}">${Icons.svg("trash",13)}</button>
          </td></tr>`}).join("")}
        </tbody></table></div></div>`;
    container.querySelector("#add-svc").onclick = ()=>serviceForm(ctx, domains, state.config);
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

function serviceForm(ctx, domains, config) {
  const { t, Icons, modal, api, toast, navigate } = ctx;
  const domNames = Object.keys(domains);
  if (domNames.length === 0) {
    toast(t("services.no_domains"), "error");
    return;
  }
  const domOpts = domNames.map(d=>`<option value="${d}">${d}</option>`).join("");
  const ports = (config && config.listen_ports) || [443];
  const portOpts = ports.map(p=>`<option value="${p}">${p}</option>`).join("");

  modal(t("services.add"), `
    <div class="form-error" id="s-err" hidden></div>
    <div class="field"><label>${t("services.name")}</label><input id="s-name" placeholder="sanei"></div>
    <div class="field"><label>${t("services.subdomain")}</label><input id="s-sub" placeholder="app"></div>
    <div class="field"><label>${t("services.domain")}</label><select id="s-dom">${domOpts}</select></div>
    <div class="field-row">
      <div class="field"><label>${t("services.local_port")}</label><input id="s-lport" type="number" min="1" max="65535" value="3000"></div>
      <div class="field"><label>${t("services.listen_port")}</label>
        <select id="s-liport">${portOpts}<option value="__new__">+ ${t("ports.add")}</option></select>
        <input id="s-liport-new" type="number" min="1" max="65535" placeholder="443" hidden>
      </div>
    </div>
    <div class="field"><label>${t("services.path")}</label><input id="s-path" placeholder="/ (root)"></div>
    <label class="checkbox"><input type="checkbox" id="s-owned" checked> ${t("services.path_owned")}</label>
    <label class="checkbox"><input type="checkbox" id="s-ssl"> ${t("services.ssl_backend")}</label>`,
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
           local_port: +document.getElementById("s-lport").value,
           listen_port: +liport,
           path: document.getElementById("s-path").value.trim() || "/",
           path_owned: document.getElementById("s-owned").checked,
           ssl_backend: document.getElementById("s-ssl").checked,
         };
         if (!(body.local_port >= 1 && body.local_port <= 65535)) throw new Error(t("services.err_local_port"));
         await api("/api/services", { method: "POST", body: JSON.stringify(body) });
         window.closeModal();
         toast(t("services.added"), "success");
         navigate("services");
       } catch(e) {
         err.textContent = e.message;
         err.hidden = false;
       }
     }}]);
}
