# Shahrag

Shahrag is an nginx reverse-proxy control panel with both a command-line
interface and a web UI. Both share one configuration file with locking and
atomic writes, so changes made in one are visible immediately in the other.

The project is written in Go and compiles to a single static binary with
all HTML/CSS/JS assets embedded — no runtime dependencies.

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
- **Install token.** The web wizard requires a one-time token that the
  installer prints in the terminal, so nobody else can hijack the panel
  before you finish the setup.

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
shahrag version     # must print:  Shahrag v1.0.0 (build r2)
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

### If something goes wrong

```bash
# Full diagnostic report (config, nginx, ports, backups)
sudo shahrag doctor

# See the panel service logs
journalctl -u shahrag -n 50

# Validate the nginx config without touching the running nginx
nginx -t

# Restore a backup made by the installer (list them first)
ls -t /var/backups/shahrag/
```

If the panel becomes unreachable after a reinstall, restore the pre-wizard
config from the installer backup and regenerate:

```bash
sudo cp /var/backups/shahrag/<latest>/config.json /etc/nginx-panel/config.json
sudo shahrag generate
sudo systemctl restart shahrag
```

The last-known-good nginx files are also kept in the backup directory, so a
manual restore is always possible:

```bash
sudo cp -a /var/backups/shahrag/<timestamp>/nginx/. /etc/nginx/
sudo nginx -t && sudo systemctl reload nginx
```

## Usage

### Command line

```bash
shahrag          # interactive menu
shahrag status   # one-shot status
shahrag generate # regenerate nginx config and reload
shahrag version  # print version
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
