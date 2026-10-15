---
title: Manage agents and upgrades
summary: Watch the agent fleet, refresh and revoke agents, upgrade them by hand or automatically in a maintenance window.
kind: guide
order: 30
tags: [inventory, agents, upgrades, enrollment]
requires: agents:manage
---
Open **Agents** in the menu (`agents:manage`). The page lists every enrolled
agent, online or not, and the current agent version of the platform.

| Column | Meaning |
|---|---|
| Status | online or offline |
| Hostname, Agent ID | the host and the agent |
| Version, Target | installed version, and the version an upgrade is moving it to |
| Upgrade | the upgrade state, see below |
| Certificates | whether the agent can receive certificates |
| Enrolled with | `token`, or the enrollment key it used |
| Last seen, Last change | last contact, last upgrade state change |

## Enroll agents

- **Issue enrollment token** mints a single-use token, valid for 24 hours.
  It is shown once. See [get started](../getting-started.md).
- **Automatic enrollment** lets many agents enroll with one tenant key,
  restricted to the networks you name. See
  [the enrollment-key example](../examples/auto-enroll-servers.md).

## Refresh and revoke

- **Refresh** asks an online agent to report now.
- **Revoke** withdraws the agent's credential, so the platform no longer
  accepts it. The host and its history stay; to report again, the agent must
  enroll again with a new token or key. Revoke agents of hosts that were rebuilt or handed over,
  and duplicate agents that claim the same host.

## Upgrade states

| State | Meaning |
|---|---|
| Up to date | runs the target version |
| Update available | an upgrade can be requested |
| Pending | requested; the agent receives it when it connects |
| Upgrading | downloading or installing |
| Failed | the upgrade failed; the reason is shown |
| Rolled back | the new version did not report in time and the old one was put back |
| Manual upgrade required | older than 4.4.0: install the current package by hand once |
| Unsupported | the agent cannot upgrade itself |

Self-upgrade needs systemd on Linux (or the Windows service). Agents verify
the release signature and checksums before installing anything, and roll
back when the new version does not connect and report within five minutes
(the agent's `upgrade.confirm_timeout_seconds`).

## Upgrade by hand

- **Upgrade** on a row, for agents in *Update available*, *Failed* or
  *Rolled back*.
- **Upgrade selected** for the rows you ticked.
- **Upgrade all outdated** for every outdated agent that supports
  self-upgrade.
- **Cancel** withdraws a *Pending* request.

Agents that are skipped are counted in the message with the reason, for
example "Already on the target version" or "Agent older than 4.4.0".

## Upgrade automatically

The **Automatic upgrades** card sets a policy for the tenant. Changing it
needs `agentupgrades:manage` (owners, admins and the *Inventory
administrator* role; not operators).

| Field | Meaning |
|---|---|
| Upgrade agents automatically | on or off (off by default) |
| Concurrent upgrades | at most this many at once (1–100) |
| Window start, Window end | `HH:MM`; the window may cross midnight; equal times mean the whole day |
| Timezone | IANA name, for example `Europe/Sofia` |
| Pinned version | empty: the current version. Otherwise a release stored on the platform; it applies to every upgrade in the tenant and may be lower (a downgrade), never below 4.4.0 |

Inside the window, outdated online agents are upgraded, oldest version
first. Until one agent of the tenant runs the target version, only one is
upgraded at a time. If an automatic upgrade fails or rolls back, the policy
**pauses**; check the agent, then choose **Resume**.

## On the host

```sh
inventory-agent update -check   # exit 0 up to date, 10 upgrade available, 1 error, 2 another upgrade running
inventory-agent update          # upgrade now (as root)
```

Set `upgrade.enabled: false` in `agent.yaml` to ignore upgrade requests
from the platform; `inventory-agent update` still works.

Related: [reference](../reference.md#agent-fields).
