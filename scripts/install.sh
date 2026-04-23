#!/bin/sh
# install.sh — bootstrap rt-node-agent on Linux and macOS.
#
# Usage (both distros):
#   curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo sh
#   curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sudo RT_AGENT_VERSION=v0.1.0 sh
#
# Must run as root — it writes to /usr/local/bin and registers a system
# service (systemd on Linux, launchd on macOS). Running via `curl | sh`
# without sudo breaks when /usr/local/bin isn't user-writable because
# the pipe has no tty for sudo's password prompt.
#
# Idempotent. Does not generate a token (operator sets /etc/rt-node-agent/token
# after install). Exits non-zero on any failure.

set -eu

REPO="redtorchinc/node-agent"
BINARY="rt-node-agent"
INSTALL_DIR="${RT_AGENT_INSTALL_DIR:-/usr/local/bin}"
VERSION="${RT_AGENT_VERSION:-latest}"

err() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
info() { printf 'install.sh: %s\n' "$*"; }

# --- root check ---
if [ "$(id -u)" -ne 0 ]; then
  err "must run as root. Retry: curl -fsSL <url>/install.sh | sudo sh"
fi

# --- detect OS/arch ---
uname_s=$(uname -s)
case "$uname_s" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) err "unsupported OS: $uname_s (install.ps1 handles Windows)" ;;
esac

uname_m=$(uname -m)
case "$uname_m" in
  x86_64|amd64)          arch=amd64 ;;
  aarch64|arm64)         arch=arm64 ;;
  *) err "unsupported arch: $uname_m" ;;
esac

asset="${BINARY}_${os}_${arch}"

# --- resolve download URL ---
if [ "$VERSION" = "latest" ]; then
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
  sig_url="${url}.minisig"
else
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  sig_url="${url}.minisig"
fi

# --- download ---
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

info "downloading $asset ($os/$arch)"
curl -fsSL -o "$tmp/$asset" "$url" || err "download failed: $url"

# --- optional signature verification ---
# When minisign is installed AND a .minisig asset is published, verify.
# When either is missing, warn and continue (v0.1.0 releases are unsigned).
if command -v minisign >/dev/null 2>&1; then
  if curl -fsSL -o "$tmp/$asset.minisig" "$sig_url" 2>/dev/null; then
    pubkey="RWS_PLACEHOLDER_PUBKEY_REPLACE_AT_FIRST_SIGNED_RELEASE"
    printf '%s\n' "$pubkey" > "$tmp/rt-node-agent.pub"
    if ! minisign -V -p "$tmp/rt-node-agent.pub" -m "$tmp/$asset" >/dev/null 2>&1; then
      err "signature verification failed for $asset"
    fi
    info "signature verified"
  else
    info "no signature published for this release; skipping verify"
  fi
else
  info "minisign not installed; skipping signature verification"
fi

# --- install binary ---
chmod +x "$tmp/$asset"
install -m 0755 "$tmp/$asset" "$INSTALL_DIR/$BINARY"
info "installed $INSTALL_DIR/$BINARY"

# --- register system service ---
# `rt-node-agent install` dispatches to internal/service/{systemd.go,launchd.go}
# based on the build-tag — systemd on Linux, launchd on macOS.
info "registering system service"
"$INSTALL_DIR/$BINARY" install

# --- healthcheck ---
sleep 1
port=${RT_AGENT_PORT:-11435}
if curl -fsS "http://127.0.0.1:${port}/version" >/dev/null 2>&1; then
  info "rt-node-agent is running on port ${port}"
else
  err "rt-node-agent did not respond on port ${port}; check service logs"
fi

info "done. health: http://127.0.0.1:${port}/health"
info "next: echo <token> | sudo tee /etc/rt-node-agent/token  (then restart service) to enable /actions/*"
