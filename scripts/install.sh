#!/bin/sh
# install.sh — curl|sh bootstrap for rt-node-agent on Linux and macOS.
#
# Usage:
#   curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | sh
#   curl -fsSL https://github.com/redtorchinc/node-agent/releases/latest/download/install.sh | RT_AGENT_VERSION=v0.1.0 sh
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

info "downloading $asset"
curl -fsSL -o "$tmp/$asset" "$url" || err "download failed: $url"

# Signature verification is best-effort: if minisign isn't available, warn
# but continue. Hard-fail once minisign is standard on target hosts.
if command -v minisign >/dev/null 2>&1; then
  if curl -fsSL -o "$tmp/$asset.minisig" "$sig_url" 2>/dev/null; then
    # The public key is pinned here; any rotation requires a new install.sh.
    pubkey="RWS_PLACEHOLDER_PUBKEY_REPLACE_AT_FIRST_RELEASE_TAG"
    printf '%s\n' "$pubkey" > "$tmp/rt-node-agent.pub"
    if ! minisign -V -p "$tmp/rt-node-agent.pub" -m "$tmp/$asset" >/dev/null 2>&1; then
      err "signature verification failed for $asset"
    fi
    info "signature verified"
  else
    info "no signature published yet (pre-M11); skipping verify"
  fi
else
  info "minisign not installed; skipping signature verification (install minisign to harden)"
fi

# --- install ---
chmod +x "$tmp/$asset"
if [ -w "$INSTALL_DIR" ]; then
  mv "$tmp/$asset" "$INSTALL_DIR/$BINARY"
else
  info "installing to $INSTALL_DIR requires sudo"
  sudo install -m 0755 "$tmp/$asset" "$INSTALL_DIR/$BINARY"
fi

# --- register service ---
info "registering system service"
if [ "$(id -u)" -eq 0 ]; then
  "$INSTALL_DIR/$BINARY" install
else
  sudo "$INSTALL_DIR/$BINARY" install
fi

# --- healthcheck ---
# Give the service a moment to bind the port, then verify.
sleep 1
port=${RT_AGENT_PORT:-11435}
if curl -fsS "http://127.0.0.1:${port}/version" >/dev/null 2>&1; then
  info "rt-node-agent is running on port ${port}"
else
  err "rt-node-agent did not respond on port ${port}; check service logs"
fi

info "done. /health: http://127.0.0.1:${port}/health"
info "next: write a token to $( [ "$os" = darwin ] && echo /etc/rt-node-agent/token || echo /etc/rt-node-agent/token ) to enable /actions/*"
