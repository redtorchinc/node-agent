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

# macOS attaches com.apple.quarantine to curl-downloaded binaries. /usr/bin/install
# doesn't propagate xattrs across the copy, but strip defensively on the
# destination too — costs nothing on Linux (xattr absent → silent) and prevents
# Gatekeeper from interfering with launchd on macOS Sequoia.
if [ "$os" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$INSTALL_DIR/$BINARY" 2>/dev/null || true
fi

# --- register system service ---
# `rt-node-agent install` dispatches to internal/service/{systemd.go,launchd.go}
# based on the build-tag — systemd on Linux, launchd on macOS.
info "registering system service"
"$INSTALL_DIR/$BINARY" install

# --- healthcheck ---
# Poll up to 15s. On macOS the agent's StartBackground performs a GPU /
# system_profiler probe + a database pre-warm before binding the port,
# which can take several seconds on a slow box or one with a hung
# nvidia-smi. A single 1s sleep races that startup and emits false
# negatives — be patient.
port=${RT_AGENT_PORT:-11435}
attempts=30
sleep_per_attempt=0.5
i=0
while [ "$i" -lt "$attempts" ]; do
  if curl -fsS "http://127.0.0.1:${port}/version" >/dev/null 2>&1; then
    info "rt-node-agent is running on port ${port}"
    break
  fi
  i=$((i + 1))
  sleep "$sleep_per_attempt"
done
if [ "$i" -ge "$attempts" ]; then
  err "rt-node-agent did not respond on port ${port} after 15s; check service logs"
fi

info "done. health: http://127.0.0.1:${port}/health"
info "the bearer token above is what the case-manager backend uses for POST /actions/*."

# --- config migration banner ---
# Two paths the installer's internal migrate may have taken:
#   1. Existing config parses cleanly but is missing v0.2.0 keys →
#      .new file is dropped alongside; operator reviews and `mv`s.
#   2. Existing config was malformed YAML → auto-recovery: original
#      backed up to .broken-<unix-ts>, fresh defaults laid down at the
#      original path so the service can start. Surface this loudly.
if ls /etc/rt-node-agent/config.yaml.broken-* >/dev/null 2>&1; then
  broken=$(ls -t /etc/rt-node-agent/config.yaml.broken-* | head -1)
  info ""
  info "*** existing config.yaml was malformed YAML — auto-recovered: ***"
  info "    your original: $broken"
  info "    fresh config:  /etc/rt-node-agent/config.yaml"
  info "    review the old, copy over any settings you'd customised, restart"
fi
if [ -f /etc/rt-node-agent/config.yaml.new ]; then
  if [ "$os" = "darwin" ]; then
    restart_cmd="sudo launchctl kickstart -k system/com.redtorch.rt-node-agent"
  else
    restart_cmd="sudo systemctl restart rt-node-agent"
  fi
  info ""
  info "*** new config keys available — review and merge: ***"
  info "    diff /etc/rt-node-agent/config.yaml /etc/rt-node-agent/config.yaml.new"
  info "    sudo mv /etc/rt-node-agent/config.yaml.new /etc/rt-node-agent/config.yaml"
  info "    $restart_cmd"
fi
