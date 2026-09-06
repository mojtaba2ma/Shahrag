/* Logs page.

   Two things this page has to get right:

   1. Readability. Raw nginx output ran together as one dense block, so the
      start and end of an entry were impossible to see. Each entry is now its
      own row: time first, the date beside it smaller and dimmer, the message
      on its own line, and a separator between entries.

   2. Explanations belong WITH the line they explain, not in a banner at the
      top of the page. A "Tips" level collects the panel's own notes, and each
      note is rendered directly under the entry that triggered it. Selecting
      the Tips level shows only the entries that produced one. */
window.Pages = window.Pages || {};

// See the note in reality.js: plain <script> loading makes a top-level
// `const` global, so every page module keeps its names inside an IIFE.
(function () {
"use strict";

const LEVELS = [
  { id: "all", label: "All" },
  { id: "error", label: "Error" },
  { id: "warn", label: "Warning" },
  { id: "notice", label: "Notice" },
  { id: "tip", label: "Tips" },
];

window.Pages.logs = {
  async render(container, state, ctx) {
    const { api, t, Icons, toast } = ctx;

    const load = () => Promise.all([
      api("/api/logs/http?lines=200"),
      api("/api/logs/stream?lines=200").catch(() => ({ content: "" })),
      api("/api/logs/error?lines=200"),
      api("/api/services").catch(() => ({})),
    ]);

    let [http, stream, err, services] = await load();
    let source = "error";
    let level = "all";
    let limit = 50;

    container.innerHTML = `
      <div class="page-header">
        <h1>${Icons.svg("logs", 20)} ${t("logs.title")}</h1>
        <div class="btn-row" style="margin:0">
          <button class="btn btn-ghost btn-sm copy-btn" id="lg-copy-all"
                  data-tip="${t("logs.copy_all_hint")}">
            ${Icons.svg("copy", 16)} <span class="btn-label">${t("logs.copy_all")}</span>
          </button>
          <button class="btn btn-ghost btn-sm" id="refresh">${Icons.svg("refresh", 14)} ${t("stats.refresh")}</button>
        </div>
      </div>
      <div class="card" style="padding:0">
        <div class="log-toolbar">
          <span class="tool" title="${t("logs.level") || "Level"}">
            <span class="tool-icon">${Icons.svg("warning", 14)}</span>
            <select id="lg-level" aria-label="${t("logs.level") || "Level"}">
              ${LEVELS.map(l => `<option value="${l.id}">${l.label}</option>`).join("")}
            </select>
          </span>
          <span class="tool" title="${t("logs.source") || "Source"}">
            <span class="tool-icon">${Icons.svg("logs", 14)}</span>
            <select id="lg-source" aria-label="${t("logs.source") || "Source"}">
              <option value="error" selected>Error</option>
              <option value="http">HTTP</option>
              <option value="stream">Stream</option>
            </select>
          </span>
          <span class="tool" title="${t("logs.lines") || "Lines"}">
            <span class="tool-icon">${Icons.svg("stats", 14)}</span>
            <select id="lg-limit" aria-label="${t("logs.lines") || "Lines"}">
              ${[20, 50, 100, 200].map(n => `<option value="${n}" ${n === limit ? "selected" : ""}>${n}</option>`).join("")}
            </select>
          </span>
        </div>
      </div>
      <div class="card" style="padding:0;overflow:hidden">
        <div class="log-list" id="lg-list"></div>
      </div>`;

    const draw = () => {
      const raw = { http: http.content, stream: stream.content, error: err.content }[source] || "";
      const entries = parseLog(raw, services, t);
      const shown = entries
        .filter(e => level === "all" ? true : (level === "tip" ? !!e.tip : e.level === level))
        .slice(-limit)
        .reverse();

      const list = document.getElementById("lg-list");
      if (!shown.length) {
        list.innerHTML = `<div class="log-empty">${t("logs.empty")}</div>`;
        return;
      }
      list.innerHTML = shown.map(e => `
        <div class="log-entry">
          <div class="log-head">
            <span class="log-time">${e.time || "—"}</span>
            <span class="log-date">${e.date || ""}</span>
            <button class="btn btn-ghost icon-btn copy-btn log-copy"
                    data-copy="${escapeAttr(entryText(e))}"
                    aria-label="${t("logs.copy_one")}"
                    data-tip="${t("logs.copy_one")}">${Icons.svg("copy", 16)}</button>
            <span class="log-level ${e.level}">${e.level}</span>
          </div>
          <div class="log-msg">${escapeHTML(e.msg)}</div>
          ${e.tip ? `<div class="log-tip">
              <span class="log-tip-label">${t("logs.tip") || "Tip"}</span>${e.tip}
            </div>` : ""}
        </div>`).join("");

      // The "copy everything" button carries exactly what is on screen —
      // the same filter, the same limit, the same order. Copying the whole
      // unfiltered file would be a different thing from what was asked for.
      const all = document.getElementById("lg-copy-all");
      if (all) {
        all.dataset.copy = shown.map(entryText).join("\n");
        all.disabled = false;
      }

      // Buttons are recreated on every draw, so they must be re-wired; the
      // helper skips anything it has already handled.
      if (window.ShahragWireCopy) window.ShahragWireCopy(list, t, toast);
      if (all && window.ShahragWireCopy) {
        window.ShahragWireCopy(all.parentElement, t, toast);
      }
    };

    draw();

    document.getElementById("lg-level").onchange = e => { level = e.target.value; draw(); };
    document.getElementById("lg-source").onchange = e => { source = e.target.value; draw(); };
    document.getElementById("lg-limit").onchange = e => { limit = +e.target.value; draw(); };

    // Re-fetch in place. location.reload() threw the whole SPA away, which on
    // a panel served under /<path>/ looked exactly like being logged out.
    document.getElementById("refresh").onclick = async (ev) => {
      const btn = ev.currentTarget;
      btn.disabled = true;
      try {
        [http, stream, err, services] = await load();
        draw();
      } catch (e) { toast(e.message, "error"); }
      btn.disabled = false;
    };
  },
};

/* entryText renders one entry the way a human would paste it into a bug
   report: the timestamp, the level, the message, and the panel's own note
   when there is one. The note is included deliberately — it is usually the
   most useful line for whoever is being asked for help. */
function entryText(e) {
  const when = [e.date, e.time].filter(Boolean).join(" ");
  const head = [when, e.level ? "[" + e.level + "]" : ""].filter(Boolean).join(" ");
  let out = (head ? head + " " : "") + e.msg;
  // The tip is now markup (translated text with bidi-isolated <code> runs),
  // so it has to be flattened back to plain text before it goes on the
  // clipboard — pasting "<code>" into a chat message would be nonsense.
  if (e.tip) out += "\n    note: " + stripHTML(e.tip);
  return out;
}

/* stripHTML turns our own tip markup back into readable plain text. The
   input is built by this file, never by a remote source, so parsing it
   through a detached element is safe and gives correct entity decoding
   for free. */
function stripHTML(html) {
  const d = document.createElement("div");
  d.innerHTML = String(html);
  return (d.textContent || "").replace(/\s+/g, " ").trim();
}

/* An attribute needs the quote escaped as well, and log lines are full of
   them ("GET /take HTTP/1.1"). Without this the value is cut short at the
   first quote and the copy silently returns a fragment. */
function escapeAttr(s) {
  return String(s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* parseLog turns raw nginx output into structured entries.

   nginx error format:  2026/08/21 12:10:57 [error] 792#792: *283 message...
   nginx access format: 1.2.3.4 - - [21/Aug/2026:12:10:57 +0000] "GET / ..." */
function parseLog(raw, services, t) {
  const byPort = {};
  for (const [name, s] of Object.entries(services || {})) {
    if (s && typeof s.local_port === "number") byPort[s.local_port] = name;
  }

  const out = [];
  for (const line of String(raw).split("\n")) {
    if (!line.trim()) continue;

    let date = "", time = "", level = "info", msg = line;

    // Error-log style.
    let m = line.match(/^(\d{4})\/(\d{2})\/(\d{2})\s+(\d{2}:\d{2}:\d{2})\s+\[(\w+)\]\s*(.*)$/);
    if (m) {
      date = `${m[1]}/${m[2]}/${m[3]}`;
      time = m[4];
      level = normalizeLevel(m[5]);
      msg = m[6];
    } else {
      // Access-log style: [21/Aug/2026:12:10:57 +0000]
      m = line.match(/\[(\d{2})\/(\w{3})\/(\d{4}):(\d{2}:\d{2}:\d{2})/);
      if (m) {
        date = `${m[3]}/${m[2]}/${m[1]}`;
        time = m[4];
        const st = line.match(/"\s(\d{3})\s/);
        level = st ? (st[1][0] === "5" ? "error" : st[1][0] === "4" ? "warn" : "notice") : "notice";
      }
    }

    out.push({ date, time, level, msg, tip: tipFor(msg, byPort, t) });
  }
  return out;
}

function normalizeLevel(l) {
  l = String(l).toLowerCase();
  if (l === "emerg" || l === "alert" || l === "crit" || l === "error") return "error";
  if (l === "warn") return "warn";
  if (l === "notice") return "notice";
  return "info";
}

/* tipFor returns the panel's own explanation for a known nginx message.

   The advice is TRANSLATED; the log line itself is not. That split is
   deliberate: the log text is nginx's own output and must stay verbatim so
   it can be searched for, pasted into a bug report and matched against
   upstream documentation. The explanation is the panel talking to its
   operator, so it belongs in the operator's language.

   Every branch returns a key plus its substitutions rather than a finished
   sentence, so the same logic serves all ten languages. */
function tipFor(msg, byPort, t) {
  // The template comes from the translation file and is trusted; the values
  // substituted into it come from the LOG LINE and are not, so each is
  // escaped before it goes in. Each is also wrapped in <code>, which is
  // bidi-isolated in CSS: a Latin command inside a right-to-left sentence
  // otherwise has its punctuation reordered, turning
  // "ss -ltnp | grep :3000" into ":ss -ltnp | grep 3000".
  const tr = (key, vars) => {
    let out = t("logs.tip_" + key);
    // A missing key falls back to the dotted path; showing that raw would
    // be worse than showing nothing.
    if (out === "logs.tip_" + key) return "";
    out = escapeHTML(out);
    for (const k in (vars || {})) {
      out = out.split("%" + k).join(`<code>${escapeHTML(String(vars[k]))}</code>`);
    }
    return out;
  };

  const up = msg.match(/upstream:\s*"https?:\/\/(?:127\.0\.0\.1|localhost):(\d+)/);
  const port = up ? up[1] : null;
  const svc = port && byPort[+port];

  if (/connect\(\) failed|no live upstreams/.test(msg)) {
    const who = svc ? `${svc}:${port}` : (port ? String(port) : "?");
    const local = /client:\s*(?:127\.0\.0\.1|::1)/.test(msg);
    return tr("upstream_down", { s: who, p: port || "PORT" })
      + " " + tr(local ? "upstream_down_local" : "upstream_down_real")
      + " " + tr("check_with", { c: "ss -ltnp | grep :" + (port || "PORT") });
  }
  if (/conflicting server name/.test(msg)) {
    return tr("conflicting") + " " + tr("check_with", { c: "sudo shahrag doctor" });
  }
  if (/no resolver defined/.test(msg)) {
    return tr("no_resolver");
  }
  if (/Address already in use|bind\(\) to/.test(msg)) {
    return tr("port_taken") + " " + tr("check_with", { c: "sudo shahrag doctor" });
  }
  // recv() failed (104) is by far the most common line in a busy proxy's
  // log and is almost always harmless: 104 is the peer hanging up. On an
  // UPGRADED connection that is simply how a WebSocket ends.
  if (/recv\(\) failed \(104/.test(msg)) {
    const upgraded = /upgraded connection/.test(msg);
    let out = tr(upgraded ? "reset_upgraded" : "reset_plain");
    out += " " + tr("reset_common");
    if (svc) out += " " + tr("reset_backend", { s: svc, p: port });
    return out + " " + tr("reset_when");
  }
  // "bad key share" is a TLS parameter mismatch settled BEFORE any
  // certificate is used, so blaming the certificate was simply wrong.
  if (/bad key share|no shared cipher|unsupported protocol|wrong version number/i.test(msg)) {
    return tr("tls_mismatch");
  }
  if (/SSL_do_handshake|handshake failed|certificate/i.test(msg)) {
    return tr("tls_cert");
  }
  if (/worker_connections exceed/.test(msg)) {
    return tr("worker_conns") + " " + tr("check_with", { c: "sudo shahrag boot-guard" });
  }
  return "";
}

})();
