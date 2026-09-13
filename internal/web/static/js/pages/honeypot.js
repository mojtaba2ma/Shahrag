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
    // confirmDialog is needed by the bulk ban/release actions added in
    // r52; without it those handlers would throw a ReferenceError the
    // first time anyone used them.
    const { api, t, Icons, toast, navigate, confirmDialog } = ctx;
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

        /* Which of these addresses are already banned.

           Fetched alongside so the table can say so, and so the bulk
           actions can be honest: offering "release" for an address that
           was never banned would report a success that did nothing. */
        let banned = {};
        try {
          const b = await api("/api/autoban/bans");
          (b.bans || []).forEach(x => { banned[x.ip] = x; });
        } catch (e) { /* the ban engine may not be running; not fatal */ }

        // How many times each address appears, so the list can be
        // filtered down to the ones that came back.
        const counts = {};
        hits.forEach(h => { counts[h.ip] = (counts[h.ip] || 0) + 1; });

        el.innerHTML = `<div class="hp-top">${top}</div><div id="hp-table"></div>`;

        window.ListView.create(document.getElementById("hp-table"), {
          id: "honeypot-hits",
          rows: hits, t, Icons,
          // The time plus the request, because one address legitimately
          // appears many times and the IP alone is not a unique row.
          rowKey: h => h.time + "|" + h.ip + "|" + h.request,
          empty: t("honeypot.no_hits"),
          columns: [
            { key: "time", label: "honeypot.when", sortable: true,
              plain: h => h.time,
              render: h => `<span class="mono tiny" dir="ltr">${h.time}</span>` },
            { key: "ip", label: "autoban.ip", sortable: true,
              plain: h => h.ip,
              render: h => `<span class="mono" dir="ltr">${h.ip}</span>` +
                (banned[h.ip]
                  ? ` <span class="badge badge-danger">${t("honeypot.is_banned")}</span>`
                  : "") },
            { key: "count", label: "honeypot.times", sortable: true, cls: "num",
              plain: h => counts[h.ip] || 1,
              render: h => counts[h.ip] || 1 },
            { key: "request", label: "honeypot.request", sortable: true,
              plain: h => h.request,
              render: h => `<code class="mono tiny" dir="ltr">${h.request}</code>` },
          ],
          filters: [
            { id: "state", label: "honeypot.filter_state", icon: "state",
              options: [{ value: "banned", label: "honeypot.is_banned" },
                        { value: "free", label: "honeypot.not_banned" }],
              match: (h, v) => v === "banned" ? !!banned[h.ip] : !banned[h.ip] },
            { id: "repeat", label: "honeypot.filter_repeat", icon: "filter",
              options: [{ value: "repeat", label: "honeypot.repeat" },
                        { value: "once", label: "honeypot.once" }],
              match: (h, v) => v === "repeat"
                ? (counts[h.ip] || 1) > 1 : (counts[h.ip] || 1) === 1 },
          ],
          bulk: [
            /* Ban the selected addresses.

               Deduplicated by address: a scanner appears on twenty rows
               and banning it twenty times would be twenty writes and
               twenty nginx reloads for one outcome. */
            { id: "ban", label: "honeypot.ban_selected", icon: "lock", danger: true,
              run: (sel, done) => {
                const ips = Array.from(new Set(sel.map(h => h.ip)))
                  .filter(ip => !banned[ip]);
                if (!ips.length) {
                  toast(t("honeypot.already_banned"), "error");
                  return;
                }
                confirmDialog(t("honeypot.ban_n").replace("%n", ips.length), async () => {
                  let n = 0;
                  for (const ip of ips) {
                    try {
                      await api("/api/autoban/bans", {
                        method: "POST",
                        body: JSON.stringify({ ip, minutes: 0, reason: "honeypot" }),
                      });
                      n++;
                    } catch (e) { /* keep going */ }
                  }
                  toast(t("honeypot.banned_n").replace("%n", n), "success");
                  done();
                  loadHits();
                });
              } },
            { id: "unban", label: "autoban.unban_selected", icon: "check",
              run: (sel, done) => {
                const ips = Array.from(new Set(sel.map(h => h.ip)))
                  .filter(ip => banned[ip]);
                if (!ips.length) {
                  toast(t("honeypot.none_banned"), "error");
                  return;
                }
                confirmDialog(t("autoban.unban_n").replace("%n", ips.length), async () => {
                  let n = 0;
                  for (const ip of ips) {
                    try {
                      await api("/api/autoban/bans/" + encodeURIComponent(ip),
                                { method: "DELETE" });
                      n++;
                    } catch (e) { /* keep going */ }
                  }
                  toast(t("autoban.unbanned_n").replace("%n", n), "success");
                  done();
                  loadHits();
                });
              } },
          ],
        });
      } catch (e) {
        el.innerHTML = `<p class="muted tiny">${e.message}</p>`;
      }
    };
    document.getElementById("hp-refresh").onclick = loadHits;
    if (hp.enabled) loadHits();
  },
};

})();
