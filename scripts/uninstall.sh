#!/bin/sh
# uninstall.sh — remove rt-node-agent on Linux and macOS.

set -eu

BINARY=/usr/local/bin/rt-node-agent

if [ ! -x "$BINARY" ]; then
  printf 'uninstall.sh: %s not found; nothing to do\n' "$BINARY" >&2
  exit 0
fi

if [ "$(id -u)" -eq 0 ]; then
  "$BINARY" uninstall
  rm -f "$BINARY"
else
  sudo "$BINARY" uninstall
  sudo rm -f "$BINARY"
fi

printf 'rt-node-agent removed. Config preserved at /etc/rt-node-agent/\n'
