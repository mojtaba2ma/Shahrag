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
      <div class="card"><div id="pt-list"></div></div>`;

    /* 80 and 443 are structural: 80 is the HTTP→HTTPS redirect and 443 is
       where nearly everything actually arrives. Deleting either takes the
       whole server off the internet, so they are not deletable here and
       are not offered to a bulk action either. */
    const isCore = p => p.port === 80 || p.port === 443;

    window.ListView.create(document.getElementById("pt-list"), {
      id: "ports",
      rows: ports, t, Icons,
      rowKey: p => String(p.port),
      empty: t("ports.empty"),
      columns: [
        { key: "port", label: "ports.port", sortable: true, cls: "num",
          // Sorted as a NUMBER: as text, 8443 sorts before 943.
          plain: p => p.port,
          render: p => `<strong class="mono" dir="ltr">${p.port}</strong>` },
        { key: "kind", label: "map.type", sortable: true,
          plain: p => p.is_http ? "HTTP" : "HTTPS",
          render: p => `<span class="badge ${p.is_http ? "badge-neutral" : "badge-info"}">${
            p.is_http ? "HTTP" : "HTTPS"}</span>` +
            (isCore(p) ? ` <span class="badge badge-off">${t("ports.core")}</span>` : "") },
        { key: "used", label: "ports.used_by", sortable: true,
          plain: p => (p.used_by || []).join(" "),
          render: p => (p.used_by || []).length
            ? `<span class="tiny">${(p.used_by || []).map(x =>
                `<span class="badge badge-neutral">${x}</span>`).join(" ")}</span>`
            : `<span class="muted tiny">—</span>` },
        { key: "_act", label: "autoban.actions", cls: "row-actions",
          plain: () => "",
          render: p => `<button class="btn btn-danger btn-sm" data-del="${p.port}"
            ${isCore(p) ? "disabled" : ""}
            title="${isCore(p) ? t("ports.core_help") : t("common.delete")}"
            >${Icons.svg("trash",13)}</button>` },
      ],
      filters: [
        { id: "kind", label: "map.type", icon: "tag",
          options: [{ value: "https", label: "ports.https" },
                    { value: "http", label: "ports.http_redirect" }],
          match: (p, v) => v === "http" ? !!p.is_http : !p.is_http },
        { id: "used", label: "ports.used_by", icon: "state",
          options: [{ value: "yes", label: "common.active" },
                    { value: "no", label: "common.inactive" }],
          match: (p, v) => v === "yes" ? (p.used_by || []).length > 0
                                       : (p.used_by || []).length === 0 },
      ],
      bulk: [
        { id: "delete", label: "services.delete_selected", icon: "trash", danger: true,
          run: (sel, done) => {
            const safe = sel.filter(p => !isCore(p));
            if (!safe.length) {
              toast(t("ports.core_help"), "error");
              return;
            }
            confirmDialog(t("services.delete_n").replace("%n", safe.length), async () => {
              let n = 0;
              for (const p of safe) {
                try {
                  await api("/api/ports/" + p.port, { method: "DELETE" });
                  n++;
                } catch (e) { /* keep going */ }
              }
              toast(t("services.deleted_n").replace("%n", n), "success");
              done();
              navigate("ports");
            });
          } },
      ],
      onRender: (root) => {
        root.querySelectorAll("[data-del]").forEach(b => b.onclick = () => {
          confirmDialog(t("ports.delete_confirm") + " " + b.dataset.del + "?", async () => {
            try {
              await api("/api/ports/" + b.dataset.del, { method: "DELETE" });
              toast(t("ports.deleted"), "success");
              navigate("ports");
            } catch (e) { toast(e.message, "error"); }
          });
        });
      },
    });
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
  }
};
