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
            ${Icons.svg("copy", 14)} <span class="btn-label">${t("logs.copy_all")}</span>
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
            <button class="btn btn-ghost btn-sm icon-btn copy-btn log-copy"
                    data-copy="${escapeAttr(entryText(e))}"
                    aria-label="${t("logs.copy_one")}"
                    data-tip="${t("logs.copy_one")}">${Icons.svg("copy", 13)}</button>
          </div>
          <div class="log-msg">${escapeHTML(e.msg)}</div>
          ${e.tip ? `<div class="log-tip">
              <span class="log-tip-label">${t("logs.tip") || "Tip"}</span>${escapeHTML(e.tip)}
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
  if (e.tip) out += "\n    note: " + e.tip;
  return out;
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
      + "resolvers under Settings \u2192 Nginx and regenerate.";
  }
  if (/Address already in use|bind\(\) to/.test(msg)) {
    return "Another process already holds this port, so nginx cannot bind it. "
      + "`nginx -t` cannot detect this. Identify the owner with: sudo shahrag doctor";
  }
  // recv() failed (104: Connection reset by peer) — by far the most common
  // line in a busy proxy's log, and almost always harmless. 104 is the
  // client (or the backend) hanging up. On an UPGRADED connection it is a
  // WebSocket or a proxy tunnel ending, which is how those connections
  // normally end: the user closed the app or changed network.
  //
  // The previous advice for this was to check the certificate, which sent
  // people looking for a fault that is not there.
  if (/recv\(\) failed \(104/.test(msg)) {
    const upgraded = /upgraded connection/.test(msg);
    const port = (msg.match(/upstream:\s*"https?:\/\/(?:127\.0\.0\.1|localhost):(\d+)/) || [])[1];
    const svc = port && byPort[+port];
    let out = upgraded
      ? "104 means the other end closed the connection. On an UPGRADED "
        + "connection (WebSocket or a tunnel) that is the normal way it ends "
        + "— the client closed the app, lost signal or changed network. "
      : "104 means the other end closed the connection mid-response. ";
    out += "nginx logs it at error level, but on its own it needs no action. ";
    if (svc) out += `The backend here is service "${svc}" (port ${port}). `;
    out += "Worth investigating only if the rate suddenly climbs, or if users "
      + "report dropped sessions — then look at the BACKEND\u2019s own timeouts "
      + "and idle limits, not at nginx.";
    return out;
  }
  // "bad key share" is a TLS negotiation mismatch in the ClientHello, not a
  // certificate fault at all: the client offered a key share for a group
  // this OpenSSL does not accept. Telling the operator to check their
  // certificate for this was simply wrong.
  if (/bad key share|no shared cipher|unsupported protocol|wrong version number/i.test(msg)) {
    return "The client and nginx could not agree on TLS parameters, so the "
      + "handshake ended before any certificate was used — this is NOT a "
      + "certificate fault. It is normal background noise when something "
      + "speaks a non-TLS or differently-shaped protocol to a TLS port: a "
      + "port scanner, an old client, or a probe against an SNI/Reality port. "
      + "It only matters if a real client of yours cannot connect.";
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
