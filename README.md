# Shahrag

Shahrag is an nginx reverse-proxy control panel with both a command-line
interface and a web UI. Both share one configuration file with locking and
atomic writes, so changes made in one are visible immediately in the other.

The project is written in Go and compiles to a single static binary with
all HTML/CSS/JS assets embedded — no runtime dependencies.

## Services: HTTP and SNI in one list

Both kinds of record answer the same question — how traffic reaches a
backend — so they live on one page, told apart by a **TYPE** badge:

| Badge | Matches on | Configured by |
|---|---|---|
| `HTTP` | host + path, after TLS is terminated | subdomain, domain, path |
| `SNI`  | the TLS SNI, without decrypting anything | an SNI pattern |

*Add service* opens a dialog with two centred tabs, **HTTP** (the default)
and **SNI**; each tab shows only its own fields and saves through its own
endpoint. Editing opens the tab for the record's actual type and locks it —
the two are different objects, not two modes of one object.

The SNI target is a plain host field with the same rule as an HTTP service:
`localhost` (the default) means this machine, anything else is used verbatim,
and a checkbox selects pass-through for unblock routing.

The global SNI settings (enable, HTTP port, DNS resolvers) live in
**Settings → Nginx**.

### Per-service raw config

Every row has a raw-config button showing that record's **JSON** and the
**nginx it generates**, in two tabs — no more hunting through a 400-line
gateway.conf for the three blocks that belong to one service. Both are
editable, with the same transactional safety as the whole-file editor: the
file is snapshotted, nginx validates it, and a rejected edit is rolled back.

A service bound to several domains produces one block per server, so the
extract carries a `# ── block N of M · server_name: … ──` separator before
each. Keep those lines when editing: they are how each block is put back into
its own server block, and a save that drops one is refused rather than
guessing.

## SNI routing (including unblock / exit routing)

Routing on the stream side is decided purely by the TLS SNI the client sends,
so the panel calls it **SNI routing**. Each rule sends matching traffic to one
of three places:

| Target | Generated upstream | Use |
|---|---|---|
| This server (local) | `127.0.0.1:<port>` | Reality and any local backend — the classic behaviour |
| Pass through to the internet | `$ssl_preread_server_name:<port>` | Send a chosen domain out to the real site through your server (unblocking / lower-latency exit) |
| Another server | `host:<port>` | Hand the traffic to a different machine |

The SNI may be a wildcard: `*.epicgames.com` is emitted as an nginx regex, so
every subdomain matches. TLS is never terminated for a pass-through rule —
nginx only reads the SNI and splices the connection.

### Why a DNS resolver is required

A fixed upstream (`proxy_pass example.com:443`) is resolved once, while nginx
reads the config. A pass-through upstream is a *variable*
(`$ssl_preread_server_name`) whose value only exists per connection, so nginx
must look it up at request time — and it refuses to do that without a
`resolver`. Verified against a real nginx: `nginx -t` reports the config as
valid and every connection then fails with
`no resolver defined to resolve <host>`. Shahrag therefore always emits one.

### Recommended architecture: AdGuard in front, Unbound behind

The split most people want is "some domains through the server, the rest
direct" — a game's login and store need the relay, its voice and CDN
endpoints must not. DNS is the right place to make that decision:

```
client ──DoH/DoT──▶ AdGuard ──┬── relayed domain  ─▶ answers: this server
                              │                      client connects here,
                              │                      nginx splices it out
                              │
                              └── everything else ─▶ forwards to Unbound
                                                     (127.0.0.1:5335)
                                                     answers: the real IP,
                                                     client connects direct
```

**Any port works.** Nothing in this design assumes port 53. When another
service already owns 53, run AdGuard on 5353 (or anything else) and publish it
to clients over DoH/DoT — those use 443/853, so the client never cares which
UDP port AdGuard listens on locally. Unbound sits on its own port either way.
Verified with all three running simultaneously: an unrelated service on 53,
AdGuard on 5353, Unbound on 5335.

The panel does not guess which resolver is which by port number. It **asks**:
it queries the configured resolver for a domain you actually relay and checks
whether the answer is this server. That is correct on any port, on the LAN, or
on a second machine — an earlier port-based guess called AdGuard-on-5353 safe,
which is exactly the setup this note describes.

nginx must use **Unbound**, never AdGuard, to resolve pass-through targets.
AdGuard is the service that rewrites those very domains to this machine, so
asking it would send nginx back to itself. Verified against a real nginx: one
request exhausted the worker with `128 worker_connections are not enough`.

The panel therefore accepts a loopback resolver on a dedicated port
(`127.0.0.1:5335` — Unbound) and refuses one on port 53 (AdGuard).

Measured cost of this design on one machine:

| | |
|---|---|
| Cached DNS answer | 0.08 ms (served from RAM) |
| First lookup of a new name | ~10 ms, once, then cached |
| Relay throughput | ~8 Gbit/s, CPU too small to measure |
| Memory | nginx ~4 MiB, Unbound ~16 MiB |

The stream module only copies bytes — it never decrypts — so the relay costs
bandwidth, not CPU. Only the domains you list are relayed; everything else
never touches the server at all.

### Viewing and editing the raw config files

Both panels can show the two files that decide whether the server works:
`config.json` (what the panel knows) and the generated nginx files (what
nginx actually serves). When those two disagree, seeing both is the fastest
way to understand why.

* **Web UI** — *Config files* in the sidebar. Each file opens in an editor;
  saving is transactional: the file is snapshotted, JSON is parsed before it
  is written and nginx files are validated with `nginx -t` after, and anything
  that fails is rolled back automatically. nginx's own error output (with its
  line number) is shown verbatim. Generated files are labelled, because the
  next Generate overwrites them.
* **CLI** — menu entry 15, *Config files*. Long files are paged with line
  numbers, and `w` saves a copy under `/var/backups/shahrag/views/`.

Every edit made through the panel also leaves a timestamped copy in
`/var/backups/shahrag/edits/`.

### Checking a domain: `shahrag route`

```bash
sudo shahrag route epicgames.com voice.example-game.com
```

For each domain it prints the SNI rule that matches, where that rule sends the
traffic, what this server's DNS answers versus the real address, whether the
combination would loop, and — for pass-through rules — the result of a real
TLS handshake through the relay.

### Do NOT use this server's own AdGuard as that resolver

The unblock setup works because AdGuard answers "epicgames.com = my server",
which is how the client reaches the panel. If nginx used the same AdGuard to
find the *real* site it would get the same answer and connect to itself.
Reproduced against a real nginx: one request exhausted the worker with
`128 worker_connections are not enough`.

nginx needs an **upstream** resolver (the default `1.1.1.1` / `8.8.8.8`). Your
AdGuard keeps serving your clients exactly as before — only nginx's own
lookups have to bypass the rewrite. The panel refuses a resolver that points
at this machine, and the generator falls back to the public defaults if one
ever reaches the config another way.

### The connection stays opaque

Pass-through never decrypts anything. nginx reads the SNI, which is sent in
the clear before the encrypted part of the handshake, and then copies bytes in
both directions. Verified byte-for-byte: a real 1527-byte ClientHello arrived
at the far end unchanged, and the reply came back unchanged. There is no
`ssl_certificate`, no `proxy_ssl` and nothing that could alter the stream.

HTTP services have the same **Target** field: leave it at `localhost` for a
backend on this machine, or enter a host to proxy to another server.

## Same core as the CLI panel

The nginx config generator is a faithful port of the trusted CLI panel's
core (`nginx-panel.sh`), so the GUI produces **exactly** the same server
blocks:

- `path_owned` services get a single prefix location that forwards the
  FULL URI (`location /path { proxy_pass http://127.0.0.1:lp; }`).
- `path_owned=false` services get the path-strip treatment
  (`= /path` redirect, `/path/` location with trailing-slash proxy_pass,
  proxy_cookie_path and sub_filter link rewriting).
- Root services get `location /` with `proxy_redirect off`.
- SSL backends get `proxy_ssl_verify off` / `proxy_ssl_server_name off` /
  `proxy_buffering off`, and server blocks use `listen PORT ssl http2`.
- Reality ports remap to the Reality HTTP port exactly like the CLI core.

The setup wizard simply creates a service named `Shahrag` with the
values you enter — nothing special-cased. The same generator then builds
its nginx block like any other service, so the panel sits behind nginx
and opens through your domain with zero custom logic.

> Version 1.0.0 (compatibility build). The wizard assigns the panel a
> **random free port** in the high range (10000–65000) that never collides
> with the ports your services and Reality use, and configs written by
> older tooling (services with direct `subdomain`/`domain` fields) are
> migrated to bindings automatically.

## Safety guarantees

The installer and the config generator are built around one rule:
**never take the server down, and never leave a broken config behind.**

- **Backup before install.** If a previous installation exists, everything
  (binary, config, nginx.conf, conf.d, sites, systemd unit) is copied to
  `/var/backups/shahrag/<timestamp>/` before anything is touched.
- **Automatic rollback.** If any step of the installation fails, the backup
  is restored and the previously running services are brought back online
  *before* the installer exits. Your connections are never left down.
- **nginx is reloaded, not restarted.** A running nginx only ever gets
  `systemctl reload` (zero dropped connections); it is started only when it
  was stopped.
- **Validated edits only.** Every nginx.conf edit is checked with
  `nginx -t` and reverted when rejected. The panel prefers drop-in files in
  `/etc/nginx/conf.d/` and almost never touches `nginx.conf` itself.
- **Generation is transactional.** `gateway.conf` / `stream-gateway.conf`
  are snapshotted before regeneration; if the new config fails `nginx -t`
  (or the reload fails), the previous files are restored.
- **No dangling includes.** Enabling Reality adds a clearly marked
  `stream {}` block to nginx.conf; disabling it removes the block again, so
  a leftover include can never stop nginx from starting.
- **No half-configured domains.** Domains without a certificate are skipped
  with a clear comment instead of generating an invalid `ssl_certificate ;`
  block that would fail `nginx -t`.
- **nginx survives reboots.** Debian/Ubuntu ship `nginx.service` without
  `Restart=`, so a single transient failure at boot (a Reality port still
  held by xray/x-ui, IPv6 addresses not configured yet for
  `listen [::]:6038`, a certificate on a not-yet-mounted filesystem) leaves
  the server permanently down even though `nginx -t` is valid. Shahrag
  installs a marked systemd drop-in
  (`/etc/systemd/system/nginx.service.d/shahrag-resilience.conf`) with
  `Restart=on-failure`, `StartLimitIntervalSec=0` and
  `After=network-online.target`, makes sure the unit is enabled, and keeps a
  watchdog that starts nginx whenever it is down with a valid config. The
  distribution's unit file is never modified. Apply or re-check it any time
  with `sudo shahrag boot-guard`; `shahrag doctor` reports the status.
- **One server block per hostname.** Server blocks are grouped by effective
  listen port and canonical hostname, so nginx can never print
  `conflicting server name "example.com" on 0.0.0.0:6038, ignored` — a
  warning that silently drops a whole block and takes the services in it
  offline (they start serving the fake page instead).
- **Install token.** The web wizard requires a one-time token that the
  installer prints in the terminal, so nobody else can hijack the panel
  before you finish the setup.
- **Sliding sessions + auto-lock.** Refreshing the page never logs you
  out. After N minutes without activity (default 60, configurable or
  disabled in Settings → Security) the panel locks itself and requires a
  fresh login.

## Installation

```bash
git clone https://github.com/mojtaba2ma/Shahrag.git /opt/shahrag-src
cd /opt/shahrag-src
sudo bash install.sh
```

**Already installed before?** `git clone` FAILS silently when the target
directory exists, and the old installer inside it would be run — that is the
classic "I reinstalled but nothing changed" trap. The installer now detects
stale copies and refuses to run, but the clean way is to re-clone:

```bash
sudo rm -rf /opt/shahrag-src
git clone https://github.com/mojtaba2ma/Shahrag.git /opt/shahrag-src
cd /opt/shahrag-src
sudo bash install.sh
```

Verify you really got the new build before and after installing:

```bash
grep -q '"doctor"' /opt/shahrag-src/cmd/shahrag/main.go && echo "source OK (new build)"
shahrag version     # must print:  Shahrag v1.0.0 (build r13)
shahrag doctor      # must print the diagnostic report (old builds open the menu instead)
```

The installer:

1. Backs up the current state (if any).
2. Installs nginx and jq if missing.
3. Applies safe base settings via drop-ins (stub_status, proxy_cache off,
   raises `worker_connections` once) — each edit validated by `nginx -t`.
4. Compiles the binary (installing the latest Go toolchain if the system
   lacks it) or uses a prebuilt one, and verifies it runs.
5. Installs a systemd unit that reads the panel port from the config
   (no hardcoded port — the unit and the config can never drift apart).
6. Restores the backup automatically if any step fails.

When it finishes, open:

```
http://<server-ip>:8080/
```

and enter the **one-time install token** printed by the installer. The
first-run wizard asks for the panel domain, subdomain, certificate paths
(optional), local port, a random 22-character secret path, and an admin
password. It creates the `Shahrag` service with `path_owned=true` so the
panel location never conflicts with other services' routing.

After the wizard the panel is served through nginx at:

```
https://sub.example.com/<random-path>/
```

### Re-installing / upgrading

Running `install.sh` again on an existing installation is safe:

- everything is backed up first,
- the old service is stopped only for the atomic binary swap and is started
  again immediately,
- on any failure the previous binary, config and unit are restored and the
  old service is restarted before the installer exits.

### "connect() failed (111)" in the error log

This means nginx routed the request correctly but **nothing was listening on
the backend port** — the backend service (xray, x-ui, AdGuard…) was down. It
is not an nginx or panel problem. The Logs page groups these errors, resolves
each failing port back to the service that owns it, and says whether the
requests came from real visitors or only from `127.0.0.1` (i.e. from
`shahrag selftest`, which probes every route on purpose).

```bash
sudo shahrag selftest        # which service is actually failing
ss -ltnp | grep :4628        # is the backend listening at all?
systemctl status xray
```

### worker_connections 65536 warning that never goes away

`worker_rlimit_nofile` cannot exceed 65535, so with
`worker_connections = 65536` the two can never be equal. Older builds compared
them directly, warned forever, and rewrote nginx.conf on every run without
changing anything. The check is now capped at the file-descriptor ceiling and
reports the effective limit instead of a false alarm.

### An upgrade seems to change nothing in the web panel

If the panel looks unchanged after an upgrade, the browser is running cached
JavaScript. Builds before r26 shipped an identical cache validator for every
release, so a browser that had used the panel before kept its old copy
indefinitely — including in a private window, once that window had loaded the
panel once.

From r26 the validator is a hash of each file's own contents, the HTML shell
is sent with `no-store`, and every asset URL carries the build (`app.js?v=r26`),
so an upgrade cannot be missed. If you are coming from an older build and it
still looks stale, one hard reload clears it:

* Android/Chrome: ⋮ → History → Clear browsing data → Cached images and files
* Desktop: Ctrl+Shift+R (Cmd+Shift+R on macOS)

Confirm which build the browser is actually running:

```bash
curl -s https://your.panel/<path>/ | grep -o 'app.js?v=[^"]*'
```

### The installer fails and you cannot tell why

Every install now writes a full log to **`/var/log/shahrag-install.log`**, and
a failure reports the exact line, the exact command and its exit code:

```
[ERR] FAILURE DETAILS (this is what actually went wrong):
  line 412 of install.sh exited with code 137
  command: cd "$SCRIPT_DIR" && CGO_ENABLED=0 go build ...
```

Exit code **137** (or "signal: killed") means the kernel OOM-killer stopped
the Go compiler — compiling needs roughly 1 GB of RAM. The installer now adds
a temporary 2 GB swapfile automatically when memory is tight, limits compiler
parallelism, and removes the swapfile afterwards. If it still fails it prints
the swap commands to run.

Preflight checks (free disk on `/usr/local` and `/tmp`, presence of nginx and
jq) now run **before** anything is modified, so a missing prerequisite can no
longer fail half-way through an installation.

### "Installation failed — the previous state was restored"

If nginx was ALREADY down before you ran the installer (because another
daemon holds one of its ports), older installers treated the failed
`systemctl start nginx` as an installation error and rolled everything back —
including the **new binary**. `shahrag version` then kept reporting the OLD
build, so the fixes never took effect and re-installing could not help.

The installer no longer aborts for a pre-existing nginx problem: the panel is
installed, the failure is reported with the offending port, and any rollback
now prints the build that ended up on disk so a stale binary is impossible to
miss. Always confirm with:

```bash
shahrag version    # must match the build the installer expects
```

### nginx says the config is valid but will not start

`nginx -t` **only parses** the configuration — it never binds a socket. So a
"valid" test next to an inactive service almost always means **another daemon
already owns a port nginx needs** (xray, x-ui, sing-box, haproxy…), and the
real error only appears at start time:

```
nginx: [emerg] bind() to 0.0.0.0:2053 failed (98: Address already in use)
```

`shahrag doctor` detects this before the start and names the process:

```
PORT CONFLICTS — this is why nginx cannot start
  port 2053 is required by stream-gateway.conf but is already held by xray (pid 812)
```

Fix by freeing the port (stop/reconfigure the other daemon) or by changing the
port in the panel, then run `sudo shahrag generate`.

Note that with Reality enabled nginx must bind **every Reality port**
(`stream-gateway.conf`), not just 80/443 — those are exactly the ports xray is
most likely to be holding.

### nginx warns "conflicting server name ... ignored"

nginx keeps the **first** server block that claims a hostname and silently
ignores every later one, so the services inside the ignored block stop working
and serve the fake page instead. Shahrag's generated files can no longer
collide with themselves, so a remaining warning means a **leftover config file
from an older setup** is still being loaded. `shahrag doctor` names the exact
files:

```
DUPLICATE server names — nginx IGNORES the later block
  sugerdood.com on port 6038
    claimed by: /etc/nginx/conf.d/gateway.conf
    claimed by: /etc/nginx/conf.d/old-panel.conf
```

Delete the file Shahrag does not manage and run `sudo shahrag generate`.

### nginx is inactive after a reboot

Symptom: `systemctl status nginx` says *inactive* while `nginx -t` reports a
perfectly valid configuration.

Causes, all handled by `shahrag boot-guard`:

1. `nginx.service` was **not enabled**, so it never starts at boot.
2. Debian/Ubuntu's unit has **no `Restart=`**, so one transient failure at
   boot (a Reality port still held by xray/x-ui, IPv6 not configured yet for
   `listen [::]:6038`, a certificate on a filesystem mounted later) makes
   systemd give up permanently.
3. `worker_connections` was raised far above the process fd limit, so nginx
   logs *"worker_connections exceed open file resource limit"*.

```bash
sudo shahrag doctor       # the "nginx boot readiness" section explains which
sudo shahrag boot-guard   # fixes all three and starts nginx
```

### If something goes wrong

```bash
# Full diagnostic report (config, nginx, ports, boot readiness, backups)
sudo shahrag doctor

# End-to-end test of EVERY service on the server itself: backend liveness,
# routing through the Reality HTTP port, routing through the reality listen
# ports (the real Cloudflare path via the stream default), port ownership
# and active-vs-disk nginx config comparison.
sudo shahrag selftest

# See the panel service logs
journalctl -u shahrag -n 50

# Validate the nginx config without touching the running nginx
nginx -t

# Restore a backup made by the installer (list them first)
ls -t /var/backups/shahrag/
```

If the panel becomes unreachable after a reinstall, restore the pre-wizard
config from the installer backup with one command (it regenerates nginx and
restarts the panel service):

```bash
sudo shahrag restore /var/backups/shahrag/wizard-pre-<timestamp>.json
```

Or manually:

```bash
sudo cp /var/backups/shahrag/<latest>/config.json /etc/nginx-panel/config.json
sudo shahrag generate
sudo systemctl restart shahrag
```

> The wizard never replaces an existing domain certificate: a certificate
> you type in the wizard is only used when the domain has none yet.
> Existing certificates are kept, because replacing a domain-wide cert
> with a single-subdomain one breaks TLS for every other subdomain.

The last-known-good nginx files are also kept in the backup directory, so a
manual restore is always possible:

```bash
sudo cp -a /var/backups/shahrag/<timestamp>/nginx/. /etc/nginx/
sudo nginx -t && sudo systemctl reload nginx
```

## Usage

### Command line

```bash
shahrag            # interactive menu
shahrag status     # one-shot status
shahrag generate   # regenerate nginx config and reload
shahrag doctor     # full diagnostic report (config, nginx, boot readiness)
shahrag selftest   # live end-to-end routing test of every service
shahrag boot-guard # make nginx start at boot and auto-restart on failure
shahrag restore F  # restore a config backup and regenerate
shahrag version    # print version
```

**After a reboot nginx did not come back?** Run:

```bash
sudo shahrag doctor       # shows WHY (and whether nginx is enabled at boot)
sudo shahrag boot-guard   # applies the fix, then starts nginx
```

The menu covers every setting — including the panel's own domain, path,
port, and certificate — so anything misconfigured in the web wizard can
be corrected from the terminal.

`np` is kept as a compatibility alias for `shahrag`.

### Web UI

Open the URL printed at the end of installation, log in, and use the
sidebar to manage services, domains, ports, Reality, the fake site,
nginx settings, backups, and the panel itself.

## Building from source

```bash
go build -ldflags="-s -w" -o shahrag ./cmd/shahrag
```

Requires Go 1.25+.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `SHAHRAG_HOST` | `0.0.0.0` | Bind address (web server) |
| `SHAHRAG_PORT` | `0` (auto) | Listen port override; `0` = use the panel port from the config |
| `SHAHRAG_CONFIG` | `/etc/nginx-panel/config.json` | Config file |
| `SHAHRAG_INSTALL_TOKEN` | `/etc/nginx-panel/.install-token` | One-time wizard token file |

## License

MIT
