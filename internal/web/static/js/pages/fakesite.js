/* Fake site page */
window.Pages = window.Pages || {};
window.Pages.fakesite = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, navigate } = ctx;
    const fs = await api("/api/fakesite");
    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("fakesite",20)} ${t("fakesite.title")}</h1></div>
      <div class="card">
        <div class="field"><label>${t("fakesite.mode")}</label>
          <select id="f-mode">
            <option value="default" ${fs.mode==="default"?"selected":""}>${t("fakesite.default")}</option>
            <option value="custom_content" ${fs.mode==="custom_content"?"selected":""}>${t("fakesite.custom_html")}</option>
            <option value="custom_file" ${fs.mode==="custom_file"?"selected":""}>${t("fakesite.custom_file")}</option>
          </select></div>
        <div class="field" id="f-content-field" hidden>
          <label>${t("fakesite.content")}</label>
          <textarea id="f-content" rows="10">${fs.content||""}</textarea></div>
        <div class="field" id="f-file-field" hidden>
          <label>${t("fakesite.file_path")}</label>
          <input id="f-file" value="${fs.source_path||""}"></div>
        <button class="btn btn-primary" id="f-save">${Icons.svg("check",14)} Save</button>
      </div>`;
    const mode = document.getElementById("f-mode");
    const update = ()=>{
      document.getElementById("f-content-field").hidden = mode.value!=="custom_content";
      document.getElementById("f-file-field").hidden = mode.value!=="custom_file";
    };
    mode.onchange = update; update();
    document.getElementById("f-save").onclick = async()=>{
      try {
        await api("/api/fakesite",{method:"PUT",body:JSON.stringify({
          mode:mode.value,
          content:document.getElementById("f-content").value,
          source_path:document.getElementById("f-file").value})});
        toast(t("settings.saved"),"success");
        navigate("fakesite");
      } catch(e) { toast(e.message,"error"); }
    };
  }
};
