# Contract: audit vocabulary and realtime events (023)

## Inventory (`internal/audit/audit.go`, table `inventory_audit_events`)

Upgrade audit rows are written **in the same transaction** as the state
change through the repo (`AppendAudit` on the tx), not through the
buffered writer, so none can be dropped (FR-016). Common `detail` keys:
`agent_id`, `host_id`, `from_version`, `to_version`, `request_id`,
`origin` (`user|policy|agent`). Reason codes are the closed set from
[inventory-grpc.md](inventory-grpc.md).

| action (NEW) | actor_kind / actor_id | subject_kind / subject_id | outcome | extra detail |
|---|---|---|---|---|
| `agent_upgrade_requested` | user / user id; system / `upgrade-policy`; agent / agent id (CLI `update`) | agent / agent id | ok | `allow_downgrade` |
| `agent_upgrade_cancelled` | user / user id | agent / agent id | ok | — |
| `agent_upgrade_delivered` | system / `inventory` | agent / agent id | ok | `instance` |
| `agent_upgrade_started` | agent / agent id | agent / agent id | ok | `state: downloading` |
| `agent_upgrade_installing` | agent / agent id | agent / agent id | ok | — |
| `agent_upgrade_succeeded` | agent / agent id | agent / agent id | ok | `duration_seconds` |
| `agent_upgrade_failed` | agent / agent id, or system (timeout) | agent / agent id | error | `reason` |
| `agent_upgrade_rolled_back` | agent / agent id | agent / agent id | error | `reason` |
| `agent_upgrade_expired` | system / `inventory` | agent / agent id | error | `reason: expired` |
| `agent_upgrade_refused` | agent / agent id | agent / agent id | refused | download/report for a foreign or inactive request (`reason`) |
| `upgrade_policy_updated` | user / user id | upgrade_policy (NEW subject kind) / tenant id | ok | `changes` {field: {before, after}} |
| `upgrade_policy_paused` | system / `upgrade-policy` | upgrade_policy / tenant id | error | `request_id`, `reason` |
| `upgrade_policy_resumed` | user / user id | upgrade_policy / tenant id | ok | — |
| `agent_release_imported` | system / `inventorysvc` | release (NEW subject kind) / version | ok / refused | `source: bundled|import`, `key_id`, `manifest_sha256`; tenant_id = all-zero uuid (platform scope; `audit.Validate` accepts it only for this action) |

Subject kinds added: `upgrade_policy`, `release`. `audit.Known` lists the
new types; `audit_test.go` asserts the vocabulary.

Detail values never contain manifests, signatures, credentials or
artifact bytes.

## Inventory realtime events (`platform:events:<tenant>`, content-free)

- `inventory.agent.upgrade` `{agent_id, upgrade_id, state}` after every
  committed transition (agent list refresh).
- `inventory.agent.upgrade_policy` `{paused}` on pause/resume/update.

## IPAM (`go-tangra-ipam-v4/internal/audit/audit.go`, written in the apply transaction)

Common columns as in 020: `actor_kind = system`, `actor_id = hostsync`;
detail keys `inventory_host_id`, `run_id`, `trigger`.

| action (NEW) | subject_kind / subject_id | extra detail |
|---|---|---|
| `hardware_reported` | device / device id | `summary` (cpu_model, cores, memory_total_bytes, memory_type, disk_count, disk_total_bytes) |
| `hardware_updated` | device / device id | `changes`: list of `{field, before, after}` — e.g. `bios.version`, `bios.release_date`, `system.serial`, `memory.total_bytes`, `memory.slot[DIMM A1]` (`added|removed|changed`), `disk[S64…]` (`added|removed|changed`), `processor[CPU1]`; ≤ 100 entries, `changes_truncated: n` |

Field keys must avoid the guarded substrings (`owner`, `contact`,
`secret`, `credential`, `snmp`, `ipmi`, `password`); `audit_test.go`
asserts the keys used by the hardware planner survive the guard.

IPAM publishes `ipam.hostsync.applied {device_id, changes}` as today (the
hardware change counts in `changes`); no hardware data in events.
