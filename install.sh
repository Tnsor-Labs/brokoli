#!/bin/sh
# Broked installer — downloads the latest release for your platform.
# Usage: curl -fsSL https://raw.githubusercontent.com/hc12r/brokolisql-go/main/install.sh | sh

set -e

REPO="hc12r/brokolisql-go"
BINARY="broked"
INSTALL_DIR="/usr/local/bin"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
NC='\033[0m'

info() { printf "${CYAN}[broked]${NC} %s\n" "$1"; }
success() { printf "${GREEN}[broked]${NC} %s\n" "$1"; }
error() { printf "${RED}[broked]${NC} %s\n" "$1" >&2; exit 1; }

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="Linux" ;;
  darwin) OS="Darwin" ;;
  *)      error "Unsupported OS: $OS (only Linux and macOS are supported)" ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="x86_64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)             error "Unsupported architecture: $ARCH" ;;
esac

info "Detected platform: ${OS}/${ARCH}"

# Get latest release tag
info "Fetching latest release..."
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$LATEST" ]; then
  error "Could not determine latest release. Check https://github.com/${REPO}/releases"
fi
info "Latest version: ${LATEST}"

# Build download URL
ARCHIVE="broked_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ARCHIVE}"

# Download
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

info "Downloading ${URL}..."
if ! curl -fsSL -o "${TMPDIR}/${ARCHIVE}" "$URL"; then
  error "Download failed. Check if the release exists: https://github.com/${REPO}/releases/tag/${LATEST}"
fi

# Extract
info "Extracting..."
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"

# Install
if [ -w "$INSTALL_DIR" ]; then
  mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
  info "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi
chmod +x "${INSTALL_DIR}/${BINARY}"

# Verify
if command -v broked >/dev/null 2>&1; then
  success "Installed broked to ${INSTALL_DIR}/${BINARY}"
  echo ""
  info "Quick start:"
  echo "  broked serve              # Start on port 8080"
  echo "  broked serve --port 9900  # Custom port"
  echo ""
  info "Then open http://localhost:8080 in your browser."
else
  success "Installed to ${INSTALL_DIR}/${BINARY}"
  info "You may need to add ${INSTALL_DIR} to your PATH."
fi
