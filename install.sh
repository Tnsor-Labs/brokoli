#!/bin/sh
#
# Brokoli one-liner installer.
#
# Usage:
#   curl -fsSL https://brokoli.orkestri.site/install.sh | sh
#
# Environment variables:
#   BROKOLI_VERSION      Install a specific release tag (default: latest).
#   BROKOLI_INSTALL_DIR  Override the install directory (default: /usr/local/bin
#                        if writable, else $HOME/.local/bin).
#   BROKOLI_NO_SETUP=1   Skip the interactive admin-user / start-server prompt.
#   BROKOLI_YES=1        Answer yes to every prompt non-interactively.
#
# This script is POSIX-sh (no bashisms) and works with either curl or wget.
#
set -eu

# ───────────────── output helpers ─────────────────
RED='' GREEN='' YELLOW='' BOLD='' DIM='' RESET=''
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    RED=$(tput setaf 1); GREEN=$(tput setaf 2); YELLOW=$(tput setaf 3)
    BOLD=$(tput bold);   DIM=$(tput dim);       RESET=$(tput sgr0)
fi
info() { printf '%s==>%s %s\n' "$GREEN" "$RESET" "$1"; }
warn() { printf '%s!! %s%s\n'  "$YELLOW" "$1" "$RESET" >&2; }
die()  { printf '%serror:%s %s\n' "$RED" "$RESET" "$1" >&2; exit 1; }
step() { printf '    %s%s%s\n' "$DIM" "$1" "$RESET"; }

# ───────────────── preflight ─────────────────
REPO="Tnsor-Labs/brokoli"
VERSION="${BROKOLI_VERSION:-latest}"

# Require curl OR wget.
if command -v curl >/dev/null 2>&1; then
    HTTP_GET='curl -fsSL'
    HTTP_HEAD='curl -fsSLI'
elif command -v wget >/dev/null 2>&1; then
    HTTP_GET='wget -qO-'
    HTTP_HEAD='wget -q --spider -S'
else
    die "neither curl nor wget found — install one of them and retry."
fi

command -v tar >/dev/null 2>&1 || die "tar not found — install it and retry."

# ───────────────── platform detection ─────────────────
OS=$(uname -s)
ARCH=$(uname -m)

case "$OS" in
    Linux)   OS_ID="Linux"   ;;
    Darwin)  OS_ID="Darwin"  ;;
    *)       die "unsupported OS: $OS (Brokoli supports Linux and macOS; Windows users should use WSL)" ;;
esac

case "$ARCH" in
    x86_64|amd64)  ARCH_ID="x86_64" ;;
    arm64|aarch64) ARCH_ID="arm64"  ;;
    *)             die "unsupported architecture: $ARCH" ;;
esac

info "Detected platform: ${OS_ID}_${ARCH_ID}"

# ───────────────── resolve version ─────────────────
if [ "$VERSION" = "latest" ]; then
    step "Looking up latest release…"
    # /releases/latest redirects to /releases/tag/vX.Y.Z — grab the final URL.
    if command -v curl >/dev/null 2>&1; then
        RESOLVED=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
            "https://github.com/${REPO}/releases/latest")
    else
        RESOLVED=$(wget --max-redirect=5 --spider -S \
            "https://github.com/${REPO}/releases/latest" 2>&1 \
            | awk '/^Location:/ {print $2}' | tail -1)
    fi
    VERSION=$(printf '%s\n' "$RESOLVED" | sed -E 's|.*/tag/(v[^/]+).*|\1|')
    [ -n "$VERSION" ] || die "could not resolve latest version (check network / GitHub status)"
fi
info "Installing Brokoli $VERSION"

# ───────────────── pick install dir ─────────────────
if [ -n "${BROKOLI_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$BROKOLI_INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="$HOME/.local/bin"
fi
# Always ensure the install dir exists — applies to both the
# BROKOLI_INSTALL_DIR override and the $HOME/.local/bin fallback.
mkdir -p "$INSTALL_DIR" 2>/dev/null || die "could not create $INSTALL_DIR"

case ":$PATH:" in
    *":$INSTALL_DIR:"*) PATH_HINT="" ;;
    *) PATH_HINT="  (not on \$PATH — add 'export PATH=\"$INSTALL_DIR:\$PATH\"' to your shell config)" ;;
esac

# ───────────────── download + extract ─────────────────
ASSET="brokoli_${OS_ID}_${ARCH_ID}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
TMP=$(mktemp -d 2>/dev/null || mktemp -d -t brokoli-install)
trap 'rm -rf "$TMP"' EXIT INT TERM

step "Downloading $ASSET…"
if command -v curl >/dev/null 2>&1; then
    curl -fSL --progress-bar "$URL" -o "$TMP/$ASSET" \
        || die "download failed: $URL"
else
    wget -q --show-progress "$URL" -O "$TMP/$ASSET" \
        || die "download failed: $URL"
fi

step "Extracting…"
tar -xzf "$TMP/$ASSET" -C "$TMP" || die "extract failed"
[ -f "$TMP/brokoli" ] || die "archive did not contain a 'brokoli' binary"
chmod +x "$TMP/brokoli"

step "Installing to $INSTALL_DIR/brokoli"
mv "$TMP/brokoli" "$INSTALL_DIR/brokoli" \
    || die "could not write to $INSTALL_DIR — set BROKOLI_INSTALL_DIR or run with sudo"

INSTALLED_VERSION=$("$INSTALL_DIR/brokoli" --version 2>/dev/null || echo "$VERSION")
info "Brokoli $INSTALLED_VERSION installed${PATH_HINT}"

# ───────────────── interactive first-run setup ─────────────────
#
# curl | sh pipes the script to stdin, so `read` from stdin fails. We read
# from /dev/tty instead, which is the real user terminal even when stdin is
# a pipe. This is the same trick rustup / bun / homebrew use.
#
setup_interactively() {
    if [ "${BROKOLI_NO_SETUP:-}" = "1" ]; then return 1; fi
    [ -e /dev/tty ] || return 1
    return 0
}

if setup_interactively; then
    printf '\n'
    printf '%sWould you like to start Brokoli now and create an admin user? [Y/n] %s' "$BOLD" "$RESET"
    if [ "${BROKOLI_YES:-}" = "1" ]; then
        ANS="y"; printf 'y\n'
    else
        read -r ANS </dev/tty || ANS="n"
    fi
    case "$ANS" in
        ''|y|Y|yes|YES|Yes) ;;
        *)
            printf '\n'
            info "Skipped setup. Start Brokoli yourself with:"
            printf '    %sbrokoli serve%s\n' "$BOLD" "$RESET"
            exit 0
            ;;
    esac

    # Check port availability before starting.
    PORT="${BROKOLI_PORT:-8080}"
    if command -v ss >/dev/null 2>&1 && ss -tln 2>/dev/null | grep -q ":${PORT} "; then
        warn "port ${PORT} is already in use — skipping auto-start"
        warn "start manually on a different port: brokoli serve --port 9090"
        exit 0
    fi

    # Collect admin credentials.
    DEFAULT_USER="admin"
    printf '    admin username [%s]: ' "$DEFAULT_USER"
    read -r ADMIN_USER </dev/tty || ADMIN_USER=""
    [ -n "$ADMIN_USER" ] || ADMIN_USER="$DEFAULT_USER"

    # Password must be 10+ chars (validated by the server).
    while :; do
        printf '    admin password (10+ chars): '
        stty -echo 2>/dev/null || true
        read -r ADMIN_PASS </dev/tty || ADMIN_PASS=""
        stty echo 2>/dev/null || true
        printf '\n'
        [ ${#ADMIN_PASS} -ge 10 ] && break
        warn "password too short, try again"
    done

    # Start the server in the background.
    LOG="$HOME/.brokoli-install.log"
    info "Starting Brokoli on http://localhost:${PORT}…"
    nohup "$INSTALL_DIR/brokoli" serve --port "$PORT" >"$LOG" 2>&1 &
    SERVER_PID=$!

    # Wait up to 20s for the health endpoint.
    i=0
    while [ $i -lt 40 ]; do
        if $HTTP_GET "http://localhost:${PORT}/health" >/dev/null 2>&1; then
            break
        fi
        i=$((i + 1))
        sleep 0.5
    done
    if [ $i -ge 40 ]; then
        warn "server did not come up in 20s — check $LOG"
        exit 1
    fi

    # Create the first user via the unauthenticated setup endpoint.
    step "Creating admin user '$ADMIN_USER'…"
    PAYLOAD=$(printf '{"username":"%s","password":"%s","role":"admin"}' \
        "$ADMIN_USER" "$ADMIN_PASS")
    if command -v curl >/dev/null 2>&1; then
        RESP=$(curl -fsS -X POST "http://localhost:${PORT}/api/auth/users" \
            -H 'Content-Type: application/json' \
            -d "$PAYLOAD" 2>&1) || { warn "admin creation failed: $RESP"; exit 1; }
    else
        RESP=$(wget -qO- --post-data="$PAYLOAD" \
            --header='Content-Type: application/json' \
            "http://localhost:${PORT}/api/auth/users" 2>&1) \
            || { warn "admin creation failed: $RESP"; exit 1; }
    fi

    printf '\n'
    info "Ready to go"
    printf '    %sURL:%s       http://localhost:%s\n' "$BOLD" "$RESET" "$PORT"
    printf '    %susername:%s  %s\n'                  "$BOLD" "$RESET" "$ADMIN_USER"
    printf '    %spassword:%s  (the one you just set)\n' "$BOLD" "$RESET"
    printf '\n'
    printf '    %sserver log:%s  %s\n' "$DIM" "$RESET" "$LOG"
    printf '    %sstop with:%s   kill %s\n'           "$DIM" "$RESET" "$SERVER_PID"
    printf '\n'
    printf '    Next: open the URL and trigger your first pipeline from the\n'
    printf '    sample templates in the Pipelines tab.\n'
else
    printf '\n'
    info "Installed. Next steps:"
    printf '    %sbrokoli serve%s                # start the server on :8080\n' "$BOLD" "$RESET"
    printf '    open http://localhost:8080      # open the UI\n'
    printf '\n'
    printf '%sDocs:%s  https://docs.brokoli.orkestri.site\n' "$DIM" "$RESET"
fi
