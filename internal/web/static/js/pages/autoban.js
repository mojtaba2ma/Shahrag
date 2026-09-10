/* Automatic banning.

   Four independent rules, each with its own threshold, window and ban
   length, plus the list of who is currently banned and why. The page has to
   make two things clear: which rules are safe to switch on (the honeypot is
   conclusive; a 404 flood is not), and that the default response is a quiet
   404 rather than a refusal that makes the server conspicuous. */
window.Pages = window.Pages || {};

(function () {
"use strict";

const RULES = ["honeypot", "auth_fail", "not_found", "error_rate"];

function fmtList(v) { return (v || []).join(", "); }
function parseList(s) {
  return String(s || "").split(/[,\n]/).map(x => x.trim()).filter(Boolean);
}

/* A ban length is a number of minutes, but "permanent" is -1 and "use the
   default" is 0. Presenting that raw would be cryptic, so the form uses a
   readable duration picker and this converts. */
const DURATIONS = [
  [15, "15m"], [60, "1h"], [240, "4h"], [720, "12h"],
  [1440, "24h"], [10080, "7d"], [-1, "∞"],
];

function durationOptions(current, t) {
  return DURATIONS.map(([v, label]) => {
    const text = v === -1 ? t("autoban.forever") : label;
    return `<option value="${v}" ${v === current ? "selected" : ""}>${text}</option>`;
  }).join("");
}

function ruleRow(id, r, t, Icons) {
  const on = !!r.enabled;
  return `
    <div class="ban-rule ${on ? "" : "off"}" data-rule="${id}">
      <div class="ban-rule-head">
        <label class="checkbox">
          <input type="checkbox" id="r-${id}-on" ${on ? "checked" : ""}>
          <span class="check-box"></span>
          <span class="ban-rule-name">${t("autoban.rule_" + id)}</span>
        </label>
        ${Icons.help(t("autoban.rule_" + id + "_help"))}
      </div>
      <div class="ban-rule-body">
        <div class="field field-port">
          <label class="tiny">${t("autoban.hits")}</label>
          <input id="r-${id}-hits" type="number" inputmode="numeric" min="1" max="10000"
                 value="${r.hits || 1}">
        </div>
        <div class="field field-port">
          <label class="tiny">${t("autoban.window")}</label>
          <input id="r-${id}-window" type="number" inputmode="numeric" min="1" max="1440"
                 value="${r.window_minutes || 10}">
        </div>
        <div class="field field-port">
          <label class="tiny">${t("autoban.duration")}</label>
          <select id="r-${id}-ban">${durationOptions(r.ban_minutes || 240, t)}</select>
        </div>
      </div>
    </div>`;
}

window.Pages.autoban = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, navigate, confirmDialog } = ctx;
    const ab = await api("/api/autoban");

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("lock", 20)} ${t("autoban.title")}</h1>
        <span class="badge ${ab.ban_count ? "badge-danger" : "badge-neutral"}"
              id="ab-count">${ab.ban_count} ${t("autoban.banned_now")}</span>
      </div>

      <div class="card">
        <p class="muted hp-lede">${t("autoban.lede")}</p>

        <label class="switch">
          <input type="checkbox" id="ab-on" ${ab.enabled ? "checked" : ""}>
          <span class="switch-track"><span class="switch-thumb"></span></span>
          <span>${t("autoban.enable")}</span>
        </label>

        <div id="ab-body" ${ab.enabled ? "" : "hidden"}>
          <div class="field field-wide">
            <label>${t("autoban.action")}${Icons.help(t("autoban.action_help"))}</label>
            <select id="ab-action">
              <option value="notfound" ${ab.action === "notfound" ? "selected" : ""}>${t("autoban.action_notfound")}</option>
              <option value="throttle" ${ab.action === "throttle" ? "selected" : ""}>${t("autoban.action_throttle")}</option>
              <option value="forbidden" ${ab.action === "forbidden" ? "selected" : ""}>${t("autoban.action_forbidden")}</option>
            </select>
            <p class="tiny muted" id="ab-action-note"></p>
          </div>

          <div class="field field-port" id="ab-rate-field">
            <label>${t("autoban.throttle_rate")}${Icons.help(t("autoban.throttle_rate_help"))}</label>
            <input id="ab-rate" type="number" inputmode="numeric" min="1" max="600"
                   value="${ab.throttle_rate || 2}">
          </div>

          <div class="tiny ban-rules-label">${t("autoban.rules")}</div>
          ${RULES.map(id => ruleRow(id, ab[id] || {}, t, Icons)).join("")}

          <div class="field field-wide">
            <label>${t("autoban.allow_ips")}${Icons.help(t("autoban.allow_ips_help"))}</label>
            <input id="ab-allow" dir="ltr" class="mono" value="${fmtList(ab.allow_ips)}"
                   placeholder="10.0.0.0/24, 192.168.1.5">
          </div>

          <!-- Defaults ON the first time the form is opened, then follows
               whatever was saved. The table above only lists bans that are
               still active, so without this file a ban that expired
               overnight cannot be investigated at all. -->
          <label class="checkbox">
            <input type="checkbox" id="ab-log" ${ab.log_bans ? "checked" : ""}>
            <span class="check-box"></span>
            <span>${t("autoban.log")}</span>${Icons.help(t("autoban.log_help"))}
          </label>
        </div>

        <!-- Outside the collapsible body: switching the feature OFF hides
             that body, and the button has to stay reachable to save it. -->
        <div class="btn-row">
          <button class="btn btn-primary" id="ab-save">
            ${Icons.svg("check", 15)} ${t("common.save")}
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <h3 class="card-title">${Icons.svg("shield", 16)} ${t("autoban.current")}</h3>
          <div class="btn-row" style="margin:0">
            <button class="btn btn-ghost btn-sm" id="ab-add">
              ${Icons.svg("plus", 14)} <span class="btn-label">${t("autoban.add")}</span>
            </button>
            <button class="btn btn-ghost btn-sm" id="ab-refresh">
              ${Icons.svg("refresh", 14)} <span class="btn-label">${t("stats.refresh")}</span>
            </button>
            <button class="btn btn-danger btn-sm" id="ab-clear">
              ${Icons.svg("trash", 14)} <span class="btn-label">${t("autoban.clear")}</span>
            </button>
          </div>
        </div>
        <div id="ab-list"></div>
      </div>

      <!-- The durable record. Separate card from the live table on
           purpose: they answer different questions and mixing them is
           what makes an expired ban impossible to look up. -->
      <div class="card" id="ab-hist-card">
        <div class="card-head">
          <h3 class="card-title">${Icons.svg("logs", 16)} ${t("autoban.history")}</h3>
          <button class="btn btn-ghost btn-sm" id="ab-hist-refresh">
            ${Icons.svg("refresh", 14)} <span class="btn-label">${t("stats.refresh")}</span>
          </button>
        </div>
        <p class="tiny muted">${t("autoban.history_help")}</p>
        <div id="ab-hist"></div>
      </div>`;

    const on = document.getElementById("ab-on");
    const body = document.getElementById("ab-body");
    const action = document.getElementById("ab-action");
    const note = document.getElementById("ab-action-note");

    const syncAction = () => {
      note.textContent = t("autoban.note_" + action.value);
      note.className = "tiny " + (action.value === "forbidden" ? "hp-warn" : "muted");
      document.getElementById("ab-rate-field").hidden = action.value !== "throttle";
    };
    action.onchange = syncAction;
    syncAction();

    on.onchange = () => { body.hidden = !on.checked; };

    // A rule's fields are meaningless while the rule is off; grey them.
    RULES.forEach(id => {
      const cb = document.getElementById(`r-${id}-on`);
      const row = container.querySelector(`[data-rule="${id}"]`);
      cb.onchange = () => row.classList.toggle("off", !cb.checked);
    });

    document.getElementById("ab-save").onclick = async (ev) => {
      const btn = ev.currentTarget;
      btn.disabled = true;
      try {
        const payload = {
          enabled: on.checked,
          action: action.value,
          throttle_rate: +document.getElementById("ab-rate").value || 0,
          allow_ips: parseList(document.getElementById("ab-allow").value),
          log_bans: document.getElementById("ab-log").checked,
        };
        RULES.forEach(id => {
          payload[id] = {
            enabled: document.getElementById(`r-${id}-on`).checked,
            hits: +document.getElementById(`r-${id}-hits`).value || 1,
            window_minutes: +document.getElementById(`r-${id}-window`).value || 10,
            ban_minutes: +document.getElementById(`r-${id}-ban`).value,
          };
        });
        const res = await api("/api/autoban", { method: "PUT", body: JSON.stringify(payload) });
        if (res.applied === false) {
          toast(t("services.apply_failed") + (res.apply_error ? ": " + res.apply_error : ""), "error");
        } else {
          toast(t("settings.saved"), "success");
        }
        navigate("autoban");
      } catch (e) {
        toast(e.message, "error");
        btn.disabled = false;
      }
    };

    const reasonLabel = r => t("autoban.reason_" + r) || r;

    const loadBans = async () => {
      const el = document.getElementById("ab-list");
      try {
        const r = await api("/api/autoban/bans");
        if (!r.running) {
          el.innerHTML = `<p class="muted tiny">${t("autoban.not_running")}</p>`;
          return;
        }
        const bans = r.bans || [];
        document.getElementById("ab-count").textContent =
          bans.length + " " + t("autoban.banned_now");
        if (!bans.length) {
          el.innerHTML = `<p class="muted tiny">${t("autoban.none")}</p>`;
          return;
        }
        // A searchable, filterable, paginated table with bulk unban.
        // A server under a scan can have thousands of bans, and a flat
        // list of thousands of rows is both unusable and about 40 MB of
        // DOM — see js/listview.js for the measurements.
        window.ListView.create(el, {
          id: "bans",
          rows: bans,
          t, Icons,
          rowKey: b => b.ip,
          empty: t("autoban.none"),
          columns: [
            { key: "ip", label: "autoban.ip", sortable: true, cls: "mono",
              plain: b => b.ip,
              render: b => `<span class="mono" dir="ltr">${b.ip}</span>` },
            { key: "reason", label: "autoban.reason", sortable: true,
              plain: b => reasonLabel(b.reason),
              render: b => `<span class="badge badge-neutral">${reasonLabel(b.reason)}</span>` },
            { key: "hits", label: "autoban.hits", sortable: true, cls: "num",
              plain: b => b.hits || 0,
              render: b => b.hits || "—" },
            // The full timestamp, not just "3h left". A duration cannot be
            // correlated with anything; an exact time can be matched
            // against nginx's own log line for line.
            { key: "banned_at", label: "autoban.banned_at", sortable: true,
              plain: b => b.banned_at || "",
              render: b => `<span class="mono tiny" dir="ltr">${fmtStamp(b.banned_at)}</span>` },
            { key: "remaining_minutes", label: "autoban.remaining", sortable: true,
              plain: b => b.permanent ? 1e12 : (b.remaining_minutes || 0),
              render: b => b.permanent ? t("autoban.forever")
                : `<span dir="ltr">${fmtRemaining(b.remaining_minutes, t)}</span>
                   <span class="tiny muted mono" dir="ltr">${fmtStamp(b.expires_at)}</span>` },
            { key: "_act", label: "autoban.actions", cls: "row-actions",
              plain: () => "",
              render: b => `<button class="btn btn-sm btn-ghost" data-unban="${b.ip}"
                title="${t("autoban.unban")}">${Icons.svg("check", 14)}</button>` },
          ],
          filters: [
            { id: "reason", label: "autoban.filter_reason",
              options: ["honeypot", "auth_fail", "not_found", "error_rate", "manual"]
                .map(r => ({ value: r, label: "autoban.reason_" + r })),
              match: (b, v) => b.reason === v },
            { id: "kind", label: "autoban.filter_kind",
              options: [{ value: "perm", label: "autoban.forever" },
                        { value: "temp", label: "autoban.temporary" }],
              match: (b, v) => v === "perm" ? !!b.permanent : !b.permanent },
          ],
          bulk: [
            { id: "unban", label: "autoban.unban_selected", icon: "check",
              run: (rows, done) => {
                confirmDialog(t("autoban.unban_n").replace("%n", rows.length), async () => {
                  // Sequential rather than parallel: each unban
                  // regenerates nginx, and firing fifty at once would
                  // queue fifty reloads.
                  let n = 0;
                  for (const b of rows) {
                    try {
                      await api("/api/autoban/bans/" + encodeURIComponent(b.ip),
                                { method: "DELETE" });
                      n++;
                    } catch (e) { /* keep going; report the total */ }
                  }
                  toast(t("autoban.unbanned_n").replace("%n", n), "success");
                  done();
                  loadBans();
                });
              } },
          ],
          onRender: (root) => {
            root.querySelectorAll("[data-unban]").forEach(btn => btn.onclick = async () => {
              try {
                await api("/api/autoban/bans/" + encodeURIComponent(btn.dataset.unban),
                          { method: "DELETE" });
                toast(t("autoban.unbanned"), "success");
                loadBans();
              } catch (e) { toast(e.message, "error"); }
            });
          },
        });
      } catch (e) {
        el.innerHTML = `<p class="muted tiny">${e.message}</p>`;
      }
    };

    document.getElementById("ab-refresh").onclick = loadBans;

    document.getElementById("ab-clear").onclick = () => {
      confirmDialog(t("autoban.clear_confirm"), async () => {
        try {
          const r = await api("/api/autoban/bans", { method: "DELETE" });
          toast(t("autoban.cleared").replace("%n", r.removed || 0), "success");
          loadBans();
        } catch (e) { toast(e.message, "error"); }
      });
    };

    document.getElementById("ab-add").onclick = () => {
      ctx.modal(t("autoban.add"), `
        <div class="form-error" id="ab-err" hidden></div>
        <div class="field field-wide">
          <label>IP</label>
          <input id="ab-new-ip" dir="ltr" class="mono" placeholder="203.0.113.5">
        </div>
        <div class="field field-wide">
          <label>${t("autoban.duration")}</label>
          <select id="ab-new-dur">${durationOptions(240, t)}</select>
        </div>`,
        [{ label: t("common.cancel"), class: "btn-ghost" },
         { label: t("common.save"), class: "btn-primary", icon: "check", keepOpen: true,
           onClick: async () => {
             const err = document.getElementById("ab-err");
             err.hidden = true;
             try {
               const mins = +document.getElementById("ab-new-dur").value;
               await api("/api/autoban/bans", {
                 method: "POST",
                 body: JSON.stringify({
                   ip: document.getElementById("ab-new-ip").value.trim(),
                   minutes: mins > 0 ? mins : 0,
                   permanent: mins === -1,
                 }),
               });
               window.closeModal();
               toast(t("settings.saved"), "success");
               loadBans();
             } catch (e) {
               err.textContent = e.message;
               err.hidden = false;
             }
           } }]);
    };

    const loadHistory = async () => {
      const el = document.getElementById("ab-hist");
      try {
        const r = await api("/api/autoban/log?limit=200");
        const ev = r.events || [];
        if (!ev.length) {
          el.innerHTML = `<p class="muted tiny">${t("autoban.no_history")}</p>`;
          return;
        }
        window.ListView.create(el, {
          id: "banhistory",
          rows: ev,
          t, Icons,
          rowKey: e => (e.time || "") + (e.ip || "") + (e.action || ""),
          empty: t("autoban.no_history"),
          columns: [
            { key: "time", label: "honeypot.when", sortable: true,
              plain: e => e.time || "",
              render: e => `<span class="mono tiny" dir="ltr">${fmtStamp(e.time)}</span>` },
            { key: "ip", label: "autoban.ip", sortable: true,
              plain: e => e.ip || "",
              render: e => `<span class="mono" dir="ltr">${e.ip || ""}</span>` },
            { key: "action", label: "autoban.event", sortable: true,
              plain: e => e.action || "",
              render: e => `<span class="badge ${e.action === "ban" ? "badge-danger" : "badge-neutral"}">
                ${t("autoban.act_" + e.action) || e.action}</span>` },
            { key: "reason", label: "autoban.reason", sortable: true,
              plain: e => e.reason || "",
              render: e => `<span class="tiny">${e.reason
                ? (t("autoban.reason_" + e.reason) || e.reason) : "—"}</span>` },
            { key: "until", label: "autoban.until",
              plain: e => e.until || "",
              render: e => `<span class="mono tiny" dir="ltr">${e.until === "forever"
                ? t("autoban.forever") : fmtStamp(e.until)}</span>` },
          ],
          filters: [
            { id: "action", label: "autoban.filter_event",
              options: [{ value: "ban", label: "autoban.act_ban" },
                        { value: "unban", label: "autoban.act_unban" }],
              match: (e, v) => e.action === v },
            { id: "reason", label: "autoban.filter_reason",
              options: ["honeypot", "auth_fail", "not_found", "error_rate", "manual"]
                .map(r => ({ value: r, label: "autoban.reason_" + r })),
              match: (e, v) => e.reason === v },
          ],
        });
      } catch (e) {
        el.innerHTML = `<p class="muted tiny">${e.message}</p>`;
      }
    };
    document.getElementById("ab-hist-refresh").onclick = loadHistory;

    loadBans();
    loadHistory();
  },
};

/* A stored ISO timestamp, shown as "2026-09-10 14:03".

   Seconds and the timezone offset are dropped: they make the column twice
   as wide and nobody correlates a ban to the second. The full value stays
   in the title attribute for anyone who does. */
function fmtStamp(iso) {
  if (!iso || iso === "-" || iso === "forever") return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const p = n => String(n).padStart(2, "0");
  return `<span title="${iso}">${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
         `${p(d.getHours())}:${p(d.getMinutes())}</span>`;
}

function fmtRemaining(mins, t) {
  if (mins == null) return "—";
  if (mins < 60) return mins + "m";
  if (mins < 1440) return Math.round(mins / 60) + "h";
  return Math.round(mins / 1440) + "d";
}

})();
