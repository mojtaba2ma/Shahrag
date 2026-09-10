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
        <div class="field field-port rule-dur">
          <label class="tiny">${t("autoban.duration")}</label>
          <select id="r-${id}-ban">${durationOptions(r.ban_minutes || 240, t)}</select>
          <p class="tiny muted rule-dur-note" hidden>${t("autoban.duration_by_ladder")}</p>
        </div>
      </div>
    </div>`;
}


/* The escalation ladder editor.

   Each step is a duration and a position. The position is the whole point:
   step 1 is what a first-time offender gets, step 5 is what an address
   that has come back five times gets. Rendering them as a numbered list
   rather than a set of fields is what makes that readable without a
   paragraph of explanation.

   Durations are entered as a number plus a unit rather than raw minutes.
   "43200" is unreadable; "30 days" is not, and the alternative — a fixed
   dropdown — cannot express the value somebody actually wants. */
const UNITS = [["m", 1], ["h", 60], ["d", 1440]];

function splitDuration(mins) {
  if (mins < 0) return { perm: true, n: 1, u: "h" };
  for (const [u, mult] of [["d", 1440], ["h", 60], ["m", 1]]) {
    if (mins >= mult && mins % mult === 0) return { perm: false, n: mins / mult, u };
  }
  return { perm: false, n: mins || 1, u: "m" };
}

function stepRow(i, mins, t) {
  const d = splitDuration(mins);
  return `
    <div class="esc-step" data-step="${i}">
      <span class="esc-num">${i + 1}</span>
      <div class="esc-inputs ${d.perm ? "off" : ""}">
        <input class="esc-n" type="number" inputmode="numeric" min="1" max="525600"
               value="${d.n}" ${d.perm ? "disabled" : ""}>
        <select class="esc-u" ${d.perm ? "disabled" : ""}>
          ${UNITS.map(([u]) => `<option value="${u}" ${u === d.u ? "selected" : ""}>${t("autoban.unit_" + u)}</option>`).join("")}
        </select>
      </div>
      <label class="checkbox esc-perm-wrap">
        <input type="checkbox" class="esc-perm" ${d.perm ? "checked" : ""}>
        <span class="check-box"></span>
        <span class="tiny">${t("autoban.forever")}</span>
      </label>
      <button class="btn btn-ghost btn-sm esc-del" type="button"
              title="${t("common.delete")}">&times;</button>
    </div>`;
}

/* Read the ladder back out of the DOM.

   Done by reading the rendered rows rather than keeping a parallel array
   in JS: with add, delete and edit all mutating the list, two copies of
   the truth is how a row ends up saved with the value of the one above
   it. The DOM is the single copy. */
function readSteps(root) {
  return Array.from(root.querySelectorAll(".esc-step")).map(row => {
    if (row.querySelector(".esc-perm").checked) return -1;
    const n = +row.querySelector(".esc-n").value || 1;
    const u = row.querySelector(".esc-u").value;
    const mult = (UNITS.find(x => x[0] === u) || ["m", 1])[1];
    return n * mult;
  });
}

function fmtStepLabel(mins, t) {
  if (mins < 0) return t("autoban.forever");
  const d = splitDuration(mins);
  // A narrow no-break space, not a plain one: it keeps "30 minutes" from
  // being split across a line while still separating the number from a
  // word-length unit like "دقیقه", which read as "30دقیقه" without it.
  return d.n + "\u202f" + t("autoban.unit_" + d.u);
}

window.Pages.autoban = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast, navigate, confirmDialog } = ctx;
    const ab = await api("/api/autoban");
    const esc = ab.escalation || { enabled: true, steps: [30, 120, 480, 1440, 10080, 43200, -1], decay_hours: 72 };
    const DEFAULT_STEPS = ((ab.defaults || {}).escalation || {}).steps
      || [30, 120, 480, 1440, 10080, 43200, -1];

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

          <!-- Progressive banning. Placed directly under the rules
               because it changes what every one of them does: the rule
               decides WHETHER to ban, this decides FOR HOW LONG. -->
          <div class="esc-card" id="ab-esc">
            <label class="checkbox">
              <input type="checkbox" id="esc-on" ${esc.enabled ? "checked" : ""}>
              <span class="check-box"></span>
              <span class="ban-rule-name">${t("autoban.esc_enable")}</span>
            </label>
            <p class="tiny muted">${t("autoban.esc_lede")}</p>
            <div id="esc-body" ${esc.enabled ? "" : "hidden"}>
              <div class="tiny esc-label">${t("autoban.esc_steps")}${Icons.help(t("autoban.esc_steps_help"))}</div>
              <div id="esc-steps">
                ${(esc.steps || []).map((m, i) => stepRow(i, m, t)).join("")}
              </div>
              <div class="btn-row">
                <button class="btn btn-ghost btn-sm" type="button" id="esc-add">
                  ${Icons.svg("plus", 14)} <span class="btn-label">${t("autoban.esc_add_step")}</span>
                </button>
                <button class="btn btn-ghost btn-sm" type="button" id="esc-reset">
                  ${Icons.svg("refresh", 14)} <span class="btn-label">${t("autoban.esc_reset")}</span>
                </button>
              </div>
              <div class="field field-port">
                <label>${t("autoban.esc_decay")}${Icons.help(t("autoban.esc_decay_help"))}</label>
                <input id="esc-decay" type="number" inputmode="numeric" min="1" max="8760"
                       value="${esc.decay_hours || 72}">
              </div>
              <p class="tiny" id="esc-warn" hidden></p>
              <p class="tiny muted" id="esc-preview"></p>
            </div>
          </div>

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
      </div>

      <!-- The escalation ledger. This is what makes the ladder
           explicable: it says which rung each address is on and what its
           next ban would cost, including addresses that are not banned
           right now. -->
      <div class="card" id="ab-off-card">
        <div class="card-head">
          <h3 class="card-title">${Icons.svg("shield", 16)} ${t("autoban.offenders")}</h3>
          <div class="btn-row" style="margin:0">
            <button class="btn btn-ghost btn-sm" id="ab-off-refresh">
              ${Icons.svg("refresh", 14)} <span class="btn-label">${t("stats.refresh")}</span>
            </button>
            <button class="btn btn-danger btn-sm" id="ab-off-clear">
              ${Icons.svg("trash", 14)} <span class="btn-label">${t("autoban.forgive_all")}</span>
            </button>
          </div>
        </div>
        <p class="tiny muted">${t("autoban.offenders_help")}</p>
        <div id="ab-off"></div>
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

    /* Escalation wiring. */
    const escOn = document.getElementById("esc-on");
    const escBody = document.getElementById("esc-body");
    const escSteps = document.getElementById("esc-steps");
    const escWarn = document.getElementById("esc-warn");
    const escPreview = document.getElementById("esc-preview");

    /* Renumber and re-preview after any structural change.

       The numbers are positional, so deleting step 2 has to make the old
       step 3 read "2" — otherwise the list says an address's second
       offence gets the third punishment, which is not what will happen. */
    const syncSteps = () => {
      const rows = Array.from(escSteps.querySelectorAll(".esc-step"));
      rows.forEach((row, i) => {
        row.querySelector(".esc-num").textContent = i + 1;
        // A permanent step anywhere but last is unreachable, and the
        // server refuses it. Say so here rather than on save.
        const perm = row.querySelector(".esc-perm").checked;
        row.classList.toggle("bad", perm && i !== rows.length - 1);
        row.querySelector(".esc-inputs").classList.toggle("off", perm);
        row.querySelector(".esc-n").disabled = perm;
        row.querySelector(".esc-u").disabled = perm;
        // One step cannot be deleted: a ladder needs a first rung.
        row.querySelector(".esc-del").disabled = rows.length <= 1;
      });
      const steps = readSteps(escSteps);
      escPreview.textContent = t("autoban.esc_preview")
        .replace("%s", steps.map(m => fmtStepLabel(m, t)).join(" → "));
      // Local mirror of the server's own checks, so the operator finds
      // out while typing rather than after pressing save.
      const msgs = [];
      let prev = 0;
      steps.forEach((m, i) => {
        if (m === -1 && i !== steps.length - 1) msgs.push(t("autoban.esc_err_perm"));
        else if (m > 0 && m < prev) msgs.push(t("autoban.esc_err_order").replace("%n", i + 1));
        if (m > 0) prev = m;
      });
      if (steps[0] >= 1440) msgs.push(t("autoban.esc_warn_first"));
      escWarn.textContent = msgs.join(" ");
      escWarn.hidden = !msgs.length;
      escWarn.className = "tiny hp-warn";
    };

    const bindStepRow = (row) => {
      row.querySelector(".esc-perm").onchange = syncSteps;
      row.querySelector(".esc-n").oninput = syncSteps;
      row.querySelector(".esc-u").onchange = syncSteps;
      row.querySelector(".esc-del").onclick = () => {
        if (escSteps.querySelectorAll(".esc-step").length <= 1) return;
        row.remove();
        syncSteps();
      };
    };
    escSteps.querySelectorAll(".esc-step").forEach(bindStepRow);

    /* Two settings must never both claim to own the same number.

       While the ladder is on it decides every ban's length, so each
       rule's own duration is inert. Leaving it enabled and editable is
       how an operator ends up setting "1 minute" and being told nothing
       when they get thirty — the same class of bug as the ban history
       that reported one default and wrote another. */
    const syncLadderOwnership = () => {
      const on = escOn.checked;
      RULES.forEach(id => {
        const sel = document.getElementById(`r-${id}-ban`);
        // A permanent rule still overrides the ladder, because "never
        // coming back" is an instruction the ladder cannot express, so
        // that option stays live.
        sel.disabled = on && +sel.value !== -1;
        const wrap = sel.closest(".rule-dur");
        wrap.classList.toggle("by-ladder", on);
        wrap.querySelector(".rule-dur-note").hidden = !on;
      });
    };

    escOn.onchange = () => {
      escBody.hidden = !escOn.checked;
      syncLadderOwnership();
    };
    RULES.forEach(id => {
      document.getElementById(`r-${id}-ban`).addEventListener("change", syncLadderOwnership);
    });
    syncLadderOwnership();

    document.getElementById("esc-add").onclick = () => {
      const cur = readSteps(escSteps);
      // A new rung starts at double the last real one, which is the
      // shape of the shipped ladder and is always valid ordering-wise.
      const last = cur.filter(m => m > 0).pop() || 30;
      const rows = escSteps.querySelectorAll(".esc-step").length;
      escSteps.insertAdjacentHTML("beforeend", stepRow(rows, Math.min(last * 2, 525600), t));
      bindStepRow(escSteps.lastElementChild);
      syncSteps();
    };

    document.getElementById("esc-reset").onclick = () => {
      escSteps.innerHTML = DEFAULT_STEPS.map((m, i) => stepRow(i, m, t)).join("");
      escSteps.querySelectorAll(".esc-step").forEach(bindStepRow);
      document.getElementById("esc-decay").value = 72;
      syncSteps();
    };

    syncSteps();

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
          escalation: {
            enabled: escOn.checked,
            steps: readSteps(escSteps),
            decay_hours: +document.getElementById("esc-decay").value || 72,
          },
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
            // The rung this ban was issued at. Without it the table shows
            // two addresses banned for wildly different lengths with no
            // visible reason, which reads as a bug.
            { key: "level", label: "autoban.level", sortable: true, cls: "num",
              plain: b => b.level || 0,
              render: b => b.level
                ? `<span class="badge ${b.level >= 5 ? "badge-danger" : "badge-neutral"}"
                     title="${t("autoban.level_help")}">${b.level}</span>`
                : "—" },
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
            { key: "level", label: "autoban.level", sortable: true, cls: "num",
              plain: e => +e.level || 0,
              render: e => e.level ? `<span class="badge badge-neutral">${e.level}</span>` : "—" },
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

    const loadOffenders = async () => {
      const el = document.getElementById("ab-off");
      try {
        const r = await api("/api/autoban/offenders");
        const rows = r.offenders || [];
        if (!r.enabled) {
          el.innerHTML = `<p class="muted tiny">${t("autoban.esc_off_note")}</p>`;
          return;
        }
        if (!rows.length) {
          el.innerHTML = `<p class="muted tiny">${t("autoban.no_offenders")}</p>`;
          return;
        }
        window.ListView.create(el, {
          id: "offenders",
          rows, t, Icons,
          rowKey: o => o.ip,
          empty: t("autoban.no_offenders"),
          columns: [
            { key: "ip", label: "autoban.ip", sortable: true, cls: "mono",
              plain: o => o.ip,
              render: o => `<span class="mono" dir="ltr">${o.ip}</span>` },
            { key: "level", label: "autoban.level", sortable: true, cls: "num",
              plain: o => o.level,
              render: o => `<span class="badge ${o.level >= 5 ? "badge-danger" : "badge-neutral"}">${o.level}</span>` },
            { key: "next_minutes", label: "autoban.next_ban", sortable: true,
              plain: o => o.next_minutes < 0 ? 1e12 : o.next_minutes,
              render: o => `<span dir="ltr">${o.next_minutes < 0
                ? t("autoban.forever") : fmtStepLabel(o.next_minutes, t)}</span>` },
            { key: "total_bans", label: "autoban.total_bans", sortable: true, cls: "num",
              plain: o => o.total_bans, render: o => o.total_bans },
            { key: "last_ban", label: "autoban.last_ban", sortable: true,
              plain: o => o.last_ban || "",
              render: o => `<span class="mono tiny" dir="ltr">${fmtStamp(o.last_ban)}</span>` },
            { key: "decays_at", label: "autoban.decays_at", sortable: true,
              plain: o => o.decays_at || "",
              render: o => `<span class="mono tiny" dir="ltr">${fmtStamp(o.decays_at)}</span>` },
            { key: "_act", label: "autoban.actions", cls: "row-actions",
              plain: () => "",
              render: o => `<button class="btn btn-sm btn-ghost" data-forgive="${o.ip}"
                title="${t("autoban.forgive")}">${Icons.svg("check", 14)}</button>` },
          ],
          bulk: [
            { id: "forgive", label: "autoban.forgive", icon: "check",
              run: (sel, done) => {
                confirmDialog(t("autoban.forgive_n").replace("%n", sel.length), async () => {
                  for (const o of sel) {
                    try {
                      await api("/api/autoban/offenders/" + encodeURIComponent(o.ip),
                                { method: "DELETE" });
                    } catch (e) { /* keep going */ }
                  }
                  done();
                  loadOffenders();
                });
              } },
          ],
          onRender: (root) => {
            root.querySelectorAll("[data-forgive]").forEach(btn => btn.onclick = async () => {
              try {
                await api("/api/autoban/offenders/" + encodeURIComponent(btn.dataset.forgive),
                          { method: "DELETE" });
                toast(t("autoban.forgiven"), "success");
                loadOffenders();
              } catch (e) { toast(e.message, "error"); }
            });
          },
        });
      } catch (e) {
        el.innerHTML = `<p class="muted tiny">${e.message}</p>`;
      }
    };
    document.getElementById("ab-off-refresh").onclick = loadOffenders;
    document.getElementById("ab-off-clear").onclick = () => {
      confirmDialog(t("autoban.forgive_all_confirm"), async () => {
        try {
          const r = await api("/api/autoban/offenders", { method: "DELETE" });
          toast(t("autoban.forgiven_n").replace("%n", r.forgiven || 0), "success");
          loadOffenders();
        } catch (e) { toast(e.message, "error"); }
      });
    };

    loadBans();
    loadHistory();
    loadOffenders();
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
