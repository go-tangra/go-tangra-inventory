#!/bin/sh
# Example modules.deploy_hook for the inventory agent (feature 037): start a
# module the platform just delivered with Docker Compose.
#
# Install it root-owned and not writable by group or others, in a root-owned
# directory, and name it in /etc/inventory-agent/agent.yaml:
#   install -o root -g root -m 0755 module-compose-up.sh /usr/local/sbin/module-compose-up
#   modules:
#     deploy_hook: /usr/local/sbin/module-compose-up
#
# The agent runs it directly (no shell, no arguments) after writing
# <modules.directory>/<module>/, with the working directory MODULE_DIR and
# only PATH, LANG, MODULE_NAME, MODULE_VERSION and MODULE_DIR set. Its output
# stays in the agent's log; a non-zero exit is reported as hook_failed.
set -eu

case "${MODULE_NAME:-}" in
  "" | */* | .*) echo "module-compose-up: MODULE_NAME is not a module name" >&2; exit 2 ;;
esac
if [ "$(pwd -P)" != "$(cd "${MODULE_DIR:?}" && pwd -P)" ]; then
  echo "module-compose-up: not started in MODULE_DIR" >&2
  exit 2
fi

echo "module-compose-up: starting ${MODULE_NAME} ${MODULE_VERSION:-}"
exec docker compose --project-name "${MODULE_NAME}" up -d --remove-orphans
