#!/bin/sh
# deb: $1=remove|upgrade|deconfigure. rpm: $1=0 remove, 1 upgrade.
set -e
case "$1" in
  remove|deconfigure|0)
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
      systemctl disable --now inventory-agent.service >/dev/null 2>&1 || true
    fi
    ;;
esac
exit 0
