/* The Backup tab.

   Split into four cards that answer four different questions, in the
   order an operator asks them:

     schedule   is this happening at all, and when
     off-site   is a copy leaving this machine
     archives   what have I actually got
     restore    (inside the archive list) put one back

   Its own file rather than another two hundred lines in settings.js,
   which is already the longest page in the panel. Loaded by a plain
   <script> tag like every other module, so the top level is wrapped in an
   IIFE — a bare `const` here would be a global. */
window.BackupTab = (function () {
"use strict";

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* Bytes, in the unit a human would use. A backup directory is measured in
   kilobytes and megabytes, never in bytes, and "40960" tells nobody
   anything. */
function fmtSize(n) {
  if (n == null) return "—";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

function fmtStamp(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return String(iso);
  const p = n => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
         `${p(d.getHours())}:${p(d.getMinutes())}`;
}

async function render(root, ctx) {
  const { api, t, Icons, toast, confirmDialog, modal } = ctx;

  let data;
  try {
    data = await api("/api/backup");
  } catch (e) {
    root.innerHTML = `<div class="card"><p class="muted">${esc(e.message)}</p></div>`;
    return;
  }
  const off = data.offsite || {};
  const ret = data.retention || {};

  const warnHTML = (data.warnings || []).length
    ? `<div class="bk-warn">${(data.warnings || []).map(w =>
        `<p class="tiny">${Icons.svg("warning", 13)} ${esc(w)}</p>`).join("")}</div>`
    : "";

  root.innerHTML = `
    <!-- ── Schedule ────────────────────────────────────── -->
    <div class="card">
      <div class="card-head">
        <h3 class="card-title">${Icons.svg("clock",16)} ${t("backup.schedule")}</h3>
        <button class="btn btn-primary btn-sm" id="bk-now">
          ${Icons.svg("archive",13)} <span class="btn-label">${t("backup.run_now")}</span>
        </button>
      </div>
      <p class="tiny muted">${t("backup.lede")}</p>

      <label class="switch">
        <input type="checkbox" id="bk-on" ${data.enabled ? "checked" : ""}>
        <span class="switch-track"><span class="switch-thumb"></span></span>
        <span>${t("backup.enable")}</span>
      </label>

      <div id="bk-body" ${data.enabled ? "" : "hidden"}>
        <div class="field-row">
          <div class="field">
            <label>${t("backup.scope")}${Icons.help(t("backup.scope_help"))}</label>
            <select id="bk-scope">
              <option value="config" ${data.scope === "config" ? "selected" : ""}>${t("backup.scope_config")}</option>
              <option value="full" ${data.scope === "full" ? "selected" : ""}>${t("backup.scope_full")}</option>
              <option value="nginx" ${data.scope === "nginx" ? "selected" : ""}>${t("backup.scope_nginx")}</option>
            </select>
          </div>
          <div class="field field-port">
            <label>${t("backup.every")}${Icons.help(t("backup.every_help"))}</label>
            <input id="bk-int" type="number" inputmode="numeric" min="1" max="168"
                   value="${data.interval_hours || 24}">
          </div>
          <div class="field field-port" id="bk-hour-field">
            <label>${t("backup.at_hour")}${Icons.help(t("backup.at_hour_help"))}</label>
            <input id="bk-hour" type="number" inputmode="numeric" min="0" max="23"
                   value="${data.hour == null ? 3 : data.hour}">
          </div>
        </div>

        <div class="tiny bk-label">${t("backup.retention")}${Icons.help(t("backup.retention_help"))}</div>
        <div class="field-row bk-ret">
          <div class="field field-port"><label class="tiny">${t("backup.hourly")}</label>
            <input id="bk-r-h" type="number" min="0" max="200" value="${ret.hourly == null ? 24 : ret.hourly}"></div>
          <div class="field field-port"><label class="tiny">${t("backup.daily")}</label>
            <input id="bk-r-d" type="number" min="0" max="200" value="${ret.daily == null ? 14 : ret.daily}"></div>
          <div class="field field-port"><label class="tiny">${t("backup.weekly")}</label>
            <input id="bk-r-w" type="number" min="0" max="200" value="${ret.weekly == null ? 8 : ret.weekly}"></div>
          <div class="field field-port"><label class="tiny">${t("backup.monthly")}</label>
            <input id="bk-r-m" type="number" min="0" max="200" value="${ret.monthly == null ? 12 : ret.monthly}"></div>
        </div>
        <p class="tiny muted" id="bk-ret-note"></p>

        <div class="field field-wide">
          <label>${t("backup.dir")}${Icons.help(t("backup.dir_help"))}</label>
          <input id="bk-dir" dir="ltr" class="mono" value="${esc(data.dir || "/var/backups/shahrag")}">
        </div>

        <label class="checkbox">
          <input type="checkbox" id="bk-enc" ${data.encrypt ? "checked" : ""}>
          <span class="check-box"></span>
          <span>${t("backup.encrypt")}</span>${Icons.help(t("backup.encrypt_help"))}
        </label>
        <div class="field field-wide" id="bk-pass-field" ${data.encrypt ? "" : "hidden"}>
          <label>${t("backup.passphrase")}</label>
          <input id="bk-pass" type="password" dir="ltr" class="mono"
                 placeholder="${data.passphrase_set ? t("backup.pass_set") : t("backup.pass_none")}">
          <span class="hint">${t("backup.passphrase_help")}</span>
        </div>
      </div>

      ${warnHTML}

      <div class="btn-row">
        <button class="btn btn-primary" id="bk-save">${Icons.svg("check",15)} ${t("common.save")}</button>
      </div>
      <p class="tiny muted" id="bk-next"></p>
    </div>

    <!-- ── Off-site ────────────────────────────────────── -->
    <div class="card">
      <h3 class="card-title">${Icons.svg("upload",16)} ${t("backup.offsite")}</h3>
      <p class="tiny muted">${t("backup.offsite_lede")}</p>

      <label class="switch">
        <input type="checkbox" id="os-on" ${off.enabled ? "checked" : ""}>
        <span class="switch-track"><span class="switch-thumb"></span></span>
        <span>${t("backup.offsite_enable")}</span>
      </label>

      <div id="os-body" ${off.enabled ? "" : "hidden"}>
        <p class="tiny hp-warn">${Icons.svg("warning",13)} ${t("backup.offsite_warn")}</p>

        <!-- SFTP -->
        <div class="ban-rule">
          <div class="ban-rule-head">
            <label class="checkbox">
              <input type="checkbox" id="os-sftp" ${off.sftp_enabled ? "checked" : ""}>
              <span class="check-box"></span>
              <span class="ban-rule-name">${t("backup.sftp")}</span>
            </label>
            ${Icons.help(t("backup.sftp_help"))}
          </div>
          <div class="ban-rule-body">
            <div class="field"><label class="tiny">${t("backup.host")}</label>
              <input id="os-host" dir="ltr" class="mono" value="${esc(off.sftp_host || "")}"></div>
            <div class="field field-port"><label class="tiny">${t("backup.port")}</label>
              <input id="os-port" type="number" min="1" max="65535" value="${off.sftp_port || 22}"></div>
            <div class="field"><label class="tiny">${t("backup.user")}</label>
              <input id="os-user" dir="ltr" class="mono" value="${esc(off.sftp_user || "")}"></div>
          </div>
          <div class="ban-rule-body">
            <div class="field field-wide"><label class="tiny">${t("backup.key")}</label>
              <input id="os-key" dir="ltr" class="mono" placeholder="/root/.ssh/id_ed25519"
                     value="${esc(off.sftp_key || "")}">
              <span class="hint">${t("backup.key_help")}</span></div>
          </div>
          <div class="ban-rule-body">
            <div class="field"><label class="tiny">${t("backup.password")}</label>
              <input id="os-pass" type="password" dir="ltr" class="mono"
                     placeholder="${data.sftp_password_set ? t("backup.pass_set") : t("backup.pass_none")}"></div>
            <div class="field"><label class="tiny">${t("backup.remote_path")}</label>
              <input id="os-path" dir="ltr" class="mono" placeholder="/backups/shahrag"
                     value="${esc(off.sftp_path || "")}"></div>
          </div>
          <p class="tiny muted">${off.sftp_fingerprint
            ? Icons.svg("lock",12) + " " + t("backup.pinned")
            : t("backup.not_pinned")}</p>
          <div class="btn-row">
            <button class="btn btn-ghost btn-sm" id="os-test">
              ${Icons.svg("zap",13)} <span class="btn-label">${t("backup.test")}</span>
            </button>
          </div>
        </div>

        <!-- Telegram -->
        <div class="ban-rule">
          <div class="ban-rule-head">
            <label class="checkbox">
              <input type="checkbox" id="os-tg" ${off.telegram_enabled ? "checked" : ""}>
              <span class="check-box"></span>
              <span class="ban-rule-name">${t("backup.telegram")}</span>
            </label>
            ${Icons.help(t("backup.telegram_help"))}
          </div>
          <div class="ban-rule-body">
            <div class="field field-wide"><label class="tiny">${t("backup.tg_chat")}</label>
              <input id="os-tgchat" dir="ltr" class="mono" placeholder="123456789"
                     value="${esc(off.telegram_chat_id || "")}">
              <span class="hint">${t("backup.tg_chat_help")}</span></div>
          </div>
        </div>
      </div>
    </div>

    <!-- ── The archives ────────────────────────────────── -->
    <div class="card">
      <div class="card-head">
        <h3 class="card-title">${Icons.svg("archive",16)} ${t("backup.archives")}</h3>
        <span class="badge badge-neutral" id="bk-total">${fmtSize(data.total_bytes)}</span>
      </div>
      <div id="bk-list"></div>
    </div>`;

  // ── behaviour ────────────────────────────────────────────
  const on = document.getElementById("bk-on");
  const body = document.getElementById("bk-body");
  on.onchange = () => { body.hidden = !on.checked; };

  const enc = document.getElementById("bk-enc");
  enc.onchange = () => {
    document.getElementById("bk-pass-field").hidden = !enc.checked;
  };

  const osOn = document.getElementById("os-on");
  const osBody = document.getElementById("os-body");
  osOn.onchange = () => {
    osBody.hidden = !osOn.checked;
    // Turning an off-site target on forces encryption on with it: the
    // server refuses the combination anyway, and finding that out on
    // save rather than here would be a worse way to learn it.
    if (osOn.checked && !enc.checked) {
      enc.checked = true;
      enc.dispatchEvent(new Event("change"));
      toast(t("backup.enc_forced"), "info");
    }
  };

  /* The hour only means something for a daily-or-coarser schedule. For
     "every 6 hours" it would be ignored, and a field that is ignored but
     still editable is a field that lies. */
  const intEl = document.getElementById("bk-int");
  const syncHour = () => {
    document.getElementById("bk-hour-field").hidden = (+intEl.value || 24) < 24;
  };
  intEl.oninput = syncHour;
  syncHour();

  const retNote = document.getElementById("bk-ret-note");
  const syncRet = () => {
    const h = +document.getElementById("bk-r-h").value || 0;
    const d = +document.getElementById("bk-r-d").value || 0;
    const w = +document.getElementById("bk-r-w").value || 0;
    const m = +document.getElementById("bk-r-m").value || 0;
    retNote.textContent = t("backup.ret_note")
      .replace("%n", h + d + w + m)
      .replace("%s", Math.round((h / 24) + d + w * 7 + m * 30));
  };
  ["bk-r-h","bk-r-d","bk-r-w","bk-r-m"].forEach(id =>
    document.getElementById(id).oninput = syncRet);
  syncRet();

  if (data.next_backup) {
    document.getElementById("bk-next").textContent =
      t("backup.next").replace("%s", fmtStamp(data.next_backup));
  }

  const payload = () => ({
    enabled: on.checked,
    scope: document.getElementById("bk-scope").value,
    interval_hours: +intEl.value || 24,
    hour: +document.getElementById("bk-hour").value || 0,
    dir: document.getElementById("bk-dir").value.trim(),
    encrypt: enc.checked,
    // Sent only when retyped. An empty string means "leave it alone",
    // never "clear it" — clearing a passphrase silently would disable
    // the encryption that the off-site copy depends on.
    passphrase: document.getElementById("bk-pass").value || undefined,
    retention: {
      hourly: +document.getElementById("bk-r-h").value || 0,
      daily: +document.getElementById("bk-r-d").value || 0,
      weekly: +document.getElementById("bk-r-w").value || 0,
      monthly: +document.getElementById("bk-r-m").value || 0,
    },
    offsite: {
      enabled: osOn.checked,
      sftp_enabled: document.getElementById("os-sftp").checked,
      sftp_host: document.getElementById("os-host").value.trim(),
      sftp_port: +document.getElementById("os-port").value || 22,
      sftp_user: document.getElementById("os-user").value.trim(),
      sftp_key: document.getElementById("os-key").value.trim(),
      sftp_password: document.getElementById("os-pass").value || undefined,
      sftp_path: document.getElementById("os-path").value.trim(),
      telegram_enabled: document.getElementById("os-tg").checked,
      telegram_chat_id: document.getElementById("os-tgchat").value.trim(),
    },
  });

  document.getElementById("bk-save").onclick = async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      await api("/api/backup", { method: "PUT", body: JSON.stringify(payload()) });
      toast(t("settings.saved"), "success");
      render(root, ctx);
    } catch (e) {
      toast(e.message, "error");
      btn.disabled = false;
    }
  };

  document.getElementById("bk-now").onclick = async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const r = await api("/api/backup/run", {
        method: "POST",
        body: JSON.stringify({ scope: document.getElementById("bk-scope").value }),
      });
      if (r.offsite_error) {
        // The local backup worked and the copy did not. Both facts.
        toast(t("backup.made_offsite_failed").replace("%s", r.offsite_error), "error");
      } else {
        toast(t("backup.made").replace("%s", fmtSize(r.archive && r.archive.size)), "success");
      }
      render(root, ctx);
    } catch (e) {
      toast(e.message, "error");
      btn.disabled = false;
    }
  };

  document.getElementById("os-test").onclick = async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    const old = btn.innerHTML;
    btn.textContent = t("backup.testing");
    try {
      // Saved first: testing settings the server has not been told about
      // would test the OLD ones and report a misleading result.
      await api("/api/backup", { method: "PUT", body: JSON.stringify(payload()) });
      await api("/api/backup/test-offsite", { method: "POST" });
      toast(t("backup.test_ok"), "success");
      render(root, ctx);
    } catch (e) {
      toast(e.message, "error");
      btn.disabled = false;
      btn.innerHTML = old;
    }
  };

  paintArchives(data.archives || [], root, ctx);
}

/* The archive list, as a ListView like every other list in the panel. */
function paintArchives(rows, root, ctx) {
  const { api, t, Icons, toast, confirmDialog, modal } = ctx;
  const el = document.getElementById("bk-list");
  if (!rows.length) {
    el.innerHTML = `<p class="muted tiny">${t("backup.none")}</p>`;
    return;
  }

  const genLabel = g => t("backup.gen_" + g) || g;

  window.ListView.create(el, {
    id: "backups",
    rows, t, Icons,
    rowKey: a => a.name,
    empty: t("backup.none"),
    columns: [
      { key: "created_at", label: "backup.taken", sortable: true,
        plain: a => a.created_at || "",
        render: a => `<span class="mono tiny" dir="ltr">${fmtStamp(a.created_at)}</span>` },
      { key: "scope", label: "backup.scope", sortable: true,
        plain: a => a.scope,
        render: a => `<span class="badge badge-neutral">${t("backup.scope_" + a.scope) || a.scope}</span>` },
      { key: "generation", label: "backup.kind", sortable: true,
        plain: a => a.generation,
        render: a => `<span class="badge ${a.generation === "manual"
          ? "badge-info" : "badge-neutral"}">${genLabel(a.generation)}</span>` },
      { key: "size", label: "backup.size", sortable: true, cls: "num",
        plain: a => a.size,
        render: a => `<span dir="ltr">${fmtSize(a.size)}</span>` },
      { key: "enc", label: "backup.encrypted", sortable: true,
        plain: a => a.encrypted ? 1 : 0,
        render: a => a.encrypted
          ? `<span class="badge badge-success">${Icons.svg("lock",11)}</span>`
          : `<span class="badge badge-off">${t("common.no")}</span>` },
      { key: "_act", label: "autoban.actions", cls: "row-actions",
        plain: () => "",
        render: a => `
          <button class="btn btn-sm btn-ghost" data-info="${esc(a.name)}"
                  title="${t("backup.inspect")}">${Icons.svg("eye",13)}</button>
          <a class="btn btn-sm btn-ghost" href="api/backup/archives/${encodeURIComponent(a.name)}/download"
             title="${t("backup.download")}">${Icons.svg("download",13)}</a>
          <button class="btn btn-sm btn-primary" data-restore="${esc(a.name)}"
                  title="${t("backup.restore")}">${Icons.svg("refresh",13)}</button>
          <button class="btn btn-sm btn-danger" data-del="${esc(a.name)}"
                  title="${t("common.delete")}">${Icons.svg("trash",13)}</button>` },
    ],
    filters: [
      { id: "scope", label: "backup.scope", icon: "tag",
        options: [{ value: "config", label: "backup.scope_config" },
                  { value: "full", label: "backup.scope_full" },
                  { value: "nginx", label: "backup.scope_nginx" }],
        match: (a, v) => a.scope === v },
      { id: "kind", label: "backup.kind", icon: "calendar",
        options: [{ value: "manual", label: "backup.gen_manual" },
                  { value: "auto", label: "backup.gen_hourly" }],
        match: (a, v) => v === "manual" ? a.generation === "manual"
                                        : a.generation !== "manual" },
    ],
    bulk: [
      { id: "delete", label: "services.delete_selected", icon: "trash", danger: true,
        run: (sel, done) => {
          confirmDialog(t("services.delete_n").replace("%n", sel.length), async () => {
            let n = 0;
            for (const a of sel) {
              try {
                await api("/api/backup/archives/" + encodeURIComponent(a.name),
                          { method: "DELETE" });
                n++;
              } catch (e) { /* keep going */ }
            }
            toast(t("services.deleted_n").replace("%n", n), "success");
            done();
            render(root, ctx);
          });
        } },
    ],
    onRender: (r) => {
      r.querySelectorAll("[data-del]").forEach(b => b.onclick = () =>
        confirmDialog(t("backup.delete_confirm"), async () => {
          try {
            await api("/api/backup/archives/" + encodeURIComponent(b.dataset.del),
                      { method: "DELETE" });
            toast(t("settings.saved"), "success");
            render(root, ctx);
          } catch (e) { toast(e.message, "error"); }
        }));

      r.querySelectorAll("[data-info]").forEach(b => b.onclick = async () => {
        try {
          const info = await api("/api/backup/archives/" +
                                 encodeURIComponent(b.dataset.info));
          modal(t("backup.inspect"), `
            <table class="data-table"><tbody>
              <tr><td>${t("backup.taken")}</td><td class="mono" dir="ltr">${fmtStamp(info.created_at)}</td></tr>
              <tr><td>${t("backup.scope")}</td><td>${esc(info.scope)}</td></tr>
              <tr><td>${t("backup.from_host")}</td><td class="mono" dir="ltr">${esc(info.hostname)}</td></tr>
              <tr><td>${t("backup.build")}</td><td class="mono" dir="ltr">${esc(info.build)}</td></tr>
              <tr><td>${t("nav.domains")}</td><td>${info.domains}</td></tr>
              <tr><td>${t("nav.services")}</td><td>${info.services}</td></tr>
            </tbody></table>
            <p class="tiny muted">${t("backup.contents")}</p>
            <div class="bk-files">${(info.files || []).map(f =>
              `<div><code dir="ltr">${esc(f.name)}</code>
               <span class="tiny muted">${fmtSize(f.size)}</span></div>`).join("")}</div>`,
            [{ label: t("common.close"), class: "btn-ghost" }]);
        } catch (e) { toast(e.message, "error"); }
      });

      r.querySelectorAll("[data-restore]").forEach(b => b.onclick = () =>
        restoreDialog(b.dataset.restore, root, ctx));
    },
  });
}

/* Restoring asks two things before it does anything: WHAT to put back, and
   the passphrase if the archive is encrypted. Certificates and state are
   unticked by default — the certificates on disk are usually newer than
   the ones in the backup, and overwriting a freshly renewed certificate
   with a month-old one is a way to cause the outage you were trying to
   recover from. */
function restoreDialog(name, root, ctx) {
  const { api, t, Icons, toast, modal } = ctx;
  modal(t("backup.restore"), `
    <div class="form-error" id="rs-err" hidden></div>
    <p class="tiny">${t("backup.restore_lede").replace("%s", esc(name))}</p>
    <p class="tiny hp-warn">${Icons.svg("warning",13)} ${t("backup.restore_warn")}</p>
    <label class="checkbox">
      <input type="checkbox" id="rs-config" checked><span class="check-box"></span>
      <span>${t("backup.rs_config")}</span>
    </label>
    <label class="checkbox">
      <input type="checkbox" id="rs-certs"><span class="check-box"></span>
      <span>${t("backup.rs_certs")}</span>${Icons.help(t("backup.rs_certs_help"))}
    </label>
    <label class="checkbox">
      <input type="checkbox" id="rs-state"><span class="check-box"></span>
      <span>${t("backup.rs_state")}</span>${Icons.help(t("backup.rs_state_help"))}
    </label>
    <div class="field field-wide">
      <label>${t("backup.passphrase")}</label>
      <input id="rs-pass" type="password" dir="ltr" class="mono"
             placeholder="${t("backup.pass_if_encrypted")}">
    </div>`,
    [{ label: t("common.cancel"), class: "btn-ghost" },
     { label: t("backup.restore"), class: "btn-primary", icon: "refresh", keepOpen: true,
       onClick: async () => {
         const err = document.getElementById("rs-err");
         err.hidden = true;
         try {
           const r = await api("/api/backup/archives/" + encodeURIComponent(name) + "/restore", {
             method: "POST",
             body: JSON.stringify({
               config: document.getElementById("rs-config").checked,
               certs: document.getElementById("rs-certs").checked,
               state: document.getElementById("rs-state").checked,
               passphrase: document.getElementById("rs-pass").value || undefined,
             }),
           });
           window.closeModal();
           if (r.applied === false) {
             toast(t("backup.restored_nginx_failed"), "error");
           } else {
             toast(t("backup.restored").replace("%n",
               ((r.result && r.result.restored) || []).length), "success");
           }
           render(root, ctx);
         } catch (e) {
           err.textContent = e.message;
           err.hidden = false;
         }
       } }]);
}

return { render };
})();
