/* Ports page — mirrors the CLI panel ports flow. */
window.Pages = window.Pages || {};
window.Pages.ports = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal, confirmDialog, toast, navigate } = ctx;
    const ports = await api("/api/ports");
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("ports",20)} ${t("ports.title")}</h1>
        <button class="btn btn-primary" id="add-port">${Icons.svg("plus",14)} ${t("ports.add")}</button>
      </div>
      <div class="card"><div class="card-list">
        ${ports.map(p=>`<div class="list-row">
          <span class="list-val num">:${p.port}</span>
          <span class="badge ${p.is_http?"badge-neutral":"badge-info"}">${p.is_http?"HTTP":"HTTPS"}</span>
          <span class="muted">${(p.used_by||[]).join(", ")||""}</span>
          <button class="btn btn-danger btn-sm" data-del="${p.port}" ${p.port===80||p.port===443?"disabled":""}>${Icons.svg("trash",13)}</button>
        </div>`).join("")}
      </div></div>`;
    // A real modal instead of the browser's prompt(): the native dialog
    // ignores the panel's theme entirely and cannot validate the value.
    container.querySelector("#add-port").onclick = ()=>{
      modal(t("ports.add"), `
        <div class="form-error" id="p-err" hidden></div>
        <div class="field field-port">
          <label>${t("ports.port")}</label>
          <input id="p-val" type="number" inputmode="numeric" min="1" max="65535" placeholder="8443">
        </div>`,
        [{label:t("common.cancel"),class:"btn-ghost"},
         {label:t("common.save"),class:"btn-primary",icon:"check",keepOpen:true,onClick:async()=>{
           const err=document.getElementById("p-err");
           err.hidden=true;
           try {
             const v=+document.getElementById("p-val").value;
             if(!(v>=1&&v<=65535)) throw new Error("1..65535");
             await api("/api/ports",{method:"POST",body:JSON.stringify({port:v})});
             window.closeModal();
             toast(t("ports.added"),"success");
             navigate("ports");
           } catch(e){ err.textContent=e.message; err.hidden=false; }
         }}]);
      setTimeout(()=>{ const i=document.getElementById("p-val"); if(i) i.focus(); },50);
    };
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=()=>{
      confirmDialog(t("ports.delete_confirm")+" "+b.dataset.del+"?", async()=>{
        try {
          await api("/api/ports/"+b.dataset.del,{method:"DELETE"});
          toast(t("ports.deleted"),"success");
          navigate("ports");
        } catch(e) { toast(e.message,"error"); }
      });
    });
  }
};
