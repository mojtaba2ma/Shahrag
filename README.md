# Shahrag

Shahrag is an nginx reverse-proxy control panel with both a command-line
interface and a web UI. Both share one configuration file with locking and
atomic writes, so changes made in one are visible immediately in the other.

The project is written in Go and compiles to a single static binary with
all HTML/CSS/JS assets embedded — no runtime dependencies.

## Installation

```bash
git clone https://github.com/mojtaba2ma/Shahrag.git /opt/shahrag-src
cd /opt/shahrag-src
sudo bash install.sh
```

The installer:

1. Installs nginx and jq if missing.
2. Applies the same recommended base settings as the original CLI
   (disables the default site and proxy cache, raises
   `worker_connections`, enables `stub_status`).
3. Compiles the binary (installing Go 1.25 if the system lacks it) or
   uses a prebuilt one.
4. Installs a systemd unit.

When it finishes, open:

```
http://<server-ip>:8080/
```

The first-run wizard asks for the panel domain, subdomain, certificate
paths (optional), local port, a random 22-character secret path, and an
admin password. It creates the `Shahrag` service with `path_owned=true`
so the panel location never conflicts with other services' routing.

After the wizard the panel is served through nginx at:

```
https://sub.example.com/<random-path>/
```

## Usage

### Command line

```bash
shahrag          # interactive menu
shahrag status   # one-shot status
shahrag generate # regenerate nginx config and reload
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
| `SHAHRAG_PORT` | `8080` | Listen port |
| `SHAHRAG_CONFIG` | `/etc/nginx-panel/config.json` | Config file |

## License

MIT
