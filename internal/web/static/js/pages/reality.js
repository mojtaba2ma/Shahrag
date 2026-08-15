/* Reality page — mirrors the CLI panel reality flow. */
window.Pages = window.Pages || {};
window.Pages.reality = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal, confirmDialog, toast, navigate } = ctx;
    const r = await api("/api/reality");
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("reality",20)} ${t("reality.title")}</h1>
        <button class="btn btn-primary" id="r-add">${Icons.svg("plus",14)} ${t("reality.add_service")}</button>
      </div>
      <div class="card">
        <label class="switch"><input type="checkbox" id="r-en" ${r.enabled?"checked":""}> ${t("reality.enabled")}</label>
        <div class="field"><label>${t("reality.http_port")}</label><input id="r-port" type="number" min="1" max="65535" value="${r.http_port||6038}"></div>
        <div class="btn-row"><button class="btn btn-primary" id="r-save">${Icons.svg("check",14)} Save</button></div>
      </div>
      <div class="card"><div class="table-wrap"><table class="data-table">
        <thead><tr><th>${t("services.name")}</th><th>SNI</th><th>${t("services.local_port")}</th><th>${t("ports.title")}</th><th></th></tr></thead>
          <tbody>${Object.entries(r.services||{}).map(([n,s])=>`
            <tr><td><strong>${n}</strong></td><td>${s.sni}</td><td>:${s.local_port}</td>
            <td>${(s.ports||[]).join(", ")}</td>
            <td><button class="btn btn-danger btn-sm" data-del="${n}">${Icons.svg("trash",13)}</button></td></tr>`).join("")}
          </tbody></table></div></div>`;
    document.getElementById("r-save").onclick = async()=>{
      try {
        await api("/api/reality",{method:"PUT",body:JSON.stringify({
          enabled:document.getElementById("r-en").checked,
          http_port:+document.getElementById("r-port").value})});
        toast(t("settings.saved"),"success");
        navigate("reality");
      } catch(e) { toast(e.message,"error"); }
    };
    document.getElementById("r-add").onclick = ()=>realityForm(ctx);
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=()=>{
      confirmDialog(t("reality.delete_confirm")+" ("+b.dataset.del+")", async()=>{
        try {
          await api("/api/reality/services/"+encodeURIComponent(b.dataset.del),{method:"DELETE"});
          toast(t("services.deleted"),"success");
          navigate("reality");
        } catch(e) { toast(e.message,"error"); }
      });
    });
  }
};
function realityForm(ctx){
  const {t,Icons,modal,api,toast,navigate}=ctx;
  modal(t("reality.add_service"),`
    <div class="form-error" id="r-err" hidden></div>
    <div class="field"><label>${t("services.name")}</label><input id="r-n"></div>
    <div class="field"><label>SNI</label><input id="r-sni" placeholder="dl.google.com"></div>
    <div class="field-row"><div class="field"><label>${t("services.local_port")}</label><input id="r-lp" type="number" value="443"></div>
    <div class="field"><label>Port</label><input id="r-p" type="number" value="443"></div></div>`,
    [{label:t("common.cancel"),class:"btn-ghost"},
     {label:t("common.save"),class:"btn-primary",icon:"check",keepOpen:true,onClick:async()=>{
       const err=document.getElementById("r-err");
       err.hidden=true;
       try {
         const name=document.getElementById("r-n").value.trim();
         const sni=document.getElementById("r-sni").value.trim();
         if(!name) throw new Error(t("services.err_name"));
         if(!sni) throw new Error(t("reality.err_sni"));
         await api("/api/reality/services",{method:"POST",body:JSON.stringify({
           name, sni,
           local_port:+document.getElementById("r-lp").value,
           ports:[+document.getElementById("r-p").value]})});
         window.closeModal();
         toast(t("reality.added"),"success");
         navigate("reality");
       } catch(e) {
         err.textContent=e.message;
         err.hidden=false;
       }
     }}]);
}
