/* Advanced nginx tuning — the form.

   Thirty fields, and the whole design problem is that almost none of them
   has a correct answer until you know what the machine is. Handing an
   operator thirty empty boxes labelled with nginx directive names would be
   worse than not having the page.

   So every field carries an auto-fill button — the magic-wand icon — which
   writes the value the server computed from the REAL core count, memory,
   descriptor limits and (on request) the measured uplink, and shows the
   reasoning underneath. Pressing it is not applying anything: the number
   lands in the box, the operator can change it, and nothing happens until
   Save.

   The reasoning is the part that matters. A panel that fills in 2048 and
   says nothing teaches nothing and cannot be argued with; one that says
   "420 MB available / 4, ~16 KB per connection, / 2 workers" can be checked
   and overridden with confidence.

   Groups are collapsible and all start CLOSED except the first. Thirty
   fields open at once is a wall; five groups of six is a page. */
window.Tuning = (function () {
"use strict";

/* The field catalogue.

   Each entry is [key, type, label-key, hint-key]. `type` drives the input:
   "int", "bool", "text", or "select:a|b|c". The key matches the JSON field
   AND the recommendation key, which is what lets auto-fill be generic
   rather than thirty special cases. */
const GROUPS = [
  ["workers", "cpu", [
    ["worker_processes",     "text", "tune.worker_processes"],
    ["worker_rlimit_nofile", "int",  "tune.worker_rlimit_nofile"],
    ["multi_accept",         "bool", "tune.multi_accept"],
  ]],
  ["proxy", "network", [
    ["proxy_read_timeout",     "int",  "tune.proxy_read_timeout"],
    ["proxy_send_timeout",     "int",  "tune.proxy_send_timeout"],
    ["proxy_connect_timeout",  "int",  "tune.proxy_connect_timeout"],
    ["proxy_socket_keepalive", "bool", "tune.proxy_socket_keepalive"],
    ["upstream_keepalive",     "int",  "tune.upstream_keepalive"],
    ["proxy_buffering_off",    "bool", "tune.proxy_buffering_off"],
  ]],
  ["timeouts", "clock", [
    ["keepalive_timeout",     "int", "tune.keepalive_timeout"],
    ["keepalive_requests",    "int", "tune.keepalive_requests"],
    ["client_header_timeout", "int", "tune.client_header_timeout"],
    ["client_body_timeout",   "int", "tune.client_body_timeout"],
    ["send_timeout",          "int", "tune.send_timeout"],
  ]],
  ["sizes", "database", [
    ["client_max_body_mb",      "int", "tune.client_max_body_mb"],
    ["client_body_buffer_kb",   "int", "tune.client_body_buffer_kb"],
    ["large_client_header_kb",  "int", "tune.large_client_header_kb"],
  ]],
  ["compression", "zap", [
    ["gzip_enabled",    "bool", "tune.gzip_enabled"],
    ["gzip_comp_level", "int",  "tune.gzip_comp_level"],
    ["gzip_min_length", "int",  "tune.gzip_min_length"],
  ]],
  ["tls", "lock", [
    ["ssl_session_cache_mb",    "int",  "tune.ssl_session_cache_mb"],
    ["ssl_session_timeout_min", "int",  "tune.ssl_session_timeout_min"],
    ["ssl_ecdh_curve",          "text", "tune.ssl_ecdh_curve"],
  ]],
  ["hardening", "shield", [
    ["server_tokens_off",   "bool", "tune.server_tokens_off"],
    ["limit_conn_per_ip",   "int",  "tune.limit_conn_per_ip"],
    ["open_file_cache_max", "int",  "tune.open_file_cache_max"],
    ["access_log_off",      "bool", "tune.access_log_off"],
  ]],
];

/* Fields whose recommendation genuinely depends on measuring the machine,
   as opposed to being a fixed sensible constant. The UI marks these so the
   operator understands why the "measure my server" button exists and what
   it improves. */
const NEEDS_SYSTEM = {
  worker_rlimit_nofile: true, upstream_keepalive: true,
  ssl_session_cache_mb: true, limit_conn_per_ip: true,
  open_file_cache_max: true, client_max_body_mb: true,
  keepalive_timeout: true, gzip_comp_level: true,
  worker_processes: true, multi_accept: true,
};

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function bytes(b) {
  if (!b || b < 0) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return (b >= 100 ? b.toFixed(0) : b.toFixed(1)) + " " + u[i];
}

/* One field row: the control, the auto-fill button, and the reason. */
function fieldRow(key, type, labelKey, data, t, Icons) {
  const cur = data.tuning[key];
  const rec = (data.recommended || {})[key] || {};
  const isBool = type === "bool";
  const sysMark = NEEDS_SYSTEM[key]
    ? `<span class="tn-sys" title="${esc(t("tune.needs_system"))}">${Icons.svg("cpu", 11)}</span>`
    : "";

  const control = isBool
    ? `<label class="checkbox tn-check">
         <input type="checkbox" data-tn="${key}" ${cur ? "checked" : ""}>
         <span class="check-box"></span>
       </label>`
    : `<input class="tn-input mono" dir="ltr" data-tn="${key}"
         type="${type === "int" ? "number" : "text"}"
         ${type === "int" ? 'inputmode="numeric" min="0"' : ""}
         value="${esc(cur == null || cur === 0 ? "" : cur)}"
         placeholder="${esc(rec.value || t("tune.nginx_default"))}">`;

  // The reason for the suggested value, with the numbers it came from.
  const why = rec.why
    ? `<div class="tn-why">
         <b>${esc(rec.value)}</b> — ${esc(t("tune.why_" + rec.why))}
         ${rec.detail ? `<span class="tn-detail mono" dir="ltr">${esc(rec.detail)}</span>` : ""}
       </div>`
    : "";

  return `
    <div class="tn-row" data-row="${key}">
      <div class="tn-head">
        <label class="tn-label">
          ${t(labelKey)}${sysMark}
          ${Icons.help(t(labelKey + "_help"))}
        </label>
        <div class="tn-ctl">
          ${control}
          <button type="button" class="btn btn-ghost btn-sm tn-auto" data-auto="${key}"
                  title="${esc(t("tune.autofill_one"))}">
            ${Icons.svg("dice", 13)}
          </button>
        </div>
      </div>
      <code class="tn-key" dir="ltr">${key}</code>
      ${why}
    </div>`;
}

/* Render the whole form into a container. */
function render(root, data, t, Icons, opts) {
  const sys = data.system || {};
  const on = !!data.enabled;

  const linkLabel = sys.link_source === "measured"
    ? `${sys.link_mbits} Mbit/s (${t("tune.link_measured")})`
    : sys.link_mbits
      ? `${sys.link_mbits} Mbit/s (${t("tune.link_kernel")})`
      : t("tune.link_unknown");

  const warn = (data.warnings || []).length
    ? `<div class="hp-conflict tn-warn">${Icons.svg("warning", 14)}
         <div><strong>${t("tune.warnings")}</strong>
         ${(data.warnings || []).map(w => `<div class="tiny">${t("tune.warn_" + w)}</div>`).join("")}
         </div></div>`
    : "";

  root.innerHTML = `
    <div class="card">
      <h3 class="card-title">${Icons.svg("zap", 16)} ${t("tune.title")}</h3>
      <p class="muted tiny">${t("tune.lede")}</p>

      <label class="switch">
        <input type="checkbox" id="tn-on" ${on ? "checked" : ""}>
        <span class="switch-track"><span class="switch-thumb"></span></span>
        <span>${t("tune.enable")}</span>
      </label>

      <div id="tn-body" ${on ? "" : "hidden"}>

        <!-- What the recommendations were computed from. Shown, not
             hidden, so the operator can judge the numbers rather than
             being asked to trust them. -->
        <div class="tn-sysbox">
          <div class="tn-sysgrid">
            ${[[t("tune.sys_cores"), sys.cores || "—"],
               [t("tune.sys_ram"), bytes(sys.ram_bytes)],
               [t("tune.sys_avail"), bytes(sys.available_bytes)],
               [t("tune.sys_swap"), bytes(sys.swap_bytes)],
               [t("tune.sys_nofile"), (sys.nofile_hard || 0).toLocaleString()],
               [t("tune.sys_link"), linkLabel],
              ].map(([k, v]) => `
              <div class="tn-sysitem">
                <div class="tn-sysk">${k}</div>
                <div class="tn-sysv mono" dir="ltr">${esc(v)}</div>
              </div>`).join("")}
          </div>
          <div class="btn-row tn-sysrow">
            <button class="btn btn-ghost btn-sm" id="tn-measure">
              ${Icons.svg("activity", 13)} ${t("tune.measure")}
            </button>
            <span class="tiny muted" id="tn-measure-note">${t("tune.measure_note")}</span>
          </div>
        </div>

        <!-- Presets. One click fills everything for a class of machine. -->
        <div class="tn-profiles">
          <span class="tiny muted">${t("tune.profile")}</span>
          ${(data.profiles || []).map(p => `
            <button class="btn btn-sm ${data.tuning.profile === p ? "btn-primary" : "btn-ghost"}"
                    data-profile="${p}">
              ${t("tune.profile_" + p)}
              ${p === data.suggested_profile ? `<span class="tn-fit">${t("tune.fits")}</span>` : ""}
            </button>`).join("")}
          <button class="btn btn-sm btn-ghost" id="tn-auto-all">
            ${Icons.svg("dice", 13)} ${t("tune.autofill_all")}
          </button>
        </div>

        ${warn}

        ${GROUPS.map(([gid, icon, fields], gi) => `
          <details class="tn-group" ${gi === 0 ? "open" : ""}>
            <summary>${Icons.svg(icon, 14)} ${t("tune.group_" + gid)}
              <span class="tn-count">${fields.length}</span></summary>
            <div class="tn-fields">
              ${fields.map(([k, ty, lk]) => fieldRow(k, ty, lk, data, t, Icons)).join("")}
            </div>
          </details>`).join("")}

        ${data.main_installed === false && on
          ? `<p class="tiny hp-warn">${t("tune.main_missing")}</p>` : ""}
      </div>

      <!-- Outside the collapsible body: switching the feature OFF hides
           that body, and the button has to stay reachable to save it. -->
      <div class="btn-row">
        <button class="btn btn-primary" id="tn-save">
          ${Icons.svg("check", 15)} ${t("common.save")}
        </button>
      </div>
    </div>`;

  // ── Wiring ─────────────────────────────────────────────────
  const onBox = root.querySelector("#tn-on");
  onBox.onchange = () => { root.querySelector("#tn-body").hidden = !onBox.checked; };

  const setField = (key, value) => {
    const el = root.querySelector(`[data-tn="${key}"]`);
    if (!el) return;
    if (el.type === "checkbox") {
      el.checked = value === "true" || value === true;
    } else {
      el.value = value;
    }
    // A brief highlight, so pressing auto-fill on a field that already
    // held that value still gives feedback rather than looking dead.
    const row = root.querySelector(`[data-row="${key}"]`);
    if (row) {
      row.classList.remove("tn-flash");
      void row.offsetWidth;   // force a reflow so the animation restarts
      row.classList.add("tn-flash");
    }
  };

  root.querySelectorAll("[data-auto]").forEach(b => {
    b.onclick = () => {
      const key = b.dataset.auto;
      const rec = (data.recommended || {})[key];
      if (rec) setField(key, rec.value);
    };
  });

  root.querySelector("#tn-auto-all").onclick = () => {
    Object.keys(data.recommended || {}).forEach(k => {
      // Never auto-fill the access log off. The recommendation is always
      // "false", but making a fill-everything button touch the switch
      // that blinds the statistics page would be a nasty surprise.
      if (k === "access_log_off") return;
      setField(k, data.recommended[k].value);
    });
    opts.toast(t("tune.filled"), "success");
  };

  root.querySelectorAll("[data-profile]").forEach(b => {
    b.onclick = () => opts.applyProfile(b.dataset.profile);
  });

  root.querySelector("#tn-measure").onclick = () => opts.measure();
  root.querySelector("#tn-save").onclick = () => opts.save(collect(root));
}

/* Read the form back into a request body.

   An empty box means "leave nginx's default alone" and is sent as 0, which
   is what the server treats as unset. That is why the placeholder shows the
   recommendation rather than the field being pre-filled: an operator who
   types nothing gets nginx's behaviour, not ours. */
function collect(root) {
  const body = { enabled: root.querySelector("#tn-on").checked };
  root.querySelectorAll("[data-tn]").forEach(el => {
    const key = el.dataset.tn;
    if (el.type === "checkbox") {
      body[key] = el.checked;
    } else if (el.type === "number") {
      body[key] = el.value === "" ? 0 : (parseInt(el.value, 10) || 0);
    } else {
      body[key] = el.value.trim();
    }
  });
  return body;
}

return { render, collect, GROUPS };
})();
