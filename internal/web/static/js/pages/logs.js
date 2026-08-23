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
        <button class="btn btn-ghost btn-sm" id="refresh">${Icons.svg("refresh", 14)} ${t("stats.refresh")}</button>
      </div>
      <div class="card">
        <div class="field-row" style="align-items:end">
          <div class="field field-port" style="max-width:12ch">
            <label>${t("logs.lines") || "Lines"}</label>
            <select id="lg-limit">
              ${[20, 50, 100, 200].map(n => `<option value="${n}" ${n === limit ? "selected" : ""}>${n}</option>`).join("")}
            </select>
          </div>
          <div class="field">
            <label>${t("logs.level") || "Level"}</label>
            <select id="lg-level">
              ${LEVELS.map(l => `<option value="${l.id}">${l.label}</option>`).join("")}
            </select>
          </div>
          <div class="field">
            <label>${t("logs.source") || "Source"}</label>
            <select id="lg-source">
              <option value="error" selected>Error</option>
              <option value="http">HTTP</option>
              <option value="stream">Stream</option>
            </select>
          </div>
        </div>
      </div>
      <div class="card" style="padding:0;overflow:hidden">
        <div class="log-list" id="lg-list"></div>
      </div>`;

    const draw = () => {
      const raw = { http: http.content, stream: stream.content, error: err.content }[source] || "";
      const entries = parseLog(raw, services);
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
            <span class="log-level ${e.level}">${e.level}</span>
          </div>
          <div class="log-msg">${escapeHTML(e.msg)}</div>
          ${e.tip ? `<div class="log-tip">
              <span class="log-tip-label">${t("logs.tip") || "Tip"}</span>${escapeHTML(e.tip)}
            </div>` : ""}
        </div>`).join("");
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

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* parseLog turns raw nginx output into structured entries.

   nginx error format:  2026/08/21 12:10:57 [error] 792#792: *283 message...
   nginx access format: 1.2.3.4 - - [21/Aug/2026:12:10:57 +0000] "GET / ..." */
function parseLog(raw, services) {
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

    out.push({ date, time, level, msg, tip: tipFor(msg, byPort) });
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

/* tipFor returns the panel's own explanation for a known nginx message, so
   the advice sits next to the line it is about. */
function tipFor(msg, byPort) {
  const m = msg.match(/upstream:\s*"https?:\/\/(?:127\.0\.0\.1|localhost):(\d+)/);
  if (/connect\(\) failed|no live upstreams/.test(msg)) {
    const port = m ? m[1] : null;
    const svc = port && byPort[+port];
    const who = svc ? `service "${svc}" (port ${port})` : (port ? `port ${port}` : "the backend");
    const local = /client:\s*(?:127\.0\.0\.1|::1)/.test(msg);
    return `nginx routed this correctly but nothing was listening on ${who}, `
      + `so the backend was down — this is not an nginx or panel fault. `
      + (local
        ? `The request came from 127.0.0.1, i.e. a local probe such as "shahrag selftest", not a real visitor. `
        : `The request came from a real client, so a user was affected. `)
      + `Check it with: ss -ltnp | grep :${port || "PORT"}`;
  }
  if (/conflicting server name/.test(msg)) {
    return "nginx keeps the FIRST block claiming a hostname and ignores the rest, "
      + "so the services in the ignored block serve the fake page. Shahrag's own "
      + "files never collide, so a leftover config is still being loaded. "
      + "Find it with: sudo shahrag doctor";
  }
  if (/no resolver defined/.test(msg)) {
    return "A pass-through SNI rule forwards to a hostname taken from a variable, "
      + "which nginx can only resolve when a resolver is configured. Set the DNS "
      + "resolvers on the SNI routing page and regenerate.";
  }
  if (/Address already in use|bind\(\) to/.test(msg)) {
    return "Another process already holds this port, so nginx cannot bind it. "
      + "`nginx -t` cannot detect this. Identify the owner with: sudo shahrag doctor";
  }
  if (/SSL_do_handshake|handshake failed|certificate/i.test(msg)) {
    return "A TLS handshake failed. Check that the certificate and key for this "
      + "domain exist and match, on the Domains page.";
  }
  if (/worker_connections exceed/.test(msg)) {
    return "worker_connections is higher than the process file-descriptor limit. "
      + "Fix it with: sudo shahrag boot-guard";
  }
  return "";
}

})();
