---
title: Explore hosts and their history
summary: Find hosts, read their hardware, software and network inventory, compare snapshots, and refresh, retire or delete a host.
kind: guide
order: 20
tags: [inventory, hosts, snapshots, changes, diff]
requires: inventory:read
---
Open **Hosts** in the menu. Everyone with `inventory:read` sees the hosts of
the tenant; the snapshot history needs `snapshots:read`.

## Find a host

The list shows host name, OS, manufacturer, model, status, whether the agent
is online and when the host last reported. Filter by:

| Filter | Matches |
|---|---|
| Hostname | part of the name, any case |
| OS name | the exact OS name |
| Manufacturer | the manufacturer |
| Status | `active`, `stale` or `retired` |
| Last seen | within the last 24 hours, 7 days or 30 days |
| Tag | `key` or `key=value` |

A host becomes `stale` when it has not reported for a day (the platform
default). Select a host to open it.

## Read the inventory

The host page shows a summary (manufacturer, model, OS, agent version,
serial, first and last seen, tags) and these tabs, all from the latest
snapshot:

| Tab | Contents |
|---|---|
| Hardware | BIOS, system, baseboard, chassis, processors, memory arrays and every memory slot, physical disks and filesystems, monitors |
| Software | operating system, environment, update state, installed programs, services, users, patches |
| Network | addressing, network interfaces with addresses, VLAN and speed, BMC (Linux), hypervisor guests (Proxmox) |
| History | snapshots, change history and snapshot comparison |
| Certificates | certificates delivered to the host; see [certificates on hosts](certificates.md) |

A category the agent could not read on that platform is empty or marked
"Not collected"; this is not an error. When the agent dropped entries above
its limits, a warning says so.

## History and changes

The **History** tab lists every snapshot (collected, received, source, agent
version) and the **change history**: for each report, the components added,
removed or modified, with category and component. Categories include
`bios`, `system`, `processor`, `memory`, `disk`, `network`, `software`,
`service`, `os`, `user`, `patch`, `update`, `bmc` and `guest`.

To compare any two snapshots, choose them under **Compare snapshots**
(**From**, **To**) and choose **Compare**. The result groups the differences
into added, removed and modified, with the value before and after.

Snapshots older than the retention period (90 days by default) are removed
automatically.

## Refresh, retire and delete

These buttons are at the top of the host page.

| Action | Needs | What it does |
|---|---|---|
| **Refresh** | `agents:manage` | asks the host's agent to report now; it must be online |
| **Retire** | `hosts:manage` | marks the host `retired`; its history stays. Retired hosts receive no certificate or module deliveries |
| **Delete** | `hosts:manage` | removes the host with all its snapshots and change history, cancels its deliveries |

Delete only hosts that are gone for good: if the agent is still running, its
next report creates the host again, without the old history.

## Tags

Tags are `key=value` labels on a host, for example `role=web`. They are used
to filter the list, and by the deployer to choose hosts for a certificate.
The portal shows tags but has no editor yet; set them with
`POST /api/inventory/v1/hosts/{id}/tags` (`hosts:manage`). The request
replaces all tags of the host:

```json
{"tags": {"role": "web", "env": "prod"}}
```

Related: [reference](../reference.md#host-fields).
