/* Ports page */
window.Pages = window.Pages || {};
window.Pages.ports = {
  async render(container, state, ctx) {
    const { api, t, Icons, confirmDialog } = ctx;
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
      const v = prompt("Port number:");
      if (v) { await api("/api/ports",{method:"POST",body:JSON.stringify({port:+v})}); location.reload(); }
    };
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=()=>{
      confirmDialog("Delete port "+b.dataset.del+"?", async()=>{
        await api("/api/ports/"+b.dataset.del,{method:"DELETE"}); location.reload();
      });
    });
  }
};
