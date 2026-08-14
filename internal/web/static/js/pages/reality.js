/* Reality page */
window.Pages = window.Pages || {};
window.Pages.reality = {
  async render(container, state, ctx) {
    const { api, t, Icons, modal } = ctx;
    const r = await api("/api/reality");
    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("reality",20)} ${t("reality.title")}</h1>
        <label class="switch">
          <input type="checkbox" id="r-en" ${r.enabled?"checked":""}>
          <span>${t("reality.enabled")}</span>
        </label>
      </div>
      <div class="card"><div class="field">
        <label>${t("reality.http_port")}</label>
        <input id="r-port" type="number" value="${r.http_port}">
      </div>
      <button class="btn btn-primary" id="r-save">${Icons.svg("check",14)} Save</button></div>
      <div class="card">
        <div class="card-head"><h3>${Icons.svg("services",16)} ${t("reality.services")}</h3>
          <button class="btn btn-ghost btn-sm" id="r-add">${Icons.svg("plus",13)} ${t("reality.add_service")}</button></div>
        <div class="table-wrap"><table class="data-table">
          <thead><tr><th>${t("services.name")}</th><th>SNI</th><th>${t("services.local_port")}</th><th>Ports</th><th></th></tr></thead>
          <tbody>${Object.entries(r.services||{}).map(([n,s])=>`
            <tr><td><strong>${n}</strong></td><td>${s.sni}</td><td>:${s.local_port}</td>
            <td>${(s.ports||[]).join(", ")}</td>
            <td><button class="btn btn-danger btn-sm" data-del="${n}">${Icons.svg("trash",13)}</button></td></tr>`).join("")}
          </tbody></table></div></div>`;
    const save = async()=>{
      await api("/api/reality",{method:"PUT",body:JSON.stringify({
        enabled:document.getElementById("r-en").checked,
        http_port:+document.getElementById("r-port").value})});
      location.reload();
    };
    document.getElementById("r-save").onclick = save;
    document.getElementById("r-add").onclick = ()=>realityForm(ctx);
    container.querySelectorAll("[data-del]").forEach(b=>b.onclick=async()=>{
      await api("/api/reality/services/"+encodeURIComponent(b.dataset.del),{method:"DELETE"}); location.reload();
    });
  }
};
function realityForm(ctx){
  const {t,Icons,modal}=ctx;
  modal(t("reality.add_service"),`
    <div class="field"><label>${t("services.name")}</label><input id="r-n"></div>
    <div class="field"><label>SNI</label><input id="r-sni" placeholder="dl.google.com"></div>
    <div class="field-row"><div class="field"><label>${t("services.local_port")}</label><input id="r-lp" type="number" value="443"></div>
    <div class="field"><label>Port</label><input id="r-p" type="number" value="443"></div></div>`,
    [{label:t("common.cancel"),class:"btn-ghost"},
     {label:t("common.save"),class:"btn-primary",icon:"check",onClick:async()=>{
       await api("/api/reality/services",{method:"POST",body:JSON.stringify({
         name:document.getElementById("r-n").value.trim(),
         sni:document.getElementById("r-sni").value.trim(),
         local_port:+document.getElementById("r-lp").value,
         ports:[+document.getElementById("r-p").value]})});
       location.reload();
     }}]);
}
