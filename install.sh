#!/usr/bin/env bash
# =========================================================================
#  Shahrag v1.0.0 Installer
#  Safe by design: never edits /etc/nginx/nginx.conf destructively.
# =========================================================================
set -euo pipefail

GREEN='\033[1;32m'; YELLOW='\033[1;33m'; RED='\033[1;31m'; CYAN='\033[1;36m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERR]${NC} $*" >&2; }

if [ "$(id -u)" -ne 0 ]; then
    error "Run as root: sudo bash $0"
    exit 1
fi

VERSION="1.0.0"
BIN_PATH="/usr/local/bin/shahrag"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_FILE="/etc/nginx-panel/config.json"
STUB_CONF="/etc/nginx/conf.d/shahrag-stub.conf"
GO_MIN_MAJOR=1
GO_MIN_MINOR=25

echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo -e "${CYAN}   Shahrag v${VERSION} — نصب پنل شاه‌رگ${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo ""

# ── 1. Package lists ─────────────────────────────────────
info "Updating package lists..."
apt-get update -qq

# ── 2. nginx ─────────────────────────────────────────────
if command -v nginx >/dev/null 2>&1; then
    info "nginx already installed: $(nginx -v 2>&1)"
else
    info "Installing nginx..."
    apt-get install -y -qq nginx
fi

if ! command -v jq >/dev/null 2>&1; then
    apt-get install -y -qq jq
fi

# ── 3. SAFE nginx base settings ─────────────────────────
info "Applying safe nginx base settings..."

# 3a. Disable default site
if [ -f /etc/nginx/sites-enabled/default ]; then
    rm -f /etc/nginx/sites-enabled/default
    info "Disabled default site."
fi

# 3b. Make sure all files referenced by "include" directives in nginx.conf
#     exist. A previous install may have left a dangling include (e.g.
#     /etc/nginx/stream-gateway.conf) which makes `nginx -t` fail and
#     blocks reload. We only create empty files for paths that live in
#     /etc/nginx and are missing.
if [ -f /etc/nginx/nginx.conf ]; then
    includes=$(grep -oE 'include[[:space:]]+[^;]*;' /etc/nginx/nginx.conf \
        | sed -E 's/include[[:space:]]+//; s/;.*//; s/^[[:space:]]*//; s/[[:space:]]*$//' \
        | grep -E '^/etc/nginx/' || true)
    for inc in $includes; do
        if [ ! -e "$inc" ]; then
            mkdir -p "$(dirname "$inc")"
            : > "$inc"
            warn "Created missing include: $inc"
        fi
    done
fi

# 3b. Write a safe drop-in for stub_status. We never touch nginx.conf
#     directly — the old installer appended blocks outside http{} and broke
#     servers. We also do NOT add proxy_cache off here because the earlier
#     installer may already have inserted it into nginx.conf; duplicate
#     directives cause nginx to refuse to start.
mkdir -p /etc/nginx/conf.d
cat > "$STUB_CONF" <<'NGINX'
# Shahrag connection metrics — drop-in, included inside http {}.
server {
    listen 127.0.0.1:8081;
    server_name _;
    location = /nginx_status {
        stub_status;
        access_log off;
        allow 127.0.0.1;
        deny all;
    }
}
NGINX
info "Wrote safe drop-in: $STUB_CONF"

# If proxy_cache off is not already in nginx.conf, add a separate drop-in.
if ! grep -q "proxy_cache off" /etc/nginx/nginx.conf 2>/dev/null; then
    cat > /etc/nginx/conf.d/shahrag-cache.conf <<'NGINX'
# Shahrag: proxy_cache disabled (matches CLI behaviour).
proxy_cache off;
NGINX
else
    info "proxy_cache off already present in nginx.conf — leaving as-is."
fi

# 3c. worker_connections — only if nginx.conf is currently valid,
#     and only edit an EXISTING directive (never insert blindly).
if nginx -t >/dev/null 2>&1; then
    CUR_WC=$(grep -oP 'worker_connections\s+\K[0-9]+' /etc/nginx/nginx.conf 2>/dev/null | head -1 || true)
    if [ -n "$CUR_WC" ] && [ "$CUR_WC" != "0" ]; then
        NEW_WC=$((CUR_WC * 13))
        [ "$NEW_WC" -gt 65536 ] && NEW_WC=65536
        if [ "$NEW_WC" -ne "$CUR_WC" ]; then
            cp /etc/nginx/nginx.conf "/etc/nginx/nginx.conf.bak.$(date +%s)"
            sed -i "s/worker_connections\s\+[0-9]*/worker_connections $NEW_WC/" /etc/nginx/nginx.conf
            if nginx -t >/dev/null 2>&1; then
                info "worker_connections: $CUR_WC -> $NEW_WC"
            else
                warn "worker_connections change rejected by nginx -t; reverting."
                LAST_BAK=$(ls -t /etc/nginx/nginx.conf.bak.* 2>/dev/null | head -1)
                [ -n "$LAST_BAK" ] && cp "$LAST_BAK" /etc/nginx/nginx.conf
            fi
        fi
    fi
else
    warn "nginx.conf is currently invalid; skipping worker_connections change."
fi

# Test before doing anything destructive
if ! nginx -t >/dev/null 2>&1; then
    error "nginx config is invalid — see 'nginx -t'. Not reloading."
    nginx -t || true
else
    systemctl restart nginx || warn "nginx restart returned non-zero."
fi

# ── 4. Directories ───────────────────────────────────────
mkdir -p /etc/nginx-panel /var/lib/shahrag /var/www/mysite /var/log/nginx

# ── 5. Build binary ──────────────────────────────────────
PREBUILT=""
if [ -f "${SCRIPT_DIR}/shahrag" ]; then
    PREBUILT="${SCRIPT_DIR}/shahrag"
elif [ -f "${SCRIPT_DIR}/cmd/shahrag/main.go" ]; then
    info "Building Shahrag from source..."
    need_go=1
    if command -v go >/dev/null 2>&1; then
        GO_VER=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' | head -1)
        GM=$(echo "$GO_VER" | cut -d. -f1)
        Gm=$(echo "$GO_VER" | cut -d. -f2)
        if [ "$GM" -gt "$GO_MIN_MAJOR" ] || { [ "$GM" -eq "$GO_MIN_MAJOR" ] && [ "$Gm" -ge "$GO_MIN_MINOR" ]; }; then
            need_go=0
        fi
    fi
    if [ "$need_go" -eq 1 ]; then
        info "Installing Go 1.25..."
        apt-get install -y -qq wget tar ca-certificates
        GO_TGZ="$(mktemp -u).tar.gz"
        ARCH=$(dpkg --print-architecture)
        case "$ARCH" in
            amd64) GOARCH="amd64" ;;
            arm64) GOARCH="arm64" ;;
            armhf) GOARCH="armv6l" ;;
            *)     error "Unsupported arch: $ARCH"; exit 1 ;;
        esac
        wget -q "https://go.dev/dl/go1.25.0.linux-${GOARCH}.tar.gz" -O "$GO_TGZ"
        rm -rf /usr/local/go
        tar -C /usr/local -xzf "$GO_TGZ"
        rm -f "$GO_TGZ"
        export PATH="/usr/local/go/bin:$PATH"
    fi
    (cd "$SCRIPT_DIR" && CGO_ENABLED=0 go build -ldflags="-s -w" -o /tmp/shahrag ./cmd/shahrag)
    PREBUILT="/tmp/shahrag"
else
    error "No prebuilt binary or Go source found."
    exit 1
fi

# Stop the service first so the binary file isn't held open ("Text file busy").
systemctl stop shahrag 2>/dev/null || true
# Atomic rename so a running process never sees a partial write.
TMP_BIN="${BIN_PATH}.new.$$"
cp "$PREBUILT" "$TMP_BIN"
chmod +x "$TMP_BIN"
mv -f "$TMP_BIN" "$BIN_PATH"

# ── 6. CLI wrapper ───────────────────────────────────────
if [ -f "${SCRIPT_DIR}/nginx-panel-cli.sh" ]; then
    cp "${SCRIPT_DIR}/nginx-panel-cli.sh" /usr/local/bin/nginx-panel
    chmod +x /usr/local/bin/nginx-panel
    ln -sf /usr/local/bin/nginx-panel /usr/local/bin/np
    info "CLI installed as 'np'."
fi

# ── 7. Default config ───────────────────────────────────
if [ ! -f "$CONFIG_FILE" ]; then
    info "Creating default config..."
    "$BIN_PATH" -port 8080 &
    TEMP_PID=$!
    sleep 1
    kill "$TEMP_PID" 2>/dev/null || true
    wait "$TEMP_PID" 2>/dev/null || true
fi
chmod 600 "$CONFIG_FILE" 2>/dev/null || true

# ── 8. Systemd service — bind 0.0.0.0 so wizard is reachable ──
info "Creating systemd service..."
cat > /etc/systemd/system/shahrag.service <<UNIT
[Unit]
Description=Shahrag Web Panel
After=network.target nginx.service

[Service]
Type=simple
User=root
ExecStart=${BIN_PATH} serve -host 0.0.0.0 -port 8080
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable shahrag >/dev/null 2>&1 || true
systemctl restart shahrag
sleep 2

# ── 9. Verify ────────────────────────────────────────────
if ! systemctl is-active --quiet shahrag; then
    error "Shahrag service failed to start. Logs:"
    journalctl -u shahrag --no-pager -n 20
    exit 1
fi

SERVER_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -z "$SERVER_IP" ] && SERVER_IP="YOUR_SERVER_IP"

# If ufw is active, open 8080
if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow 8080/tcp >/dev/null 2>&1 || true
    info "Opened port 8080 in UFW."
fi

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Shahrag v${VERSION} installed.${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
echo ""
echo -e "  ${CYAN}Open the setup wizard:${NC}"
echo -e "  http://${SERVER_IP}:8080/"
echo ""
warn "After completing the wizard, the panel is reachable at your configured"
warn "domain/path through nginx. The direct :8080 port can then be firewalled."
