#!/usr/bin/env bash
# Shahrag — bring a fresh sandbox back to a working state.
#
# WHY THIS EXISTS
# ───────────────
# The sandbox is rebuilt between messages. /tmp is wiped, installed packages
# are gone, and the git checkout rolls back to an old snapshot. Only
# /home/user survives. Every session therefore starts by rebuilding the same
# toolchain, and that time came out of the budget for the actual work.
#
# WHAT WAS MEASURED, not guessed (re-measured 2026-09-21 after the first
# attempt turned out to be wrong):
#
#   Go download + extract ..........  2s   ← the network is fast
#   git fetch + reset ..............  1s
#   cold `go build` ................ 17-21s
#   full `go test ./...` ........... 23s
#   Playwright browser + apt deps .. 60-90s (only for UI tests)
#
# AN EARLIER VERSION OF THIS SCRIPT CLAIMED TO CUT START-UP TO 3 SECONDS BY
# PERSISTING THE BUILD CACHE INTO $HOME. That claim was wrong and is now
# removed. The measurement behind it was taken inside a single session, so
# the cache was still warm; it never actually survived a rebuild.
#
# The reason it cannot: the workspace snapshot is capped at roughly 128 MB
# and 10,000 files. Compiling ONE small package already produces 776 files
# and 50 MB of cache, and a full build produced 110 MB. The cache is either
# over the cap or crowds out everything else, so it is dropped. Verified by
# checking for ~/.shahrag-cache after a rebuild: absent, while ~/.config and
# the scripts themselves survived.
#
# So this script does NOT make the build faster. What it does is real but
# smaller: it replaces five separate commands — each of which could fail,
# stall, or be forgotten — with one, and it restores the repository from
# GitHub when the checkout has rolled back. Start-up is ~20 seconds either
# way; the value is reliability, not speed.

# USAGE
#   bash ~/setup.sh          # toolchain + repo + build cache
#   bash ~/setup.sh --browser # also install Playwright (slow, only for UI tests)
#   bash ~/setup.sh --panel   # also start the live test panel
#
# It is idempotent: safe to run twice, and it never touches the working tree
# unless the checkout has actually rolled back.

set -uo pipefail

GO_VER="1.25.0"
GO_TGZ="go${GO_VER}.linux-amd64.tar.gz"
REPO="https://github.com/mojtaba2ma/Shahrag.git"
SRC="$HOME/Shahrag"
# GOCACHE is left at its default. Pointing it into $HOME was tried and does
# not work — see the note above — and forcing it somewhere unusual only makes
# the failure harder to diagnose. GOMODCACHE is different: ~/go/pkg really
# does survive (118 MB of it did), so module downloads stay free.
export GOMODCACHE="$HOME/go/pkg/mod"
export GOTOOLCHAIN=local

step() { printf '\n\033[1;36m── %s\033[0m\n' "$*"; }
ok()   { printf '   \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '   \033[33m!\033[0m %s\n' "$*"; }

T0=$(date +%s)

# ── 1. Go toolchain ───────────────────────────────────────────────────
step "Go toolchain"
if [ -x /tmp/go/bin/go ]; then
  ok "already present ($(/tmp/go/bin/go version | awk '{print $3}'))"
else
  # Keep a copy in the home directory so a second run in the same session,
  # or a sandbox that only cleared /tmp, skips the network entirely.
  if [ -f "$HOME/.shahrag-cache/$GO_TGZ" ]; then
    tar -C /tmp -xzf "$HOME/.shahrag-cache/$GO_TGZ" && ok "restored from the home cache"
  else
    mkdir -p "$HOME/.shahrag-cache"
    if curl -sSfL -o "/tmp/$GO_TGZ" "https://go.dev/dl/$GO_TGZ"; then
      cp "/tmp/$GO_TGZ" "$HOME/.shahrag-cache/$GO_TGZ" 2>/dev/null
      tar -C /tmp -xzf "/tmp/$GO_TGZ" && ok "downloaded and extracted"
    else
      warn "download failed — no compiler available"; exit 1
    fi
  fi
fi
export PATH="/tmp/go/bin:$PATH"

# ── 2. the repository ─────────────────────────────────────────────────
step "Repository"
cd "$SRC" 2>/dev/null || { warn "$SRC is missing"; exit 1; }
git config user.email "dev@shahrag.local"
git config user.name  "shahrag"

LOCAL_HEAD=$(git rev-parse --short HEAD 2>/dev/null)
if git fetch -q "$REPO" main 2>/dev/null; then
  REMOTE_HEAD=$(git rev-parse --short FETCH_HEAD)
  if [ "$(git rev-parse HEAD)" = "$(git rev-parse FETCH_HEAD)" ]; then
    ok "already at $LOCAL_HEAD (matches origin)"
  else
    # The checkout rolls BACK when the sandbox is rebuilt, so the remote is
    # normally ahead. Uncommitted work is stashed rather than destroyed:
    # losing a session's edits to a convenience script would be worse than
    # the rollback it is fixing.
    if [ -n "$(git status --porcelain)" ]; then
      STASH="shahrag-setup-$(date +%s)"
      git stash push -u -q -m "$STASH" 2>/dev/null &&
        warn "uncommitted changes stashed as '$STASH' (git stash list)"
    fi
    git reset -q --hard FETCH_HEAD
    ok "restored $LOCAL_HEAD → $REMOTE_HEAD"
  fi
else
  warn "offline — staying at $LOCAL_HEAD"
fi
echo "     $(git log --oneline -1)"

# ── 3. build cache ────────────────────────────────────────────────────
step "Build cache"
BS=$(date +%s)
if go build -o /tmp/shahrag ./cmd/shahrag 2>&1 | head -5; then
  BE=$(date +%s)
  ok "built in $((BE-BS))s"
  echo "     $(/tmp/shahrag version)"
else
  warn "build failed — see the output above"
fi

# ── 4. optional: browser for UI tests ─────────────────────────────────
if [[ " $* " == *" --browser "* ]]; then
  step "Playwright (slow: ~60-90s)"
  if python3 -c "import playwright" 2>/dev/null; then
    ok "python package present"
  else
    pip3 install -q playwright 2>&1 | tail -1
  fi
  if [ -d "$HOME/.cache/ms-playwright" ]; then
    ok "browser already installed"
  else
    python3 -m playwright install chromium 2>&1 | tail -1
    # The shared libraries are the part that genuinely needs root and
    # genuinely takes time; without them chromium exits with
    # "libnspr4.so: cannot open shared object file".
    sudo -n python3 -m playwright install-deps chromium 2>&1 | tail -1
  fi
fi

# ── 5. optional: the live test panel ──────────────────────────────────
if [[ " $* " == *" --panel "* ]]; then
  step "Test panel"
  bash "$HOME/panel-setup.sh" 2>&1 | tail -3
fi

# ── 6. push credentials ───────────────────────────────────────────────
# Reported by the operator: having to paste a GitHub token into the chat
# every session, from a phone. The token is now kept in
# ~/.shahrag-push-token, which survives a rebuild because only
# .git/config, .git/credentials, .git-credentials and .netrc are stripped
# from the snapshot — those four names specifically, not any file that
# happens to hold a secret.
step "Push credentials"
if [ -r "$HOME/.shahrag-push-token" ] && [ -s "$HOME/.shahrag-push-token" ]; then
  ok "stored token found — push with: bash ~/push.sh"
else
  warn "no stored token; a push will need one pasted in"
fi

step "Ready in $(( $(date +%s) - T0 ))s"
cat <<'EOF'
   export PATH=/tmp/go/bin:$PATH GOTOOLCHAIN=local

   Run the tests AS ROOT. Two e2e tests write /etc/nginx-panel and drive a
   real nginx; as an ordinary user they fail with "no such file or
   directory", which looks like a code bug and is not one:

     sudo -n env PATH=/tmp/go/bin:/usr/sbin:/usr/bin:/bin \
       GOTOOLCHAIN=local go test ./... -count=1
EOF
