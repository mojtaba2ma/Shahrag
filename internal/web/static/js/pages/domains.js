/* Domains page */
window.Pages = window.Pages || {};
window.Pages.domains = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal, confirmDialog } = ctx;
    const domains = await api("/api/domains");
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("domains",20)} ${t("domains.title")}</h1>
        <button class="btn btn-primary" id="add-btn">${Icons.svg("plus",14)} ${t("domains.add")}</button>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr><th>${t("domains.name")}</th><th>${t("domains.cert")}</th><th></th></tr></thead>
        <tbody>${Object.entries(domains).map(([n,d])=>`
          <tr><td><strong>${n}</strong></td>
          <td><code>${d.cert||"—"}</code></td>
          <td class="row-actions">
            <button class="btn btn-ghost btn-sm" data-edit="${n}">${Icons.svg("edit",13)}</button>
            <button class="btn btn-danger btn-sm" data-del="${n}">${Icons.svg("trash",13)}</button>
          </td></tr>`).join("")}
        </tbody></table></div></div>`;
    container.querySelector("#add-btn").onclick = ()=>domainForm(ctx, null, {});
    container.querySelectorAll("[data-edit]").forEach(b=>b.onclick=()=>{
      const n=b.dataset.edit; domainForm(ctx, n, domains[n]);
    });
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=()=>{
      confirmDialog(`Delete ${b.dataset.del}?`, async ()=>{
        await api("/api/domains/"+encodeURIComponent(b.dataset.del), {method:"DELETE"});
        window.Pages.domains.render(container, state, ctx);
      });
    });
  }
};
function domainForm(ctx, name, d) {
  const { t, Icons, modal } = ctx;
  const isEdit = !!name;
  modal(isEdit?t("domains.edit"):t("domains.add"), `
    <div class="field"><label>${t("domains.name")}</label>
      <input id="d-name" value="${name||""}" ${isEdit?"disabled":""} placeholder="example.com"></div>
    <div class="field"><label>${t("domains.cert")}</label>
      <input id="d-cert" value="${d.cert||""}" placeholder="/etc/letsencrypt/.../fullchain.pem"></div>
    <div class="field"><label>${t("domains.key")}</label>
      <input id="d-key" value="${d.key||""}" placeholder="/etc/letsencrypt/.../privkey.pem"></div>`,
    [{label:t("common.cancel"),class:"btn-ghost"},
     {label:t("common.save"),class:"btn-primary",icon:"check",onClick:async()=>{
       const body={
         name:document.getElementById("d-name").value.trim(),
         cert:document.getElementById("d-cert").value.trim(),
         key:document.getElementById("d-key").value.trim()};
       if(isEdit) await api("/api/domains/"+encodeURIComponent(name),{method:"PUT",body:JSON.stringify({cert:body.cert,key:body.key})});
       else await api("/api/domains",{method:"POST",body:JSON.stringify(body)});
       location.reload();
     }}]);
}
