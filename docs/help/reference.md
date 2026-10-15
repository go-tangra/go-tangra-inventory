---
title: Reference
summary: Host, snapshot, change and agent fields, host statuses, upgrade and delivery states, permissions, roles, API routes and limits.
kind: reference
order: 60
tags: [inventory, reference, hosts, agents, permissions, api, limits]
---
## Host fields

| Field | Meaning |
|---|---|
| `id` | host ID |
| `hostname`, `machine_id`, `hardware_uuid` | identity; matched in the order hardware UUID, machine ID, host name |
| `system_serial`, `manufacturer`, `model` | from SMBIOS |
| `os_name`, `os_version`, `os_arch` | operating system |
| `agent_version` | version of the agent that last reported |
| `assigned_user` | as reported by the agent |
| `status` | see [host statuses](#host-statuses) |
| `tags` | `key: value` labels, set through the API |
| `first_seen`, `last_seen` | first and latest report |
| `last_snapshot_id` | the latest snapshot |

## Host statuses

| Status | Meaning |
|---|---|
| `active` | reporting |
| `stale` | no report for `stale.after_seconds` (one day by default) |
| `retired` | retired by a user; history kept, no certificate or module deliveries |

## Snapshot and change fields

A snapshot summary has `id`, `host_id`, `collected_at` (on the host),
`received_at`, `agent_version`, `source`, `os_name`, `os_version`,
`manufacturer` and `model`; the full snapshot carries the whole inventory.

A change has `category`, `change_type` (`added`, `removed`, `modified`),
`component_key`, `before` and `after` (JSON), `detected_at`, and the
snapshot and previous snapshot it compares. Categories: `bios`, `system`,
`baseboard`, `chassis`, `processor`, `memory`, `disk`, `network`, `monitor`,
`software`, `service`, `os`, `user`, `patch`, `virtualization`, `bmc`,
`guest`, `update`, `hardware_schema`.

## Agent fields

| Field | Meaning |
|---|---|
| `agent_id`, `host_id`, `hostname` | the agent and its host |
| `version`, `os`, `arch`, `install_type` | `linux`/`windows`, `amd64`/`arm64`, `deb`/`rpm`/`binary` |
| `online`, `connected_at`, `last_seen` | connection |
| `enrolled_via`, `auto_enroll_key_id` | `token` or `auto`, and the key used |
| `target_version`, `upgrade_state`, `upgrade_reason`, `state_changed_at` | upgrade progress |
| `certificate_capability` | `enabled`, `disabled_on_host`, `upgrade_required`, `not_supported_platform`, `disabled_on_server` |

Upgrade states: `up_to_date`, `available`, `pending`, `in_progress`,
`failed`, `rolled_back`, `manual_upgrade_required`, `unsupported`. Failure
reasons include `signature_invalid`, `unknown_key`, `checksum_mismatch`,
`size_mismatch`, `platform_mismatch`, `downgrade_refused`, `disk_full`,
`download_failed`, `install_failed`, `start_timeout`,
`unsupported_install` (no systemd), `busy` and `expired`.

## Certificate delivery states

`pending`, `delivered` and `fetched` are in progress; `installed`,
`unchanged`, `failed`, `hook_failed`, `unsupported`, `superseded`,
`expired` and `cancelled` are final. See
[certificates on hosts](guides/certificates.md#follow-deliveries).

## Permissions and roles

| Permission | Allows |
|---|---|
| `inventory:read` | list and read hosts and their latest inventory, certificates and deliveries; live updates |
| `inventory:write` | contribute inventory data; no portal page or route uses it yet |
| `hosts:manage` | tag, retire and delete hosts |
| `agents:manage` | enrollment tokens and keys, the agent list, refresh, upgrade and revoke agents, cancel certificate deliveries |
| `agentupgrades:manage` | change the automatic upgrade policy and the pinned version |
| `snapshots:read` | snapshot history, comparisons and change records |
| `snapshots:manage` | delete snapshots |
| `stats:read` | the dashboard |
| `backup:manage` | export and import the tenant's inventory |

| Module role | Permissions |
|---|---|
| Inventory administrator | all nine |
| Inventory editor | `inventory:read`, `inventory:write`, `snapshots:read` |
| Inventory viewer | `inventory:read`, `snapshots:read`, `stats:read` |

The platform's built-in roles also receive inventory permissions: `owner`
and `admin` all; `operator` all but `agentupgrades:manage` and
`backup:manage`; `member` `inventory:read`, `inventory:write`,
`snapshots:read`, `stats:read`; `auditor` `inventory:read`,
`snapshots:read`, `stats:read`.

## API

The browser API is under `/api/inventory/v1`, through the gateway. Changing
requests need the `X-CSRF-Token` header.

| Route | Permission |
|---|---|
| `GET /hosts`, `GET /hosts/{id}`, `GET /hosts/{id}/latest` | `inventory:read` |
| `POST /hosts/{id}/tags`, `POST /hosts/{id}/retire`, `DELETE /hosts/{id}` | `hosts:manage` |
| `GET /hosts/{id}/snapshots`, `GET /hosts/{id}/changes`, `GET /snapshots/{id}`, `GET /snapshots/{id}/diff/{other}` | `snapshots:read` |
| `DELETE /snapshots/{id}` | `snapshots:manage` |
| `GET /hosts/{id}/certificates`, `GET /certificate-deliveries`, `GET /certificate-deliveries/{item_id}` | `inventory:read` |
| `POST /certificate-deliveries/{item_id}/cancel` | `agents:manage` |
| `GET /agents`, `GET /agents/{id}`, `POST /agents/enroll-token`, `POST /agents/{host_id}/refresh`, `POST /agents/{id}/revoke` | `agents:manage` |
| `GET`, `POST /agents/upgrades`, `POST /agents/upgrades/{id}/cancel`, `GET /agent-releases`, `GET /agents/upgrade-policy` | `agents:manage` |
| `PUT /agents/upgrade-policy`, `POST /agents/upgrade-policy/resume` | `agentupgrades:manage` |
| `GET`, `PUT /agents/auto-enroll`, `POST /agents/auto-enroll/keys`, `PATCH`, `DELETE /agents/auto-enroll/keys/{id}`, `POST .../rotate` | `agents:manage` |
| `GET /statistics/tenant`, `GET /statistics/system` | `stats:read` |
| `POST /backup/export`, `POST /backup/import` | `backup:manage` |
| `GET /stream` (server-sent events) | `inventory:read` |

`POST /agents/enroll-token` takes an optional `label` and `ttl_seconds`
(default 24 hours) and returns the token once. `PATCH
/agents/auto-enroll/keys/{id}` can also turn a key off with
`{"enabled": false}`.

`POST /backup/export` exports the tenant's hosts, their latest snapshots
(all snapshots with `{"include_history": true}`), tags and change history;
agent credentials and key secrets are never included. `POST /backup/import`
takes `{"mode": "skip" | "overwrite", "backup": {...}}` and keeps IDs.

Refusals answer `{"reason": "...", "detail": {...}}`, for example `403
forbidden`, `404 not_found`, `409 conflict` or `422 validation_failed`.

## Limits

| Limit | Value |
|---|---|
| enrollment token lifetime | 24 hours unless `ttl_seconds` says otherwise |
| enrollment key | 1–32 networks, `/0` refused; limit 0 (unlimited) to 1 000 000 |
| key enrollments | at most 60 per minute per server instance |
| snapshot size | 8 MiB (`limits_inventory.max_snapshot_bytes`) |
| snapshot retention | 90 days (`retention.days`) |
| agent report interval | 60 s to 7 days, default 1 hour (`interval_seconds` in `agent.yaml`) |
| upgrade confirmation | 60–1800 s, default 300 (`upgrade.confirm_timeout_seconds`) |
| backup import | 256 MiB |
