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

# Pinned minisign public key for release-artifact verification.
#
# STILL A PLACEHOLDER: no project signing key has been generated yet, so no
# release carries signatures. The placeholder is detected explicitly below
# and skips verification. It is deliberately NOT handed to minisign — it
# would fail every check, and that path calls err(), which would hard-abort
# the install one-liner on every host that happens to have minisign
# installed, the moment signatures started being published.
#
# To enable signing, follow docs/releasing.md and replace this value with
# the real public key in the same commit that adds the secrets.
PUBKEY="RWS_PLACEHOLDER_PUBKEY_REPLACE_AT_FIRST_SIGNED_RELEASE"

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
# Three things must all hold to verify: a real pinned PUBKEY, minisign
# installed, and a .minisig published for this asset.
#
# The policy is fail-OPEN on "signing isn't set up" and fail-CLOSED on
# "signing is set up and the signature doesn't match". Getting that pairing
# right matters: the placeholder branch exists precisely so enabling signing
# can never turn into a fleet-wide install outage.
case "$PUBKEY" in
  RWS_PLACEHOLDER_PUBKEY_*)
    info "release signing not configured yet (placeholder pubkey); skipping signature verification"
    ;;
  "")
    # Not a signing state — the installer itself is broken. Fail closed with
    # a diagnostic, rather than handing an empty key to minisign and
    # reporting it as a signature mismatch.
    err "PUBKEY is empty — this installer is misconfigured; refusing to install. Re-download install.sh from the release assets."
    ;;
  *)
    if ! command -v minisign >/dev/null 2>&1; then
      info "minisign not installed; skipping signature verification"
    elif curl -fsSL -o "$tmp/$asset.minisig" "$sig_url" 2>/dev/null; then
      printf '%s\n' "$PUBKEY" > "$tmp/rt-node-agent.pub"
      if ! minisign -V -p "$tmp/rt-node-agent.pub" -m "$tmp/$asset" >/dev/null 2>&1; then
        err "signature verification FAILED for $asset — refusing to install"
      fi
      info "signature verified"
    else
      # Unsigned older release, or an asset published before signing was
      # switched on. Fail-open for backward compatibility; see the
      # downgrade-window note in docs/releasing.md.
      info "no signature published for this release; skipping verify"
    fi
    ;;
esac

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
# Migration writes in place since v0.2.7:
#   - Operator's customised values are grafted onto the new defaults.
#   - The previous file is backed up to config.yaml.bak (single file).
#   - The .broken-<ts> path is reserved for the auto-recovery branch
#     (malformed YAML → ForceReset).
# The rt-node-agent install subcommand printed its own banner during
# `rt-node-agent install`; the lines below are belt-and-suspenders so
# the operator still sees the path even if scrollback was lost.
if ls /etc/rt-node-agent/config.yaml.broken-* >/dev/null 2>&1; then
  broken=$(ls -t /etc/rt-node-agent/config.yaml.broken-* | head -1)
  info ""
  info "*** existing config.yaml was malformed YAML — auto-recovered: ***"
  info "    your original: $broken"
  info "    fresh config:  /etc/rt-node-agent/config.yaml"
  info "    review the old, copy over any settings you'd customised, restart"
fi
if [ -f /etc/rt-node-agent/config.yaml.bak ]; then
  if [ "$os" = "darwin" ]; then
    restart_cmd="sudo launchctl kickstart -k system/com.redtorch.rt-node-agent"
  else
    restart_cmd="sudo systemctl restart rt-node-agent"
  fi
  info ""
  info "*** config.yaml updated in place; previous version at config.yaml.bak ***"
  info "    diff /etc/rt-node-agent/config.yaml.bak /etc/rt-node-agent/config.yaml"
  info "    edit /etc/rt-node-agent/config.yaml to enable new features"
  info "    $restart_cmd"
fi
