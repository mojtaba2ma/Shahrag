/* Ports page — mirrors the CLI panel ports flow. */
window.Pages = window.Pages || {};
window.Pages.ports = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog, toast, navigate } = ctx;
    const ports = await api("/api/ports");
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("ports",20)} ${t("ports.title")}</h1>
        <button class="btn btn-primary" id="add-port">${Icons.svg("plus",14)} ${t("ports.add")}</button>
      </div>
      <div class="card"><div class="card-list">
        ${ports.map(p=>`<div class="list-row">
          <span class="list-val">:${p.port}</span>
          <span class="badge ${p.is_http?"badge-neutral":"badge-info"}">${p.is_http?"HTTP":"HTTPS"}</span>
          <span class="muted">${(p.used_by||[]).join(", ")||""}</span>
          <button class="btn btn-danger btn-sm" data-del="${p.port}" ${p.port===80||p.port===443?"disabled":""}>${Icons.svg("trash",13)}</button>
        </div>`).join("")}
      </div></div>`;
    container.querySelector("#add-port").onclick = async()=>{
      const v = prompt(t("ports.add")+":");
      if (!v) return;
      try {
        await api("/api/ports",{method:"POST",body:JSON.stringify({port:+v})});
        toast(t("ports.added"),"success");
        navigate("ports");
      } catch(e) { toast(e.message,"error"); }
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
