#!/bin/sh
# install.sh -- install git-remote-arweave without root or pre-installed Go.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/git-remote-arweave/git-remote-arweave/refs/heads/main/install.sh | bash
#
# Installs the binary to ~/.local/bin (created if needed).
# Downloads a temporary Go toolchain, builds from source, then cleans up.

set -eu

REPO="https://github.com/git-remote-arweave/git-remote-arweave"
GO_VERSION="1.26.1"
INSTALL_DIR="${HOME}/.local/bin"

# --- helpers ----------------------------------------------------------------

die() { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }
info() { printf '\033[1m%s\033[0m\n' "$*"; }

detect_platform() {
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m)"

  case "$OS" in
    linux)  GOOS="linux" ;;
    darwin) GOOS="darwin" ;;
    *)      die "unsupported OS: $OS" ;;
  esac

  case "$ARCH" in
    x86_64|amd64)   GOARCH="amd64" ;;
    aarch64|arm64)   GOARCH="arm64" ;;
    *)               die "unsupported architecture: $ARCH" ;;
  esac
}

check_deps() {
  for cmd in git curl tar; do
    command -v "$cmd" >/dev/null 2>&1 || die "'$cmd' is required but not found"
  done
}

# --- main -------------------------------------------------------------------

check_deps
detect_platform

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# 1. Download Go toolchain
GO_TARBALL="go${GO_VERSION}.${GOOS}-${GOARCH}.tar.gz"
GO_URL="https://go.dev/dl/${GO_TARBALL}"

info "Downloading Go ${GO_VERSION} (${GOOS}/${GOARCH})..."
curl -fsSL "$GO_URL" -o "${TMPDIR}/${GO_TARBALL}"
tar -xzf "${TMPDIR}/${GO_TARBALL}" -C "$TMPDIR"

export GOROOT="${TMPDIR}/go"
export GOPATH="${TMPDIR}/gopath"
export GOMODCACHE="${GOPATH}/pkg/mod"
export PATH="${GOROOT}/bin:${PATH}"

# 2. Clone and build
info "Cloning git-remote-arweave..."
git clone --depth 1 "$REPO" "${TMPDIR}/src" 2>&1 | grep -v "^$" || true

cd "${TMPDIR}/src"
VERSION="$(git describe --tags --always 2>/dev/null || echo unknown)"

info "Building..."
go build -ldflags "-X main.version=${VERSION}" -o git-remote-arweave ./cmd/git-remote-arweave/
go build -o arweave-git ./cmd/arweave-git/

# 3. Install
mkdir -p "$INSTALL_DIR"
mv git-remote-arweave "${INSTALL_DIR}/git-remote-arweave"
mv arweave-git "${INSTALL_DIR}/arweave-git"
chmod 755 "${INSTALL_DIR}/git-remote-arweave" "${INSTALL_DIR}/arweave-git"

info "Installed: ${INSTALL_DIR}/git-remote-arweave"
info "Installed: ${INSTALL_DIR}/arweave-git"

# 4. Check PATH
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    printf '\n\033[33m%s is not in your PATH.\033[0m\n' "$INSTALL_DIR"
    echo "Add it to your shell profile:"
    echo ""
    echo "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc"
    echo ""
    ;;
esac
