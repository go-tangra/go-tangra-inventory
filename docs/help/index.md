---
title: Inventory
summary: Collect hardware, software and network inventory from Windows and Linux hosts with an agent, keep every snapshot and see what changed.
kind: guide
order: 0
tags: [inventory, hosts, agents, overview]
---
Inventory keeps an up-to-date record of your computers and servers. A small
program, the **inventory agent**, runs on each host, collects what is
installed and reports it to the platform. Inventory stores every report and
shows what changed between two of them.

## Concepts

- **Host** — one computer or server. Inventory recognises a host by its
  hardware UUID, then its machine ID, then its host name, so a host keeps its
  history when it is renamed or reinstalled on the same hardware.
- **Agent** — `inventory-agent` on the host (Windows or Linux). It enrolls
  once, with a single-use **enrollment token** or a tenant **enrollment key**,
  and then reports on an interval (every hour by default) and whenever you
  ask for a refresh.
- **Snapshot** — one full report from an agent: hardware (BIOS, system,
  processors, memory slots, disks), operating system, installed programs,
  services, users, patches, network interfaces, and on Linux BMC settings,
  hypervisor guests and pending updates. Snapshots are never changed.
- **Change** — a difference between a snapshot and the one before it: a
  component added, removed or modified.

Linux agents can also receive certificates from the deployer and install them
on the host; see [certificates on hosts](guides/certificates.md).

## Pages in the portal

| Page | Shows | Needs |
|---|---|---|
| Hosts | every host, its latest inventory, history and certificates | `inventory:read` |
| Agents | enrolled agents, enrollment tokens and keys, upgrades | `agents:manage` |
| Dashboard | host counts by status, OS and manufacturer, totals of RAM, cores and disk | `stats:read` |

Pages update live when agents connect, disconnect or report. Your role
decides what you can do; see
[permissions and roles](reference.md#permissions-and-roles).

## Where to go next

- [Get started](getting-started.md): enroll your first Linux host.
- [Explore hosts and their history](guides/hosts.md)
- [Manage agents and upgrades](guides/agents.md)
- [Certificates on hosts](guides/certificates.md)
- [Example: enroll a rack of servers with an enrollment key](examples/auto-enroll-servers.md)
- [Reference](reference.md): fields, states, permissions, API and limits.
