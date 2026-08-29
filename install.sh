#!/usr/bin/env bash
# =========================================================================
#  Shahrag v1.0.0 Installer
#
#  Safe by design:
#    • Never edits /etc/nginx/nginx.conf destructively (drop-ins preferred,
#      every edit is validated with `nginx -t` and reverted on failure).
#    • BACKS UP everything before touching anything. If ANY step fails, the
#      backup is restored automatically and the previously running services
#      are put back online BEFORE the installer exits — your connections are
#      never left down.
#    • nginx is only ever *reloaded* (never restarted while running), so
#      live traffic is not dropped.
#    • The one-time install token guards the web wizard against hijacking.
# =========================================================================
set -Eeuo pipefail

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
CLI_PATH="/usr/local/bin/nginx-panel"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_FILE="/etc/nginx-panel/config.json"
UNIT_FILE="/etc/systemd/system/shahrag.service"
TOKEN_FILE="/etc/nginx-panel/.install-token"
STUB_CONF="/etc/nginx/conf.d/shahrag-stub.conf"
CACHE_CONF="/etc/nginx/conf.d/shahrag-cache.conf"
EXPECTED_BUILD="r32"
BACKUP_ROOT="/var/backups/shahrag"
BACKUP_DIR="${BACKUP_ROOT}/$(date +%Y%m%d-%H%M%S)"
PANEL_PORT=0
SUCCESS=0
NGINX_ACTIVE_BEFORE=0
SHAHRAG_ACTIVE_BEFORE=0
TMP_BIN=""

echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo -e "${CYAN}   Shahrag v${VERSION} — نصب پنل شاه‌رگ${NC}"
echo -e "${CYAN}═══════════════════════════════════════════════${NC}"
echo ""

# ────────────────────────────────────────────────────────────
#  0. Stale-copy guard (BEFORE anything else)
#  The classic trap: `git clone` fails silently because the target
#  directory already exists, and the OLD installer inside it is run
#  instead. Refuse to continue when the source here is not the new build.
# ────────────────────────────────────────────────────────────
if [ -f "${SCRIPT_DIR}/cmd/shahrag/main.go" ]; then
    if ! grep -q '"boot-guard"' "${SCRIPT_DIR}/cmd/shahrag/main.go" 2>/dev/null; then
        error "This installer copy is OUTDATED (no 'boot-guard' command in the source)."
        error "An old clone already exists at: ${SCRIPT_DIR}"
        error "The project was NOT cloned again — git clone fails when the directory exists."
        error "Fix it by removing the old directory and re-cloning:"
        error ""
        error "  rm -rf ${SCRIPT_DIR}"
        error "  git clone https://github.com/mojtaba2ma/Shahrag.git ${SCRIPT_DIR}"
        error "  cd ${SCRIPT_DIR}"
        error "  sudo bash install.sh"
        error ""
        error "Then verify with:  shahrag version   (must show 'build ${EXPECTED_BUILD}')"
        exit 1
    fi
fi

# ────────────────────────────────────────────────────────────
#  Rollback — runs on ANY failure or interrupt (EXIT trap).
#  Restores the backup and brings the previous services back
#  BEFORE the installer exits.
# ────────────────────────────────────────────────────────────
# report_installed_build prints what is ACTUALLY on disk, so a rollback can
# never leave the operator believing the new version is installed. The
# reported outage was invisible for exactly this reason: a pre-existing port
# conflict aborted the install, the rollback restored the OLD binary, and
# `shahrag version` kept printing the old build tag while the operator
# assumed the fixes were live.
report_installed_build() {
    if [ -x "$BIN_PATH" ]; then
        local have
        have=$("$BIN_PATH" version 2>/dev/null || echo "unknown")
        case "$have" in
            *"build ${EXPECTED_BUILD}"*)
                echo -e "  ${GREEN}Installed build:${NC} $have" ;;
            *)
                echo -e "  ${RED}Installed build: $have${NC}"
                echo -e "  ${RED}This is NOT the expected build ${EXPECTED_BUILD}.${NC}"
                echo -e "  ${YELLOW}The rollback restored the previous binary, so none of the${NC}"
                echo -e "  ${YELLOW}newer fixes are active. Fix the error above and re-run.${NC}" ;;
        esac
    fi
}

# ── Failure context ─────────────────────────────────────────
# Without this, a failed install printed ONLY "Installation failed" and the
# operator (and I) had no way to know which step broke. `set -e` aborts
# silently, so record the line, the command and its exit code the moment the
# error happens, and replay it in the rollback report.
FAILED_LINE=""
FAILED_CMD=""
FAILED_CODE=""
record_failure() {
    FAILED_CODE="$?"
    FAILED_LINE="${1:-?}"
    FAILED_CMD="${2:-?}"
}
trap 'record_failure "$LINENO" "$BASH_COMMAND"' ERR

# Everything the installer prints is also written here, so the full context
# survives even when the terminal scrolled away.
INSTALL_LOG="/var/log/shahrag-install.log"

report_failure_context() {
    if [ -n "$FAILED_CMD" ]; then
        echo ""
        error "FAILURE DETAILS (this is what actually went wrong):"
        echo -e "  ${RED}line ${FAILED_LINE} of install.sh exited with code ${FAILED_CODE}${NC}" >&2
        echo -e "  ${RED}command: ${FAILED_CMD}${NC}" >&2
        # An OOM-killed `go build` is the classic silent killer on small VPS.
        if [ "${FAILED_CODE}" = "137" ] || echo "$FAILED_CMD" | grep -q "go build"; then
            local memtotal
            memtotal=$(awk '/MemTotal/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
            local swaptotal
            swaptotal=$(awk '/SwapTotal/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
            warn "The Go build needs roughly 1 GB of free memory."
            warn "This machine has ${memtotal} MB RAM and ${swaptotal} MB swap."
            if dmesg 2>/dev/null | tail -n 50 | grep -qi "killed process.*go\|out of memory"; then
                error "The kernel OOM-killer terminated the build (see: dmesg | tail)."
            fi
            if [ "${swaptotal}" -lt 1024 ]; then
                warn "Add temporary swap and re-run:"
                warn "  sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile"
                warn "  sudo mkswap /swapfile && sudo swapon /swapfile"
            fi
        fi
        echo -e "  ${YELLOW}Full log: ${INSTALL_LOG}${NC}" >&2
    fi
}

rollback() {
    if [ "$SUCCESS" -eq 1 ]; then
        return 0
    fi
    set +e
    warn "Installation did not complete — restoring the backup now..."
    info "Backup directory: ${BACKUP_DIR}"

    # 1. nginx files (config, conf.d, sites-enabled, sites-available, stream)
    if [ -d "${BACKUP_DIR}/nginx" ]; then
        cp -a "${BACKUP_DIR}/nginx/." /etc/nginx/ 2>/dev/null
    fi

    # 2. Panel config
    if [ -f "${BACKUP_DIR}/config.json" ]; then
        cp -f "${BACKUP_DIR}/config.json" "$CONFIG_FILE"
        chmod 600 "$CONFIG_FILE"
    else
        rm -f "$CONFIG_FILE"
    fi

    # 3. Binary
    if [ -f "${BACKUP_DIR}/shahrag.bin" ]; then
        cp -f "${BACKUP_DIR}/shahrag.bin" "$BIN_PATH"
        chmod +x "$BIN_PATH"
    else
        rm -f "$BIN_PATH"
    fi

    # 4. CLI wrapper
    if [ -f "${BACKUP_DIR}/nginx-panel" ]; then
        cp -f "${BACKUP_DIR}/nginx-panel" "$CLI_PATH"
        chmod +x "$CLI_PATH"
        ln -sf "$CLI_PATH" /usr/local/bin/np
    else
        rm -f "$CLI_PATH" /usr/local/bin/np
    fi

    # 5. systemd unit
    if [ -f "${BACKUP_DIR}/shahrag.service" ]; then
        cp -f "${BACKUP_DIR}/shahrag.service" "$UNIT_FILE"
    else
        rm -f "$UNIT_FILE"
    fi
    systemctl daemon-reload 2>/dev/null || true

    # 6. nginx — bring it back the same way we found it. Never restart a
    #    running nginx: reload keeps connections alive; if the restored
    #    config is somehow invalid, the running nginx keeps serving its
    #    in-memory config (the safest possible state).
    if nginx -t >/dev/null 2>&1; then
        if [ "$NGINX_ACTIVE_BEFORE" -eq 1 ]; then
            systemctl reload nginx 2>/dev/null || true
        else
            systemctl stop nginx 2>/dev/null || true
        fi
    else
        warn "Restored nginx config is not valid — nginx was NOT reloaded."
        warn "The running nginx keeps serving its previous in-memory config."
    fi

    # 7. shahrag — restore its previous running state.
    if [ "$SHAHRAG_ACTIVE_BEFORE" -eq 1 ]; then
        if ! systemctl is-active --quiet shahrag 2>/dev/null; then
            systemctl start shahrag 2>/dev/null || warn "Could not restart the old shahrag service — check 'systemctl status shahrag'."
        fi
    else
        systemctl stop shahrag 2>/dev/null || true
    fi

    [ -n "$TMP_BIN" ] && rm -f "$TMP_BIN"
    echo ""
    error "Installation failed — the previous state was restored. No services were left down."
    report_failure_context
    report_installed_build
    echo ""
    exit 1
}
# Mirror everything to the log file from here on (the terminal still shows
# it live). This is what makes a failed install diagnosable after the fact.
mkdir -p "$(dirname "$INSTALL_LOG")" 2>/dev/null || true
exec > >(tee -a "$INSTALL_LOG") 2>&1
echo "=== Shahrag install $(date -Is) (expecting build ${EXPECTED_BUILD}) ==="

trap 'rollback' EXIT

# ────────────────────────────────────────────────────────────
#  Preflight + BACKUP (before any change)
# ────────────────────────────────────────────────────────────
systemctl is-active --quiet nginx    && NGINX_ACTIVE_BEFORE=1   || true
systemctl is-active --quiet shahrag  && SHAHRAG_ACTIVE_BEFORE=1 || true

info "Creating backup before any change..."
mkdir -p "$BACKUP_DIR/nginx"
[ -f /etc/nginx/nginx.conf ]          && cp -a /etc/nginx/nginx.conf "$BACKUP_DIR/nginx/" || true
[ -d /etc/nginx/conf.d ]              && cp -a /etc/nginx/conf.d "$BACKUP_DIR/nginx/" || true
[ -d /etc/nginx/sites-enabled ]       && cp -a /etc/nginx/sites-enabled "$BACKUP_DIR/nginx/" || true
[ -d /etc/nginx/sites-available ]     && cp -a /etc/nginx/sites-available "$BACKUP_DIR/nginx/" || true
[ -f /etc/nginx/stream-gateway.conf ] && cp -a /etc/nginx/stream-gateway.conf "$BACKUP_DIR/nginx/" || true
[ -f "$CONFIG_FILE" ]                 && cp -a "$CONFIG_FILE" "$BACKUP_DIR/config.json" || true
[ -f "$BIN_PATH" ]                    && cp -a "$BIN_PATH" "$BACKUP_DIR/shahrag.bin" || true
[ -f "$CLI_PATH" ]                    && cp -a "$CLI_PATH" "$BACKUP_DIR/nginx-panel" || true
[ -f "$UNIT_FILE" ]                   && cp -a "$UNIT_FILE" "$BACKUP_DIR/shahrag.service" || true
info "Backup saved under: ${BACKUP_DIR}"

# ── 0b. Preflight: fail EARLY and clearly, before touching anything ──
# Each of these previously surfaced as a bare "Installation failed" after
# the installer had already started changing things.
PREFLIGHT_OK=1
DISK_FREE_MB=$(df -Pm /usr/local 2>/dev/null | awk 'NR==2{print $4}' || echo 0)
if [ "${DISK_FREE_MB:-0}" -lt 600 ]; then
    error "Only ${DISK_FREE_MB} MB free on /usr/local — the Go toolchain needs ~600 MB."
    PREFLIGHT_OK=0
fi
DISK_FREE_TMP=$(df -Pm /tmp 2>/dev/null | awk 'NR==2{print $4}' || echo 0)
if [ "${DISK_FREE_TMP:-0}" -lt 300 ]; then
    error "Only ${DISK_FREE_TMP} MB free on /tmp — the build needs ~300 MB."
    PREFLIGHT_OK=0
fi
if [ "$PREFLIGHT_OK" -ne 1 ]; then
    error "Preflight checks failed — nothing was changed on this system."
    exit 1
fi
MEM_AVAIL_MB=$(awk '/MemAvailable/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
info "Preflight OK — RAM available: ${MEM_AVAIL_MB} MB, disk: ${DISK_FREE_MB} MB on /usr/local."

# ── 1. Package lists ─────────────────────────────────────
info "Updating package lists..."
# A transient mirror hiccup must not abort the whole install: the packages we
# need are usually present already, and step 2/3 verify that explicitly.
apt-get update -qq || warn "apt-get update failed (continuing — required packages are verified below)."

# ── 2. nginx ─────────────────────────────────────────────
# nginx lives in /usr/sbin, which is NOT on every PATH (cron, some sudo
# configurations, minimal systemd environments). Looking only at
# `command -v nginx` made the installer believe nginx was missing on a host
# where it was installed and running.
find_nginx() {
    if command -v nginx >/dev/null 2>&1; then
        command -v nginx
        return 0
    fi
    local p
    for p in /usr/sbin/nginx /usr/local/sbin/nginx /sbin/nginx /usr/local/nginx/sbin/nginx; do
        [ -x "$p" ] && { echo "$p"; return 0; }
    done
    return 1
}
NGINX_BIN="$(find_nginx || true)"
if [ -n "$NGINX_BIN" ]; then
    info "nginx already installed: $("$NGINX_BIN" -v 2>&1)"
else
    info "Installing nginx..."
    apt-get install -y -qq nginx || true
    NGINX_BIN="$(find_nginx || true)"
fi
# Make sure plain `nginx` works for the rest of the script regardless of PATH.
if [ -n "$NGINX_BIN" ]; then
    export PATH="$(dirname "$NGINX_BIN"):$PATH"
fi

if ! command -v jq >/dev/null 2>&1; then
    apt-get install -y -qq jq || true
fi
# jq is used to patch the config; without it the install cannot proceed
# correctly, so verify rather than discovering it half-way through.
if ! command -v jq >/dev/null 2>&1; then
    error "jq is required but could not be installed. Install it manually:  sudo apt-get install jq"
    exit 1
fi
if [ -z "${NGINX_BIN:-}" ]; then
    error "nginx is required but could not be found or installed."
    error "Install it manually and re-run:  sudo apt-get install nginx"
    exit 1
fi

# ── 3. SAFE nginx base settings ─────────────────────────
info "Applying safe nginx base settings..."

# 3a. Disable default site (backed up above).
if [ -f /etc/nginx/sites-enabled/default ]; then
    rm -f /etc/nginx/sites-enabled/default
    info "Disabled default site."
fi

# 3b. Make sure all files referenced by "include" directives in nginx.conf
#     exist. A previous install may have left a dangling include (e.g.
#     /etc/nginx/stream-gateway.conf) which makes `nginx -t` fail and
#     blocks reload. We only create empty files for paths that live in
#     /etc/nginx and are missing — and we skip glob PATTERNS (they are not
#     real files; creating a file literally named "*.conf" would be wrong).
if [ -f /etc/nginx/nginx.conf ]; then
    includes=$(grep -oE 'include[[:space:]]+[^;]*;' /etc/nginx/nginx.conf \
        | sed -E 's/include[[:space:]]+//; s/;.*//; s/^[[:space:]]*//; s/[[:space:]]*$//' \
        | grep -E '^/etc/nginx/' || true)
    for inc in $includes; do
        case "$inc" in
            *'*'*|*'?'*|*'['*) continue ;;   # glob pattern — not a file
        esac
        if [ ! -e "$inc" ]; then
            mkdir -p "$(dirname "$inc")"
            : > "$inc"
            warn "Created missing include: $inc"
        fi
    done
fi

# Remove stray EMPTY files that older installer versions created by
# mistaking glob patterns for real files.
for f in "/etc/nginx/conf.d/*.conf" "/etc/nginx/sites-enabled/*" "/etc/nginx/modules-enabled/*.conf"; do
    if [ -f "$f" ] && [ ! -s "$f" ]; then
        rm -f "$f"
        warn "Removed stray empty file created by an older installer: $f"
    fi
done

# 3c. stub_status drop-in (never touches nginx.conf).
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

# 3d. proxy_cache off — as a drop-in too (an old version may have inserted
#     it inline; that is harmless and left alone).
if ! grep -q "proxy_cache off" /etc/nginx/nginx.conf 2>/dev/null; then
    cat > "$CACHE_CONF" <<'NGINX'
# Shahrag: proxy_cache disabled (matches CLI behaviour).
proxy_cache off;
NGINX
else
    info "proxy_cache off already present in nginx.conf — leaving as-is."
fi

# 3e. worker_connections — only when nginx.conf is currently valid, only edit
#     an EXISTING directive, and only raise it once (values >= 10000 are left
#     alone so re-running the installer never inflates it again).
if nginx -t >/dev/null 2>&1; then
    CUR_WC=$(grep -oP 'worker_connections\s+\K[0-9]+' /etc/nginx/nginx.conf 2>/dev/null | head -1 || true)
    if [ -n "$CUR_WC" ] && [ "$CUR_WC" != "0" ] && [ "$CUR_WC" -lt 4096 ]; then
        NEW_WC=$((CUR_WC * 13))
        [ "$NEW_WC" -gt 65536 ] && NEW_WC=65536
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
else
    warn "nginx.conf is currently invalid; skipping worker_connections change."
fi

# Test before doing anything to the running nginx.
if ! nginx -t >/dev/null 2>&1; then
    error "nginx config is invalid — see 'nginx -t'. Not touching the running nginx."
    nginx -t || true
    exit 1
fi

# ── 4. Directories ───────────────────────────────────────
mkdir -p /etc/nginx-panel /var/lib/shahrag /var/www/mysite /var/log/nginx

# ── 5. Build binary ──────────────────────────────────────
# Source always wins: a leftover `shahrag` file in the installer directory
# may be an OLD binary from a previous build — using it silently installs
# the old version (the "I reinstalled but nothing changed" trap).
# The prebuilt file is only used when there is no Go source at all.
PREBUILT=""
if [ -f "${SCRIPT_DIR}/cmd/shahrag/main.go" ]; then
    info "Building Shahrag from source..."
    GO_MIN_MAJOR=1
    GO_MIN_MINOR=25
    need_go=1
    if command -v go >/dev/null 2>&1; then
        GO_VER=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' | head -1 || true)
        if [ -n "$GO_VER" ]; then
            GM=$(echo "$GO_VER" | cut -d. -f1)
            Gm=$(echo "$GO_VER" | cut -d. -f2)
            if [ "$GM" -gt "$GO_MIN_MAJOR" ] || { [ "$GM" -eq "$GO_MIN_MAJOR" ] && [ "$Gm" -ge "$GO_MIN_MINOR" ]; }; then
                need_go=0
            fi
        fi
    fi
    if [ "$need_go" -eq 1 ]; then
        info "Installing Go toolchain..."
        apt-get install -y -qq wget tar ca-certificates >/dev/null 2>&1 || true
        GO_TGZ="$(mktemp -u).tar.gz"
        ARCH=$(dpkg --print-architecture)
        case "$ARCH" in
            amd64) GOARCH="amd64" ;;
            arm64) GOARCH="arm64" ;;
            armhf) GOARCH="armv6l" ;;
            *)     error "Unsupported arch: $ARCH"; exit 1 ;;
        esac
        GO_VER_DL=$(wget -qO- "https://go.dev/VERSION?m=text" 2>/dev/null | head -1 || true)
        case "$GO_VER_DL" in
            go1.*) ;;
            *) GO_VER_DL="go1.25.0" ;;
        esac
        info "Downloading Go ${GO_VER_DL}..."
        wget -q "https://go.dev/dl/${GO_VER_DL}.linux-${GOARCH}.tar.gz" -O "$GO_TGZ"
        rm -rf /usr/local/go
        tar -C /usr/local -xzf "$GO_TGZ"
        rm -f "$GO_TGZ"
        export PATH="/usr/local/go/bin:$PATH"
    fi
    # ── Build, defensively ───────────────────────────────────────────
    # Compiling Go needs ~1 GB of RAM. On a small VPS the kernel OOM-killer
    # silently kills the compiler (exit 137 / "signal: killed"), `set -e`
    # aborts, and the rollback restores the OLD binary — the operator then
    # keeps running the previous build with no idea why nothing changed.
    #
    # So: add temporary swap when memory is tight, cap the compiler's
    # parallelism, and report the real reason if it still fails.
    AVAIL_MB=$(awk '/MemAvailable/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
    SWAP_MB=$(awk '/SwapTotal/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)
    SWAPFILE_CREATED=0
    if [ "${AVAIL_MB:-0}" -lt 1200 ] && [ "${SWAP_MB:-0}" -lt 512 ]; then
        warn "Only ${AVAIL_MB} MB RAM available and no swap — adding a temporary 2 GB swapfile for the build."
        if fallocate -l 2G /var/shahrag-build-swap 2>/dev/null || \
           dd if=/dev/zero of=/var/shahrag-build-swap bs=1M count=2048 status=none 2>/dev/null; then
            chmod 600 /var/shahrag-build-swap
            if mkswap /var/shahrag-build-swap >/dev/null 2>&1 && swapon /var/shahrag-build-swap 2>/dev/null; then
                SWAPFILE_CREATED=1
                info "Temporary swap enabled (removed automatically after the build)."
            else
                rm -f /var/shahrag-build-swap
                warn "Could not enable swap — continuing, the build may still succeed."
            fi
        else
            warn "Could not create a swapfile — continuing."
        fi
    fi

    # Fewer parallel compile jobs = a much lower peak memory footprint.
    BUILD_JOBS=2
    if [ "${AVAIL_MB:-0}" -lt 900 ]; then
        BUILD_JOBS=1
    fi

    BUILD_LOG="/tmp/shahrag-build.log"
    set +e
    # -mod=vendor builds from the vendored dependencies in the repo, so the
    # install works on a server that cannot reach proxy.golang.org (common
    # behind national filtering). Falls back to a normal build if the vendor
    # directory is somehow missing.
    BUILD_MOD=""
    if [ -d "$SCRIPT_DIR/vendor" ]; then
        BUILD_MOD="-mod=vendor"
        info "building from vendored dependencies (no network needed)"
    fi
    (cd "$SCRIPT_DIR" && CGO_ENABLED=0 GOFLAGS="-p=${BUILD_JOBS} ${BUILD_MOD}" GOGC=50 \
        go build -ldflags="-s -w" -o /tmp/shahrag-build ./cmd/shahrag) >"$BUILD_LOG" 2>&1
    BUILD_RC=$?
    set -e

    if [ "$SWAPFILE_CREATED" -eq 1 ]; then
        swapoff /var/shahrag-build-swap 2>/dev/null || true
        rm -f /var/shahrag-build-swap
    fi

    if [ "$BUILD_RC" -ne 0 ]; then
        error "Compiling Shahrag FAILED (exit ${BUILD_RC}). Compiler output:"
        sed 's/^/    /' "$BUILD_LOG" | tail -n 25
        if grep -qiE "signal: killed|cannot allocate memory|out of memory" "$BUILD_LOG" 2>/dev/null \
           || [ "$BUILD_RC" -eq 137 ]; then
            error "The compiler was killed — this machine ran out of memory."
            warn "Add permanent swap and re-run the installer:"
            warn "  sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile"
            warn "  sudo mkswap /swapfile && sudo swapon /swapfile"
            warn "  echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab"
        fi
        exit 1
    fi
    PREBUILT="/tmp/shahrag-build"
elif [ -f "${SCRIPT_DIR}/shahrag" ]; then
    info "No Go source found — using the prebuilt binary in the installer directory."
    PREBUILT="${SCRIPT_DIR}/shahrag"
else
    error "No prebuilt binary or Go source found."
    exit 1
fi

# Verify the new binary actually runs BEFORE replacing anything.
if ! "$PREBUILT" version >/tmp/shahrag-ver.out 2>&1; then
    error "The candidate binary failed to execute ('$PREBUILT version'). Aborting."
    cat /tmp/shahrag-ver.out >&2 || true
    exit 1
fi
VER_OUT=$(cat /tmp/shahrag-ver.out)
info "New binary verified: ${VER_OUT}"
case "$VER_OUT" in
    *"build ${EXPECTED_BUILD}"*) ;;
    *)
        error "The candidate binary is an OLD Shahrag build: '${VER_OUT}'"
        error "Expected 'Shahrag v${VERSION} (build r2)'. Your clone is stale."
        error "Remove it and re-clone the project:"
        error "  rm -rf ${SCRIPT_DIR}"
        error "  git clone https://github.com/mojtaba2ma/Shahrag.git ${SCRIPT_DIR}"
        error "  cd ${SCRIPT_DIR} && sudo bash install.sh"
        exit 1
        ;;
esac
# Smoke-check the new diagnostic command too (old builds lack `doctor` and
# would open the interactive menu instead — bounded by timeout). The check
# runs against a THROWAWAY config so it cannot create /etc/nginx-panel
# files as a side effect (that would confuse the default-config step below).
if ! SHAHRAG_CONFIG=/tmp/shahrag-smoke-config.json timeout 10 "$PREBUILT" doctor >/dev/null 2>&1; then
    error "The candidate binary lacks the 'doctor' command — stale build."
    error "Re-clone the project and re-run the installer (see above)."
    exit 1
fi
rm -f /tmp/shahrag-smoke-config.json

# ── 6. Panel port ────────────────────────────────────────
# The systemd unit does NOT hardcode a port — the binary reads the port from
# the panel config, so they can never drift apart. For EXISTING configs we
# keep the configured port; for a fresh install we pick a RANDOM free port
# in the high range so the panel can never collide with a backend service.
random_free_port() {
    python3 - <<'PY'
import socket, sys, random
for _ in range(2000):
    p = random.randint(10000, 65000)
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.bind(("0.0.0.0", p))
        s.close()
        print(p)
        sys.exit(0)
    except OSError:
        s.close()
sys.exit(1)
PY
}
# port_bindable: can a NEW socket bind 0.0.0.0:$1 right now? A real bind
# test catches conflicts that `ss` misses (listeners on specific interfaces
# such as VPN/cloud-metadata addresses block wildcard binds).
port_bindable() {
    python3 - "$1" <<'PY' >/dev/null 2>&1
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    s.bind(("0.0.0.0", int(sys.argv[1])))
    s.close()
    sys.exit(0)
except OSError:
    sys.exit(1)
PY
}
if [ -f "$CONFIG_FILE" ]; then
    CFG_PORT=$(jq -r '.shahrag.panel.local_port // 0' "$CONFIG_FILE" 2>/dev/null || true)
    case "$CFG_PORT" in
        ''|*[!0-9]*) CFG_PORT=0 ;;
    esac
    if [ "$CFG_PORT" -lt 1 ] 2>/dev/null || [ "$CFG_PORT" -gt 65535 ] 2>/dev/null; then
        CFG_PORT=0
    fi
    PANEL_PORT="$CFG_PORT"
fi
if [ "${PANEL_PORT:-0}" -lt 1 ] 2>/dev/null || [ "${PANEL_PORT:-0}" -gt 65535 ] 2>/dev/null; then
    PANEL_PORT=$(random_free_port) || { error "No free port found."; exit 1; }
fi
if ! port_bindable "$PANEL_PORT"; then
    # The running panel itself holds its own port — that is fine; we swap
    # binaries under it and restart it on the same port.
    if [ "$SHAHRAG_ACTIVE_BEFORE" -eq 0 ]; then
        warn "Port $PANEL_PORT is busy — picking a free random port..."
        PANEL_PORT=$(random_free_port) || { error "No free port found."; exit 1; }
    fi
fi
info "Panel port: $PANEL_PORT"

# ── 7. systemd unit (port resolved from config at runtime) ──
info "Creating systemd service..."
cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=Shahrag Web Panel
After=network.target nginx.service
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${BIN_PATH} serve -host 0.0.0.0
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable shahrag >/dev/null 2>&1 || true

# ── 8. Swap the binary atomically ────────────────────────
# Stop the old service only for the swap itself, then start the new one
# immediately. If anything goes wrong here, rollback() restores the old
# binary and starts the old service again.
if [ "$SHAHRAG_ACTIVE_BEFORE" -eq 1 ]; then
    systemctl stop shahrag
fi
TMP_BIN="${BIN_PATH}.new.$$"
cp "$PREBUILT" "$TMP_BIN"
chmod +x "$TMP_BIN"
mv -f "$TMP_BIN" "$BIN_PATH"

# ── 9. CLI wrapper ───────────────────────────────────────
if [ -f "${SCRIPT_DIR}/nginx-panel-cli.sh" ]; then
    cp "${SCRIPT_DIR}/nginx-panel-cli.sh" "$CLI_PATH"
    chmod +x "$CLI_PATH"
    ln -sf "$CLI_PATH" /usr/local/bin/np
    info "CLI installed as 'np'."
fi

# ── 10. Default config + panel port ──────────────────────
if [ ! -f "$CONFIG_FILE" ]; then
    info "Creating default config..."
    "$BIN_PATH" init-config >/dev/null
    if [ ! -f "$CONFIG_FILE" ]; then
        error "The binary did not create the default config."
        exit 1
    fi
    # Persist the chosen port so the service binds it on start.
    jq --argjson p "$PANEL_PORT" '.shahrag.panel.local_port = $p' "$CONFIG_FILE" > "${CONFIG_FILE}.tmp" \
        && mv "${CONFIG_FILE}.tmp" "$CONFIG_FILE"
fi

# When the panel has never been installed and no port is configured yet,
# persist the chosen port into the EXISTING config too — otherwise the
# service falls back to 8080 while the installer printed a different
# wizard URL above.
CFG_INSTALLED=$(jq -r '.shahrag.panel.installed // false' "$CONFIG_FILE" 2>/dev/null || true)
CFG_LPORT=$(jq -r '.shahrag.panel.local_port // 0' "$CONFIG_FILE" 2>/dev/null || true)
case "$CFG_LPORT" in
    ''|*[!0-9]*) CFG_LPORT=0 ;;
esac
if [ "$CFG_INSTALLED" != "true" ] && [ "$CFG_LPORT" -lt 1 ] 2>/dev/null; then
    jq --argjson p "$PANEL_PORT" '.shahrag.panel.local_port = $p' "$CONFIG_FILE" > "${CONFIG_FILE}.tmp" \
        && mv "${CONFIG_FILE}.tmp" "$CONFIG_FILE"
    info "Panel port persisted in config: $PANEL_PORT"
fi
chmod 600 "$CONFIG_FILE" 2>/dev/null || true

# ── 11. One-time install token (wizard protection) ───────
INSTALLED=$(jq -r '.shahrag.panel.installed // false' "$CONFIG_FILE" 2>/dev/null || true)
TOKEN=""
if [ "$INSTALLED" != "true" ]; then
    TOKEN=$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')
    printf '%s\n' "$TOKEN" > "$TOKEN_FILE"
    chmod 600 "$TOKEN_FILE"
else
    rm -f "$TOKEN_FILE"
fi

# ── 12. Start the panel service and verify ───────────────
# Wait briefly in case a conflicting socket (another process on the port)
# is still being released; the panel itself then falls back to loopback or
# a free port if the conflict persists.
if [ "$SHAHRAG_ACTIVE_BEFORE" -eq 0 ]; then
    for _ in $(seq 1 20); do
        port_bindable "$PANEL_PORT" && break
        sleep 0.25
    done
fi
systemctl restart shahrag
sleep 2
if ! systemctl is-active --quiet shahrag; then
    error "Shahrag service failed to start. Logs:"
    journalctl -u shahrag --no-pager -n 20 || true
    exit 1
fi
info "Shahrag service is active."

# ── 13. nginx: reload (never restart a running nginx) ────
if ! nginx -t >/dev/null 2>&1; then
    error "nginx config invalid after install — see 'nginx -t'."
    exit 1
fi
if [ "$NGINX_ACTIVE_BEFORE" -eq 1 ]; then
    systemctl reload nginx
    info "nginx reloaded (no connections dropped)."
else
    # nginx was ALREADY down before this install.
    #
    # Starting it can legitimately fail for a reason that has nothing to do
    # with the installation: another daemon (xray / x-ui / sing-box) holds a
    # port nginx wants. `nginx -t` cannot see that — it only parses the
    # config — so the failure surfaces only here.
    #
    # This must NOT abort the install. Before this guard, `set -e` turned a
    # pre-existing port conflict into "Installation failed — the previous
    # state was restored", which rolled the NEW binary back to the old one.
    # The operator then ran an old build, saw the old bugs, and no amount of
    # re-installing could ever fix anything. The panel itself is already
    # installed and running at this point; nginx being down is a separate,
    # pre-existing problem that we diagnose loudly instead.
    if systemctl start nginx 2>/tmp/shahrag-nginx-start.log; then
        info "nginx started."
    else
        NGINX_START_FAILED=1
        warn "nginx was already stopped and could not be started."
        warn "This is NOT caused by the installation — the panel is installed fine."
        sed 's/^/    /' /tmp/shahrag-nginx-start.log 2>/dev/null | tail -n 5 || true
        # Name the exact ports and the processes holding them.
        journalctl -u nginx --no-pager -n 20 2>/dev/null \
            | grep -iE "bind\(\)|emerg|address already in use" \
            | tail -n 6 | sed 's/^/    /' || true
        warn "Run this for the full diagnosis:  sudo shahrag doctor"
    fi
fi

# ── 13b. Regenerate nginx configs with the NEW binary ────────
# The generator is transactional (nginx -t + reload with rollback), so this
# keeps /etc/nginx/conf.d/gateway.conf and stream-gateway.conf consistent
# with the freshly installed binary even when the operator forgets to run
# `shahrag generate` afterwards.
if "$BIN_PATH" generate >/tmp/shahrag-gen.log 2>&1; then
    info "nginx configs regenerated with the new binary."
    # Verify nginx actually picked the new config up. A reload can fail
    # silently (e.g. xray bound a reality port directly), leaving nginx on
    # the OLD in-memory config — the classic "services still down" state.
    HTTP_PORT=$(jq -r '.reality.http_port // 6038' "$CONFIG_FILE" 2>/dev/null || echo 6038)
    ACTIVE_BLOCKS=$(nginx -T 2>/dev/null | grep -c "listen ${HTTP_PORT} ssl" || true)
    DISK_BLOCKS=$(grep -c "listen ${HTTP_PORT} ssl" /etc/nginx/conf.d/gateway.conf 2>/dev/null || true)
    if [ "$ACTIVE_BLOCKS" != "$DISK_BLOCKS" ]; then
        warn "nginx may still be running the PREVIOUS config (active: $ACTIVE_BLOCKS blocks, on disk: $DISK_BLOCKS)."
        warn "This usually means the reload failed due to a port conflict."
        warn "Check with: ss -ltnp | grep -E ':(443|2053|8443) '   and   shahrag selftest"
    fi
else
    warn "nginx config generation FAILED — the previous config was kept. Details:"
    sed 's/^/    /' /tmp/shahrag-gen.log | tail -n 12
    warn "Fix the issue and run: shahrag generate"
fi

# ── 13c. Boot protection ─────────────────────────────────
# A server reboot left nginx inactive even though its config was valid:
# Debian/Ubuntu ship nginx.service WITHOUT Restart=, so ONE transient
# failure at boot (a Reality port still held by xray/x-ui, IPv6 addresses
# not configured yet for `listen [::]:6038`, a certificate on a
# not-yet-mounted filesystem) makes systemd give up for good.
# `shahrag boot-guard` installs a drop-in (Restart=on-failure,
# After=network-online.target, StartLimitIntervalSec=0), enables the unit
# and aligns worker_rlimit_nofile with worker_connections. The
# distribution's unit file is never modified.
info "Applying nginx boot protection..."
if "$BIN_PATH" boot-guard >/tmp/shahrag-bootguard.log 2>&1; then
    sed 's/^/    /' /tmp/shahrag-bootguard.log
else
    warn "Boot protection could not be applied fully:"
    sed 's/^/    /' /tmp/shahrag-bootguard.log | tail -n 8
    warn "nginx may not come back automatically after a reboot. Run: sudo shahrag boot-guard"
fi

# Verify systemd really picked the drop-in up (the value systemd reports is
# the ground truth — a file on disk alone proves nothing).
if systemctl show nginx -p Restart 2>/dev/null | grep -q "Restart=on-failure"; then
    info "nginx will auto-restart on failure and start at boot."
else
    warn "systemd does not report Restart=on-failure for nginx — check: systemctl show nginx -p Restart"
fi

# ── 14. Firewall ─────────────────────────────────────────
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow "$PANEL_PORT"/tcp >/dev/null 2>&1 || true
    info "Opened port $PANEL_PORT in UFW."
fi

if [ "${NGINX_START_FAILED:-0}" -eq 1 ]; then
    echo ""
    warn "═══════════════════════════════════════════════"
    warn " Shahrag installed successfully, but nginx is DOWN."
    warn " Cause: a port nginx needs is held by another process"
    warn " (nginx -t cannot detect this — it never binds a port)."
    warn ""
    warn " Diagnose and fix:"
    warn "   sudo shahrag doctor          # names the port AND the process"
    warn "   sudo ss -ltnp | grep -E ':(80|443|2053|8443|6038) '"
    warn "═══════════════════════════════════════════════"
fi

SERVER_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -z "$SERVER_IP" ] && SERVER_IP="YOUR_SERVER_IP"

SUCCESS=1

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Shahrag v${VERSION} installed successfully.${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════${NC}"
echo ""
echo -e "  ${CYAN}Installed binary:${NC} $("$BIN_PATH" version)"
echo ""
echo -e "  ${CYAN}Open the setup wizard:${NC}"
echo -e "  http://${SERVER_IP}:${PANEL_PORT}/"
echo ""
if [ -n "$TOKEN" ]; then
    echo -e "  ${CYAN}One-time install token:${NC}"
    echo -e "  ${YELLOW}${TOKEN}${NC}"
    echo ""
    echo -e "  ${YELLOW}Enter this token in the final wizard step. It is removed${NC}"
    echo -e "  ${YELLOW}after a successful install.${NC}"
    echo ""
fi
echo -e "  Backup of the previous installation (if any):"
echo -e "  ${BACKUP_DIR}"
echo ""
warn "After completing the wizard, the panel is reachable at your configured"
warn "domain/path through nginx. The direct :${PANEL_PORT} port can then be firewalled."
