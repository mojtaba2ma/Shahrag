# Shahrag

Shahrag is an nginx reverse-proxy control panel with both a command-line
interface and a web UI. Both share one configuration file with locking and
atomic writes, so changes made in one are visible immediately in the other.

The project is written in Go and compiles to a single static binary with
all HTML/CSS/JS assets embedded — no runtime dependencies.

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
