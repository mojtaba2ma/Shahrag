/* Logs page */
window.Pages = window.Pages || {};
window.Pages.logs = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;
    const [http, stream, err] = await Promise.all([
      api("/api/logs/http?lines=100"),
      api("/api/logs/stream?lines=100").catch(()=>({content:""})),
      api("/api/logs/error?lines=100"),
    ]);
    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("logs",20)} ${t("logs.title")}</h1>
        <button class="btn btn-ghost btn-sm" id="refresh">${Icons.svg("refresh",14)} Refresh</button></div>
      <div class="tabs">
        <button class="tab active" data-tab="http">HTTP</button>
        <button class="tab" data-tab="stream">Stream</button>
        <button class="tab" data-tab="error">Error</button>
      </div>
      <pre class="log-view" id="log-out"></pre>`;
    const out = document.getElementById("log-out");
    const data = {http:http.content, stream:stream.content, error:err.content};
    const show = k => out.textContent = data[k]||"(empty)";
    show("http");
    container.querySelectorAll(".tab").forEach(b=>b.onclick=()=>{
      container.querySelectorAll(".tab").forEach(x=>x.classList.remove("active"));
      b.classList.add("active"); show(b.dataset.tab);
    });
    document.getElementById("refresh").onclick=()=>location.reload();
  }
};
