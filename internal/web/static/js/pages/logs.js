/* Logs page.

   Raw nginx errors are hard to act on: "connect() failed (111)" says nothing
   about WHICH service is down or whether the request even came from a real
   user. This page groups the recent errors, resolves each failing upstream
   port back to the service that owns it, and states the likely cause. */
window.Pages = window.Pages || {};
window.Pages.logs = {
  async render(container, state, ctx) {
    const { api, t, Icons } = ctx;

    const load = async () => Promise.all([
      api("/api/logs/http?lines=200"),
      api("/api/logs/stream?lines=200").catch(() => ({ content: "" })),
      api("/api/logs/error?lines=200"),
      api("/api/services").catch(() => ({})),
    ]);

    let [http, stream, err, services] = await load();

    container.innerHTML = `
      <div class="page-header"><h1>${Icons.svg("logs", 20)} ${t("logs.title")}</h1>
        <button class="btn btn-ghost btn-sm" id="refresh">${Icons.svg("refresh", 14)} Refresh</button></div>
      <div id="diagnosis"></div>
      <div class="tabs">
        <button class="tab active" data-tab="http">HTTP</button>
        <button class="tab" data-tab="stream">Stream</button>
        <button class="tab" data-tab="error">Error</button>
      </div>
      <pre class="log-view" id="log-out"></pre>`;

    const out = document.getElementById("log-out");
    let current = "http";

    const render = () => {
      const data = { http: http.content, stream: stream.content, error: err.content };
      out.textContent = data[current] || "(empty)";
      document.getElementById("diagnosis").innerHTML = diagnose(err.content || "", services, Icons);
    };

    render();

    container.querySelectorAll(".tab").forEach(b => b.onclick = () => {
      container.querySelectorAll(".tab").forEach(x => x.classList.remove("active"));
      b.classList.add("active");
      current = b.dataset.tab;
      render();
    });

    // Re-fetch in place. `location.reload()` threw the whole SPA away — on a
    // panel served under /<path>/ that is a full round trip through nginx
    // and it looked like a session drop whenever anything went wrong.
    document.getElementById("refresh").onclick = async (e) => {
      const btn = e.currentTarget;
      btn.disabled = true;
      try {
        [http, stream, err, services] = await load();
        render();
      } catch (ex) {
        ctx.toast(ex.message, "error");
      }
      btn.disabled = false;
    };
  },
};

/* diagnose turns repeated nginx error lines into one actionable summary. */
function diagnose(errText, services, Icons) {
  if (!errText) return "";
  const lines = errText.split("\n");

  // upstream port -> { count, hosts:Set, paths:Set }
  const upstreams = new Map();
  let selfTestOnly = true;
  let sawUpstreamError = false;

  for (const l of lines) {
    if (l.indexOf("connect() failed") === -1 && l.indexOf("no live upstreams") === -1) continue;
    sawUpstreamError = true;
    const m = l.match(/upstream:\s*"https?:\/\/127\.0\.0\.1:(\d+)([^"]*)"/);
    const hostM = l.match(/host:\s*"([^"]+)"/);
    const clientM = l.match(/client:\s*([0-9a-fA-F.:]+)/);
    if (clientM && clientM[1] !== "127.0.0.1" && clientM[1] !== "::1") selfTestOnly = false;
    if (!m) continue;
    const port = parseInt(m[1], 10);
    if (!upstreams.has(port)) upstreams.set(port, { count: 0, hosts: new Set(), paths: new Set() });
    const e = upstreams.get(port);
    e.count++;
    if (hostM) e.hosts.add(hostM[1]);
    if (m[2]) e.paths.add(m[2]);
  }

  if (!sawUpstreamError || upstreams.size === 0) return "";

  // Resolve each failing port back to the service that owns it.
  const byPort = {};
  for (const [name, s] of Object.entries(services || {})) {
    if (s && typeof s.local_port === "number") byPort[s.local_port] = name;
  }

  const rows = [...upstreams.entries()]
    .sort((a, b) => b[1].count - a[1].count)
    .map(([port, e]) => {
      const svc = byPort[port];
      const who = svc
        ? `<strong>${svc}</strong>`
        : `<span class="muted">(no service uses this port)</span>`;
      return `<div class="rank-row">
        <span class="rank-k">${who} → 127.0.0.1:${port}
          ${e.hosts.size ? `<span class="muted"> · ${[...e.hosts].slice(0, 3).join(", ")}</span>` : ""}</span>
        <span class="rank-v">${e.count}</span>
      </div>`;
    }).join("");

  const originNote = selfTestOnly
    ? `<p class="muted" style="margin-top:8px">All of these requests came from <code>127.0.0.1</code>,
       i.e. from <code>shahrag selftest</code> or another local probe — not from real visitors.
       They still mean the backend was not listening at that moment.</p>`
    : `<p class="muted" style="margin-top:8px">Some of these requests came from real clients,
       so users were affected.</p>`;

  return `
    <div class="card" style="border-color:var(--warning)">
      <h3>${Icons.svg("warning", 16)} Backends refusing connections</h3>
      <p class="muted">nginx routed the request correctly, but nothing was listening on the
      backend port (<code>connect() failed (111: Connection refused)</code>).
      This is the backend service being down — not an nginx or panel problem.</p>
      <div class="rank-list" style="margin-top:10px">${rows}</div>
      ${originNote}
      <p style="margin-top:10px">Check the owning service, e.g.:
        <code>systemctl status xray</code> · <code>ss -ltnp | grep :PORT</code> ·
        <code>sudo shahrag selftest</code></p>
    </div>`;
}
