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
      <input id="d-key" dir="ltr" class="mono" value="${d.key||""}" placeholder="/etc/letsencrypt/live/example.com/privkey.pem"></div>

    ${isEdit ? `
    <div class="btn-row" style="margin-top:4px">
      <button type="button" class="btn btn-ghost btn-block" id="d-issue">
        ${Icons.svg("lock",14)} ${t("domains.get_cert").replace("%s", name)}</button>
    </div>
    <span class="hint">${t("domains.get_cert_hint")}</span>
    <div class="form-error" id="d-issue-err" hidden></div>
    <div id="d-issue-progress" hidden>
      <div class="tiny" id="d-issue-state"></div>
      <pre class="log-view" id="d-issue-log" style="max-height:160px"></pre>
    </div>` : `<span class="hint">${t("domains.get_cert_after_save")}</span>`}`,
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

  /* "Get certificate" issues for THIS domain and drops the resulting paths
     into THIS form's fields.

     The domain is taken from the closure constant below, never re-read from
     the form or from a row the user might meanwhile have clicked. That is
     the whole safety story here: if the name could drift, a certificate for
     one domain would be written onto another, and nginx would then serve a
     name mismatch that is genuinely hard to diagnose. The response is also
     checked against the same constant before anything is filled in. */
  if (!isEdit) return;
  const forDomain = name;               // frozen at dialog-open time

  const issueBtn = document.getElementById("d-issue");
  if (!issueBtn) return;

  issueBtn.onclick = async () => {
    const err = document.getElementById("d-issue-err");
    const prog = document.getElementById("d-issue-progress");
    const logEl = document.getElementById("d-issue-log");
    const stateEl = document.getElementById("d-issue-state");
    err.hidden = true;
    prog.hidden = false;
    issueBtn.disabled = true;

    let job;
    try {
      job = await api("/api/certs/issue", {
        method: "POST",
        body: JSON.stringify({
          domain: forDomain,
          wildcard: true,          // covers the domain and one subdomain level
          challenge: "dns-01",
          method: "cloudflare",
          remember: true,
        }),
      });
    } catch (e) {
      err.textContent = e.message;
      err.hidden = false;
      prog.hidden = true;
      issueBtn.disabled = false;
      return;
    }

    const timer = setInterval(async () => {
      let j;
      try {
        j = await api("/api/certs/jobs/" + encodeURIComponent(job.job));
      } catch (_) { return; }

      logEl.textContent = (j.log || []).join("\n");
      logEl.scrollTop = logEl.scrollHeight;
      stateEl.textContent = t("certs.state_" + j.state) || j.state;

      if (j.state === "waiting_dns") {
        // The manual flow needs a record the operator must create, and this
        // compact dialog is the wrong place for that. Say so plainly rather
        // than appearing to hang.
        clearInterval(timer);
        err.textContent = t("domains.get_cert_manual");
        err.hidden = false;
        issueBtn.disabled = false;
        return;
      }

      if (j.state === "done") {
        clearInterval(timer);
        // Re-read the record and verify it is the domain we asked for
        // before touching the inputs.
        try {
          const list = await api("/api/certs");
          const row = (list.certs || []).find(c => c.domain === forDomain);
          if (!row || !row.cert_path) throw new Error(t("domains.get_cert_missing"));
          document.getElementById("d-cert").value = row.cert_path;
          document.getElementById("d-key").value = row.key_path;
          toast(t("domains.get_cert_done").replace("%s", forDomain), "success");
        } catch (e) {
          err.textContent = e.message;
          err.hidden = false;
        }
        issueBtn.disabled = false;
      } else if (j.state === "error") {
        clearInterval(timer);
        err.textContent = j.error || "failed";
        err.hidden = false;
        issueBtn.disabled = false;
      }
    }, 1500);
  };
}
