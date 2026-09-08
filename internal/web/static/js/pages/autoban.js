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
        el.innerHTML = `
          <div class="table-wrap"><table class="data-table">
            <thead><tr>
              <th class="num-col">#</th><th>IP</th><th>${t("autoban.reason")}</th>
              <th>${t("autoban.hits")}</th><th>${t("autoban.remaining")}</th><th></th>
            </tr></thead>
            <tbody>${bans.map((b, i) => `
              <tr>
                <td class="num-col">${i + 1}</td>
                <td class="mono">${b.ip}</td>
                <td><span class="badge badge-neutral">${reasonLabel(b.reason)}</span></td>
                <td class="num">${b.hits || "—"}</td>
                <td>${b.permanent ? t("autoban.forever")
                       : fmtRemaining(b.remaining_minutes, t)}</td>
                <td class="row-actions">
                  <button class="btn btn-sm btn-ghost" data-unban="${b.ip}"
                          title="${t("autoban.unban")}">${Icons.svg("check", 14)}</button>
                </td>
              </tr>`).join("")}
            </tbody></table></div>`;
        el.querySelectorAll("[data-unban]").forEach(btn => btn.onclick = async () => {
          try {
            await api("/api/autoban/bans/" + encodeURIComponent(btn.dataset.unban),
                      { method: "DELETE" });
            toast(t("autoban.unbanned"), "success");
            loadBans();
          } catch (e) { toast(e.message, "error"); }
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
        el.innerHTML = `
          <div class="table-wrap"><table class="data-table">
            <thead><tr><th class="num-col">#</th><th>${t("honeypot.when")}</th>
              <th>IP</th><th>${t("autoban.event")}</th>
              <th>${t("autoban.reason")}</th><th>${t("autoban.until")}</th></tr></thead>
            <tbody>${ev.map((e, i) => `
              <tr>
                <td class="num-col">${i + 1}</td>
                <td class="mono tiny" dir="ltr">${e.time || ""}</td>
                <td class="mono">${e.ip || ""}</td>
                <td><span class="badge ${e.action === "ban" ? "badge-danger" : "badge-neutral"}">
                  ${t("autoban.act_" + e.action) || e.action}</span></td>
                <td class="tiny">${e.reason ? (t("autoban.reason_" + e.reason) || e.reason) : "—"}</td>
                <td class="mono tiny" dir="ltr">${e.until === "forever" ? t("autoban.forever") : (e.until || "—")}</td>
              </tr>`).join("")}
            </tbody></table></div>`;
      } catch (e) {
        el.innerHTML = `<p class="muted tiny">${e.message}</p>`;
      }
    };
    document.getElementById("ab-hist-refresh").onclick = loadHistory;

    loadBans();
    loadHistory();
  },
};

function fmtRemaining(mins, t) {
  if (mins == null) return "—";
  if (mins < 60) return mins + "m";
  if (mins < 1440) return Math.round(mins / 60) + "h";
  return Math.round(mins / 1440) + "d";
}

})();
