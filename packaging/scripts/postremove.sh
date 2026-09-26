#!/bin/sh
# deb: $1=remove|purge|upgrade. rpm: $1=0 remove, 1 upgrade.
set -e
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
fi
# A purge also forgets the agent's credential and state.
if [ "$1" = purge ]; then
  rm -rf /var/lib/inventory-agent
fi
exit 0
