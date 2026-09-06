/* Honeypot — paths no legitimate visitor ever requests.

   The page has to make one thing unmistakable: the default response is to
   SLOW an offender down, not to refuse them. A server observed refusing
   connections is a server whose address gets filtered, so the quiet mode is
   both the default and the recommended one, and the block mode carries a
   warning rather than being presented as the "strong" choice.

   A wrapping IIFE, like every other page module: these load through plain
   <script> tags, so a top-level const would become a global. */
window.Pages = window.Pages || {};

(function () {
"use strict";

function fmtList(v) { return (v || []).join(", "); }
function parseList(s) {
  return String(s || "").split(/[,\n]/).map(x => x.trim()).filter(Boolean);
}

window.Pages.honeypot = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, navigate } = ctx;
    const hp = await api("/api/honeypot");
    const mode = hp.mode || "throttle";

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("shield", 20)} ${t("honeypot.title")}</h1>
      </div>

      <div class="card">
        <p class="muted hp-lede">${t("honeypot.lede")}</p>

        <label class="switch">
          <input type="checkbox" id="hp-on" ${hp.enabled ? "checked" : ""}>
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("honeypot.enable")}</span>
        </label>

        <div id="hp-body" ${hp.enabled ? "" : "hidden"}>
          <div class="field field-wide">
            <label>${t("honeypot.mode")}${Icons.help(t("honeypot.mode_help"))}</label>
            <select id="hp-mode">
              <option value="throttle" ${mode === "throttle" ? "selected" : ""}>${t("honeypot.mode_throttle")}</option>
              <option value="decoy" ${mode === "decoy" ? "selected" : ""}>${t("honeypot.mode_decoy")}</option>
              <option value="block" ${mode === "block" ? "selected" : ""}>${t("honeypot.mode_block")}</option>
            </select>
            <p class="tiny muted" id="hp-mode-note"></p>
          </div>

          <div class="field field-port" id="hp-rate-field">
            <label>${t("honeypot.rate")}${Icons.help(t("honeypot.rate_help"))}</label>
            <input id="hp-rate" type="number" inputmode="numeric" min="1" max="600"
                   value="${hp.rate_per_minute || hp.default_rate_per_minute}">
          </div>

          <label class="checkbox">
            <input type="checkbox" id="hp-log" ${hp.log_hits ? "checked" : ""}>
            <span class="check-box"></span>
            <span>${t("honeypot.log")}</span>${Icons.help(t("honeypot.log_help"))}
          </label>

          <div class="gate-except">
            <div class="tiny">${t("honeypot.exceptions")}</div>
            <div class="field field-wide">
              <label>${t("honeypot.allow_paths")}${Icons.help(t("honeypot.allow_paths_help"))}</label>
              <input id="hp-allow-paths" dir="ltr" class="mono"
                     value="${fmtList(hp.allow_paths)}" placeholder="/wp-admin">
            </div>
            <div class="field field-wide">
              <label>${t("honeypot.allow_ips")}${Icons.help(t("honeypot.allow_ips_help"))}</label>
              <input id="hp-allow-ips" dir="ltr" class="mono"
                     value="${fmtList(hp.allow_ips)}" placeholder="10.0.0.0/24, 192.168.1.5">
            </div>
            <div class="field field-wide">
              <label>${t("honeypot.extra_paths")}${Icons.help(t("honeypot.extra_paths_help"))}</label>
              <input id="hp-extra" dir="ltr" class="mono"
                     value="${fmtList(hp.extra_paths)}" placeholder="/my-trap">
            </div>
          </div>

          <div id="hp-conflicts"></div>
        </div>

        <!-- Outside the collapsible body on purpose: switching the trap OFF
             hides that body, and with the button inside it there would be
             no way to save the change. -->
        <div class="btn-row">
          <button class="btn btn-primary" id="hp-save">
            ${Icons.svg("check", 15)} ${t("common.save")}
          </button>
        </div>
      </div>

      <div class="card" id="hp-paths-card" ${hp.enabled ? "" : "hidden"}>
        <h3 class="card-title">${Icons.svg("logs", 16)} ${t("honeypot.covered")}
          <span class="badge badge-neutral" id="hp-count">${(hp.effective_paths || []).length}</span>
        </h3>
        <p class="tiny muted">${t("honeypot.covered_help")}</p>
        <div class="hp-paths" id="hp-paths">
          ${(hp.effective_paths || []).map(p =>
            `<code class="hp-path">${p}</code>`).join("")}
        </div>
      </div>

      <div class="card" id="hp-hits-card" ${hp.enabled ? "" : "hidden"}>
        <div class="card-head">
          <h3 class="card-title">${Icons.svg("activity", 16)} ${t("honeypot.hits")}</h3>
          <button class="btn btn-ghost btn-sm" id="hp-refresh">
            ${Icons.svg("refresh", 14)} <span class="btn-label">${t("stats.refresh")}</span>
          </button>
        </div>
        <div id="hp-hits"></div>
      </div>`;

    const on = document.getElementById("hp-on");
    const body = document.getElementById("hp-body");
    const modeSel = document.getElementById("hp-mode");
    const note = document.getElementById("hp-mode-note");
    const rateField = document.getElementById("hp-rate-field");

    // The mode note is the honest part of this page: it says out loud that
    // blocking makes the server conspicuous.
    const syncMode = () => {
      const m = modeSel.value;
      note.textContent = t("honeypot.note_" + m);
      note.className = "tiny " + (m === "block" ? "hp-warn" : "muted");
      // The request budget only exists in the modes that throttle.
      rateField.hidden = m === "block";
    };
    modeSel.onchange = syncMode;
    syncMode();

    on.onchange = () => {
      body.hidden = !on.checked;
      document.getElementById("hp-paths-card").hidden = !on.checked;
      document.getElementById("hp-hits-card").hidden = !on.checked;
    };

    const showConflicts = (list) => {
      const el = document.getElementById("hp-conflicts");
      if (!list || !list.length) { el.innerHTML = ""; return; }
      el.innerHTML = `<div class="hp-conflict">${Icons.svg("warning", 14)}
        <div><strong>${t("honeypot.conflict")}</strong>
        <div class="tiny">${t("honeypot.conflict_help")}</div>
        <div class="tiny mono">${list.join(" · ")}</div></div></div>`;
    };
    showConflicts(hp.conflicts);

    document.getElementById("hp-save").onclick = async (ev) => {
      const btn = ev.currentTarget;
      btn.disabled = true;
      try {
        const res = await api("/api/honeypot", {
          method: "PUT",
          body: JSON.stringify({
            enabled: on.checked,
            mode: modeSel.value,
            rate_per_minute: +document.getElementById("hp-rate").value || 0,
            log_hits: document.getElementById("hp-log").checked,
            allow_paths: parseList(document.getElementById("hp-allow-paths").value),
            allow_ips: parseList(document.getElementById("hp-allow-ips").value),
            extra_paths: parseList(document.getElementById("hp-extra").value),
          }),
        });
        // The save is reported separately from the reload, exactly as the
        // service form does: the setting is stored either way, and hiding a
        // config nginx refused would be the worst outcome.
        if (res.applied === false) {
          toast(t("services.apply_failed") + (res.apply_error ? ": " + res.apply_error : ""), "error");
        } else {
          toast(t("settings.saved"), "success");
        }
        showConflicts(res.conflicts);
        navigate("honeypot");
      } catch (e) {
        toast(e.message, "error");
        btn.disabled = false;
      }
    };

    const loadHits = async () => {
      const el = document.getElementById("hp-hits");
      try {
        const r = await api("/api/honeypot/hits?limit=100");
        const hits = r.hits || [];
        if (!hits.length) {
          el.innerHTML = `<p class="muted tiny">${t("honeypot.no_hits")}</p>`;
          return;
        }
        const top = (r.top || []).map(x =>
          `<span class="badge badge-neutral mono">${x.ip} × ${x.count}</span>`).join(" ");
        el.innerHTML = `
          <div class="hp-top">${top}</div>
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th class="num-col">#</th><th>${t("honeypot.when")}</th>
              <th>IP</th><th>${t("honeypot.request")}</th></tr></thead>
            <tbody>${hits.map((h, i) => `
              <tr><td class="num-col">${i + 1}</td>
                  <td class="mono tiny">${h.time}</td>
                  <td class="mono">${h.ip}</td>
                  <td class="mono tiny" dir="ltr">${h.request}</td></tr>`).join("")}
            </tbody></table></div>`;
      } catch (e) {
        el.innerHTML = `<p class="muted tiny">${e.message}</p>`;
      }
    };
    document.getElementById("hp-refresh").onclick = loadHits;
    if (hp.enabled) loadHits();
  },
};

})();
