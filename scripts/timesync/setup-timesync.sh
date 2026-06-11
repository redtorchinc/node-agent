#!/bin/sh
# setup-timesync.sh — stand up chrony for the RedTorch fleet's time sync.
#
# Topology:
#   * ONE box is the local NTP "server" — it disciplines itself from public
#     NTP over the WAN uplink (e.g. Starlink) and serves the LAN. It keeps
#     serving at a usable stratum even if the uplink drops (`local stratum`).
#   * EVERY node is a "client" — it disciplines its clock from the local
#     server only (no internet needed on the nodes).
#
# Usage (run as root on each box):
#   # on the NTP server box — pass the LAN/CIDR that may query it:
#   sudo ./setup-timesync.sh server 10.0.0.0/24
#   # (optionally append upstream NTP servers; defaults shown below)
#   sudo ./setup-timesync.sh server 10.0.0.0/24 time.cloudflare.com 2.pool.ntp.org
#
#   # on every node — pass the NTP server's LAN IP/host:
#   sudo ./setup-timesync.sh client 10.0.0.10
#
# After this runs, rt-node-agent measures the result automatically:
#   * OS-sync fields (time_sync.source=chrony / skew_ms / stratum) populate
#     from `chronyc tracking` on every box.
#   * Point the agent's `timesync.server` at the NTP server's IP to also get
#     Offset A (node↔reference) and feed GET /time / clock_offset_high.
#
# Idempotent: backs up any existing chrony.conf before writing. Exits
# non-zero on any failure. Installs chrony via the host package manager.

set -eu

err()  { printf 'setup-timesync.sh: %s\n' "$*" >&2; exit 1; }
info() { printf 'setup-timesync.sh: %s\n' "$*"; }

[ "$(id -u)" = "0" ] || err "must run as root (chrony install + /etc writes)"

ROLE="${1:-}"
case "$ROLE" in
  server)
    LAN="${2:-}"
    [ -n "$LAN" ] || err "server role needs a LAN CIDR, e.g. server 10.0.0.0/24"
    shift 2  # drop 'server' + CIDR; any remaining args are upstream NTP hosts
    UPSTREAM="$*"
    [ -n "$UPSTREAM" ] || UPSTREAM="time.cloudflare.com 2.pool.ntp.org"
    ;;
  client)
    NTP_SERVER="${2:-}"
    [ -n "$NTP_SERVER" ] || err "client role needs the NTP server IP/host, e.g. client 10.0.0.10"
    ;;
  *)
    err "first arg must be 'server' or 'client' (see header for usage)"
    ;;
esac

# --- locate the host's chrony layout (Debian/Ubuntu vs RHEL/SUSE differ) ---
install_chrony() {
  if command -v chronyd >/dev/null 2>&1; then info "chrony already installed"; return; fi
  if   command -v apt-get >/dev/null 2>&1; then apt-get update -qq && apt-get install -y chrony
  elif command -v dnf     >/dev/null 2>&1; then dnf install -y chrony
  elif command -v yum     >/dev/null 2>&1; then yum install -y chrony
  elif command -v zypper  >/dev/null 2>&1; then zypper --non-interactive install chrony
  else err "no supported package manager (apt/dnf/yum/zypper) — install chrony manually"
  fi
}

# Debian/Ubuntu: /etc/chrony/chrony.conf + service 'chrony'.
# RHEL/SUSE:     /etc/chrony.conf       + service 'chronyd'.
conf_path() {
  if [ -d /etc/chrony ]; then echo /etc/chrony/chrony.conf; else echo /etc/chrony.conf; fi
}
service_name() {
  if systemctl list-unit-files 2>/dev/null | grep -q '^chrony\.service'; then echo chrony; else echo chronyd; fi
}

install_chrony
CONF="$(conf_path)"
SVC="$(service_name)"

if [ -f "$CONF" ]; then
  BAK="$CONF.bak.$(date +%s)"
  cp "$CONF" "$BAK"
  info "backed up existing config to $BAK"
fi

if [ "$ROLE" = "server" ]; then
  {
    echo "# Managed by rt-node-agent setup-timesync.sh (role: server)."
    echo "# Disciplines from public NTP over the WAN uplink; serves the LAN."
    for u in $UPSTREAM; do echo "server $u iburst"; done
    echo "# Keep serving the LAN at a usable stratum if the uplink (Starlink) drops:"
    echo "local stratum 10"
    echo "allow $LAN"
    echo "driftfile /var/lib/chrony/drift"
    echo "makestep 1.0 3"
    echo "rtcsync"
  } > "$CONF"
  info "wrote server config ($CONF): upstream='$UPSTREAM' allow=$LAN"
else
  {
    echo "# Managed by rt-node-agent setup-timesync.sh (role: client)."
    echo "# Disciplines from the local fleet NTP server only — no internet needed."
    echo "server $NTP_SERVER iburst"
    echo "driftfile /var/lib/chrony/drift"
    echo "makestep 1.0 3"
    echo "rtcsync"
  } > "$CONF"
  info "wrote client config ($CONF): server=$NTP_SERVER"
fi

systemctl enable "$SVC" >/dev/null 2>&1 || true
systemctl restart "$SVC" || err "failed to (re)start $SVC — check 'journalctl -u $SVC'"
info "$SVC restarted"

# Give chrony a moment to make first contact, then show state.
sleep 2
info "--- chronyc tracking ---"
chronyc tracking || true
info "--- chronyc sources ---"
chronyc -n sources || true

info "done. rt-node-agent will now report time_sync.source=chrony on this box."
[ "$ROLE" = "client" ] && info "remember: set the agent's timesync.server: $NTP_SERVER for Offset A + GET /time."
exit 0
