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
        <thead><tr><th>${t("domains.name")}</th><th></th></tr></thead>
        <tbody>${Object.entries(domains).map(([n,d])=>`
          <tr class="row-main"><td><strong>${n}</strong></td>
          <td class="row-actions">
            <button class="btn btn-sm btn-edit" data-edit="${n}" title="${t("common.edit")}">${Icons.svg("edit",13)}</button>
            <button class="btn btn-danger btn-sm" data-del="${n}" title="${t("common.delete")}">${Icons.svg("trash",13)}</button>
          </td></tr>
          <tr class="row-path"><td colspan="2">
            <div class="path-line"><span class="path-label">${t("domains.cert")}</span>
            <code>${d.cert||"—"}</code></div>
            <div class="path-line" style="margin-top:6px"><span class="path-label">${t("domains.key")}</span>
            <code>${d.key||"—"}</code></div>
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
  const { t, Icons, modal, api, toast, navigate } = ctx;
  const isEdit = !!name;
  modal(isEdit?t("domains.edit"):t("domains.add"), `
    <div class="form-error" id="d-err" hidden></div>
    <div class="field"><label>${t("domains.name")}</label>
      <input id="d-name" value="${name||""}" ${isEdit?"disabled":""} placeholder="example.com"></div>
    <div class="field field-wide"><label>${t("domains.cert")}</label>
      <input id="d-cert" dir="ltr" class="mono" value="${d.cert||""}" placeholder="/etc/letsencrypt/live/example.com/fullchain.pem"></div>
    <div class="field field-wide"><label>${t("domains.key")}</label>
      <input id="d-key" dir="ltr" class="mono" value="${d.key||""}" placeholder="/etc/letsencrypt/live/example.com/privkey.pem"></div>`,
    [{label:t("common.cancel"),class:"btn-ghost"},
     {label:t("common.save"),class:"btn-primary",icon:"check",keepOpen:true,onClick:async()=>{
       const err = document.getElementById("d-err");
       err.hidden = true;
       try {
         const body={
           name:document.getElementById("d-name").value.trim(),
           cert:document.getElementById("d-cert").value.trim().replace(/\/+$/g,""),
           key:document.getElementById("d-key").value.trim().replace(/\/+$/g,"")};
         if (!body.name) throw new Error(t("domains.err_name"));
         if(isEdit) await api("/api/domains/"+encodeURIComponent(name),{method:"PUT",body:JSON.stringify({cert:body.cert,key:body.key})});
         else await api("/api/domains",{method:"POST",body:JSON.stringify(body)});
         window.closeModal();
         toast(isEdit ? t("domains.saved") : t("domains.added"), "success");
         navigate("domains");
       } catch(e) {
         err.textContent = e.message;
         err.hidden = false;
       }
     }}]);
}
