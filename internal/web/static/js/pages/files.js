/* Config files — view and edit config.json and the nginx files.

   Reading these two files answers most "why is it doing that?" questions, and
   editing them turns an urgent fix into a two-minute job instead of an SSH
   session. Every save is transactional on the server (snapshot → validate →
   restore on failure), so a broken edit cannot take the server down; this
   page's job is to make that obvious and hard to misuse:

     • generated files are labelled as such, because the next Generate
       overwrites them;
     • the Save button stays disabled until something actually changes;
     • nginx's own error output is shown verbatim when it rejects an edit. */
window.Pages = window.Pages || {};

(function () {
"use strict";

let files = [];
let current = null;   // { id, path, content, language, generated, description }
let original = "";

window.Pages.files = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, confirmDialog } = ctx;

    files = await api("/api/files");

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("logs", 20)} ${t("files.title")}</h1>
        <div class="btn-row" style="margin:0">
          <button class="btn btn-ghost btn-sm" id="f-reload-file">
            ${Icons.svg("refresh", 14)} <span class="btn-label">${t("files.reload_file")}</span>
          </button>
          <button class="btn btn-primary btn-sm" id="f-save" disabled>
            ${Icons.svg("check", 14)} <span class="btn-label">${t("common.save")}</span>
          </button>
        </div>
      </div>

      <div class="card" style="padding:0">
        <div class="file-tabs" id="f-tabs">
          ${files.map(f => `
            <button class="file-tab" data-id="${f.id}" ${f.exists ? "" : "disabled"}>
              ${Icons.svg(f.language === "json" ? "settings" : "zap", 13)}
              <span>${f.label}</span>
              ${f.generated ? `<span class="file-badge">${t("files.generated_short")}</span>` : ""}
              ${f.exists ? "" : `<span class="file-badge missing">${t("files.missing")}</span>`}
            </button>`).join("")}
        </div>
      </div>

      <div id="f-meta"></div>

      <div class="card" style="padding:0;overflow:hidden">
        <div class="editor-wrap">
          <textarea id="f-editor" class="code-editor" spellcheck="false"
                    dir="ltr" wrap="off" placeholder="…"></textarea>
        </div>
        <div class="editor-foot">
          <span id="f-stats" class="muted"></span>
          <label class="checkbox" style="margin:0">
            <input type="checkbox" id="f-apply" checked><span class="check-box"></span>
            <span>${t("files.apply_now")}</span>
          </label>
        </div>
      </div>

      <div id="f-result"></div>`;

    const editor = document.getElementById("f-editor");
    const saveBtn = document.getElementById("f-save");

    const markDirty = () => {
      const dirty = editor.value !== original;
      saveBtn.disabled = !dirty;
      const lines = editor.value ? editor.value.split("\n").length : 0;
      document.getElementById("f-stats").textContent =
        `${lines} ${t("files.lines")}${dirty ? " · " + t("files.unsaved") : ""}`;
    };

    const open = async (id) => {
      container.querySelectorAll(".file-tab").forEach(b =>
        b.classList.toggle("active", b.dataset.id === id));
      document.getElementById("f-result").innerHTML = "";
      try {
        current = await api("/api/files/" + encodeURIComponent(id));
        original = current.content || "";
        editor.value = original;
        document.getElementById("f-meta").innerHTML = `
          <div class="file-meta ${current.generated ? "warn" : ""}">
            <code class="file-path">${current.path}</code>
            <span>${current.description || ""}</span>
          </div>`;
        markDirty();
      } catch (e) {
        toast(e.message, "error");
      }
    };

    container.querySelectorAll(".file-tab").forEach(b => b.onclick = () => open(b.dataset.id));
    editor.addEventListener("input", markDirty);

    // Tab should indent, not jump to the next control: this is a code editor.
    editor.addEventListener("keydown", (e) => {
      if (e.key !== "Tab") return;
      e.preventDefault();
      const s = editor.selectionStart, en = editor.selectionEnd;
      editor.value = editor.value.slice(0, s) + "    " + editor.value.slice(en);
      editor.selectionStart = editor.selectionEnd = s + 4;
      markDirty();
    });

    document.getElementById("f-reload-file").onclick = () => {
      if (!current) return;
      if (editor.value !== original) {
        confirmDialog(t("files.discard_confirm"), () => open(current.id),
                      { label: t("files.reload_file"), danger: false, icon: "refresh" });
      } else {
        open(current.id);
      }
    };

    saveBtn.onclick = () => {
      if (!current) return;
      const apply = document.getElementById("f-apply").checked;
      // Editing a generated file is legitimate for an urgent fix, but the
      // operator must know it will not survive the next Generate.
      const warn = current.generated ? t("files.generated_confirm") + "\n\n" : "";
      confirmDialog(warn + t("files.save_confirm"), async () => {
        saveBtn.disabled = true;
        try {
          const res = await api("/api/files/" + encodeURIComponent(current.id), {
            method: "PUT",
            body: JSON.stringify({ content: editor.value, reload: apply }),
          });
          showResult(res, t, Icons);
          if (res.ok) {
            original = editor.value;
            toast(t("settings.saved"), "success");
          } else {
            toast(res.detail || t("files.rejected"), "error");
          }
        } catch (e) {
          // A rejected edit comes back as 4xx, which api() turns into a
          // thrown error. The MESSAGE is the useful part (it names the JSON
          // syntax error), so it belongs in the result panel — not only in a
          // toast that disappears after a few seconds.
          showResult({ ok: false, detail: e.message }, t, Icons);
          toast(e.message, "error");
        }
        markDirty();
      }, { label: t("common.save"), danger: false, icon: "check" });
    };

    await open(files.find(f => f.exists) ? files.find(f => f.exists).id : files[0].id);
  },
};

/* showResult renders nginx's own output. When a config is rejected the exact
   message (with its line number) is the only thing that matters, so it is
   shown verbatim rather than summarised. */
function showResult(res, t, Icons) {
  const box = document.getElementById("f-result");
  if (!box) return;
  if (res.ok) {
    const extra = res.warning ? `<p class="muted" style="margin-top:6px">${res.warning}</p>` : "";
    box.innerHTML = `
      <div class="card" style="border-color:var(--success)">
        <h3>${Icons.svg("check", 16)} ${t("files.saved_ok")}</h3>
        <p class="muted"><code>${res.path || ""}</code></p>${extra}
      </div>`;
    return;
  }
  box.innerHTML = `
    <div class="card" style="border-color:var(--danger)">
      <h3>${Icons.svg("warning", 16)} ${t("files.rejected")}</h3>
      <p class="muted">${res.detail || ""}</p>
      ${res.stderr ? `<pre class="log-view" style="margin-top:10px">${escapeHTML(res.stderr)}</pre>` : ""}
    </div>`;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

})();
