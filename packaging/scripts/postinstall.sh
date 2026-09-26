#!/bin/sh
# deb: $1=configure, $2=previous version on upgrade. rpm: $1=1 install, 2 upgrade.
set -e
upgrade=0
case "$1" in
  configure) [ -n "$2" ] && upgrade=1 ;;
  2) upgrade=1 ;;
esac
mkdir -p /var/lib/inventory-agent
chmod 0700 /var/lib/inventory-agent
chmod 0640 /etc/inventory-agent/agent.yaml 2>/dev/null || true
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
  if [ "$upgrade" = 1 ]; then
    systemctl try-restart inventory-agent.service || true
  else
    systemctl enable inventory-agent.service >/dev/null 2>&1 || true
    echo "inventory-agent: set ingest_endpoint in /etc/inventory-agent/agent.yaml, put the enrollment token in /etc/inventory-agent/enrollment.token (mode 0600), then: systemctl start inventory-agent"
  fi
fi
exit 0
