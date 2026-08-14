/* Services page */
window.Pages = window.Pages || {};
window.Pages.services = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal, confirmDialog } = ctx;
    const [services, domains] = await Promise.all([api("/api/services"), api("/api/domains")]);
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("services",20)} ${t("services.title")}</h1>
        <button class="btn btn-primary" id="add-svc">${Icons.svg("plus",14)} ${t("services.add")}</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr><th>${t("services.name")}</th><th>${t("services.local_port")}</th><th>${t("services.listen_port")}</th><th>${t("services.path")}</th><th>${t("services.bindings")}</th><th></th></tr></thead>
        <tbody>${Object.entries(services).map(([n,s])=>`
          <tr><td><strong>${n}</strong></td><td>${s.local_port}</td><td>${s.listen_port}</td>
          <td><code>/${s.path==="/"?"":s.path}</code></td>
          <td>${(s.bindings||[]).map(b=>`<span class="badge badge-neutral">${b.subdomain?b.subdomain+".":""}${b.domain}</span>`).join(" ")}</td>
          <td class="row-actions">
            <button class="btn btn-danger btn-sm" data-del="${n}">${Icons.svg("trash",13)}</button>
          </td></tr>`).join("")}
        </tbody></table></div></div>`;
    container.querySelector("#add-svc").onclick = ()=>serviceForm(ctx, domains);
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=()=>{
      confirmDialog(`Delete ${b.dataset.del}?`, async()=>{
        await api("/api/services/"+encodeURIComponent(b.dataset.del),{method:"DELETE"});
        location.reload();
      });
    });
  }
};
function serviceForm(ctx, domains) {
  const { t, Icons, modal } = ctx;
  const domOpts = Object.keys(domains).map(d=>`<option value="${d}">${d}</option>`).join("");
  modal(t("services.add"), `
    <div class="field"><label>${t("services.name")}</label><input id="s-name"></div>
    <div class="field"><label>${t("services.subdomain")}</label><input id="s-sub" placeholder="app"></div>
    <div class="field"><label>${t("services.domain")}</label><select id="s-dom">${domOpts}</select></div>
    <div class="field-row">
      <div class="field"><label>${t("services.local_port")}</label><input id="s-lport" type="number" value="3000"></div>
      <div class="field"><label>${t("services.listen_port")}</label><input id="s-liport" type="number" value="443"></div>
    </div>
    <div class="field"><label>${t("services.path")}</label><input id="s-path" placeholder="/ (root)"></div>
    <label class="checkbox"><input type="checkbox" id="s-owned"> ${t("services.path_owned")}</label>
    <label class="checkbox"><input type="checkbox" id="s-ssl"> ${t("services.ssl_backend")}</label>`,
    [{label:t("common.cancel"),class:"btn-ghost"},
     {label:t("common.save"),class:"btn-primary",icon:"check",onClick:async()=>{
       const body={
         name:document.getElementById("s-name").value.trim(),
         subdomain:document.getElementById("s-sub").value.trim(),
         domain:document.getElementById("s-dom").value,
         local_port:+document.getElementById("s-lport").value,
         listen_port:+document.getElementById("s-liport").value,
         path:document.getElementById("s-path").value.trim()||"/",
         path_owned:document.getElementById("s-owned").checked,
         ssl_backend:document.getElementById("s-ssl").checked};
       await api("/api/services",{method:"POST",body:JSON.stringify(body)});
       location.reload();
     }}]);
}
