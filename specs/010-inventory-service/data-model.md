# Phase 1 Data Model: Inventory Service

All tables carry `tenant_id uuid NOT NULL` and are protected by per-tenant
row-level security (RLS), like every other Freya module. Timestamps are
`timestamptz`. IDs are application-generated (uuid v7-style) except where a natural
key is noted. Trusted worker paths run under a scoped system subject (nil-uuid
tenant pin for cross-tenant maintenance queries), never an unauthenticated bypass.

## Entities

### inventory_hosts  (RLS table)
The managed endpoint (one row per host per tenant).
- `id` (PK), `tenant_id`
- `hostname`, `machine_id`, `hardware_uuid`, `system_serial`
- `identity_key` (which key resolved identity: hardware_uuid | machine_id | hostname)
- `manufacturer`, `model`, `os_name`, `os_version`, `os_arch`
- `agent_version`, `assigned_user`
- `status` (active | stale | retired)
- `tags` (jsonb string→string)
- `first_seen`, `last_seen`, `last_snapshot_id`
- `created_at`, `updated_at`
- Uniqueness per tenant: `(tenant_id, hardware_uuid)` where non-empty; else
  `(tenant_id, machine_id)`; else `(tenant_id, hostname)` — enforced via partial
  unique indexes. Indexes: hostname, os_name, manufacturer, status, last_seen, tags (GIN).

### inventory_snapshots  (hypertable on collected_at)
An immutable inventory report bound to a host.
- `id` (PK), `tenant_id`, `host_id` (FK → inventory_hosts)
- `collected_at` (hypertable partition column), `received_at`
- `agent_version`, `source` (agent | manual | import)
- `os_name`, `os_version`, `manufacturer`, `model` (lifted for fast summary)
- `payload` (jsonb — full hardware+software+network inventory, source of truth for detail/diff)
- Indexes: `(tenant_id, host_id, collected_at DESC)`; retention purge by collected_at.

### Normalized component child tables (per snapshot; RLS; queryable)
Each references `snapshot_id` + `host_id` + `tenant_id`. Populated on ingest from
the payload for querying/statistics; the payload remains authoritative.
- `inventory_processors` (socket_designation, manufacturer, version, max_speed_mhz, current_speed_mhz, core_count, core_enabled, thread_count, part_number, serial_number, socket_populated)
- `inventory_memory_modules` (device_locator, bank_locator, capacity_bytes, form_factor, memory_type, speed_mt_s, configured_speed_mt_s, manufacturer, serial_number, part_number)
- `inventory_disks` (model, serial, size_bytes, media_type, interface; partitions in payload)
- `inventory_network_interfaces` (name, mac, ip_addresses[], subnet, gateway, dns[], dhcp, speed_bps, type, up)
- `inventory_software` (name, version, publisher, install_date, install_location, size_bytes)
- `inventory_services` (name, display_name, state, start_mode, account)
- `inventory_monitors` (manufacturer, model, serial_number)
Indexes on the high-cardinality query fields (serial, name+version, mac).

### inventory_changes  (RLS table)
Per-snapshot change record vs the previous snapshot of the same host.
- `id` (PK), `tenant_id`, `host_id`, `snapshot_id`, `prev_snapshot_id`, `detected_at`
- `category` (bios|system|processor|memory|disk|network|software|service|monitor|os|…)
- `change_type` (added | removed | modified)
- `component_key` (stable key within the category), `before` (jsonb), `after` (jsonb)
- Index: `(tenant_id, host_id, detected_at DESC)`, `(tenant_id, snapshot_id)`.

### inventory_agents  (RLS table)
The enrolled endpoint client (one per host).
- `id` (PK == agent credential id), `tenant_id`, `host_id` (nullable until first snapshot)
- `credential_sealed` (bytea — sealed per-agent secret; NEVER returned)
- `enrolled_at`, `last_seen`, `agent_version`, `revoked` (bool)
- `identity_hint` (hostname/machine at enroll time)
- Live connection status is NOT stored here — it lives in the Valkey registry.

### inventory_enrollment_tokens  (RLS table)
Operator-minted, single-use, expiring enrollment token.
- `id` (PK), `tenant_id`, `token_hash` (hash of the secret; secret returned once at mint, never stored plaintext)
- `expires_at`, `used_at` (null until consumed), `created_by`, `created_at`, `label`
- A token is valid iff `used_at IS NULL AND now() < expires_at AND NOT revoked`.

### inventory_audit_events  (append-only, hypertable)
- `id`, `tenant_id`, `at`, `actor_kind` (agent|user|service|system), `actor_id`,
  `action` (see vocabulary), `subject_kind` (host|snapshot|agent|token|backup|system),
  `subject_id`, `outcome` (ok|refused|error), `reason`, `detail` (jsonb, redacted).

## Enums / vocabularies

- **HostStatus**: active, stale, retired.
- **SnapshotSource**: agent, manual, import.
- **ChangeType**: added, removed, modified.
- **ChangeCategory**: bios, system, baseboard, chassis, processor, cache, memory,
  port, slot, monitor, disk, network, os, software, service, user, patch.
- **CommandType**: refresh (extensible).
- **AgentConnState** (registry only): online, offline.
- **AuditAction**: agent_enrolled, token_minted, token_revoked, snapshot_ingested,
  snapshot_deleted, host_updated, host_retired, host_deleted, host_tagged,
  refresh_requested, refresh_delivered, backup_exported, backup_imported,
  access_refused.
- **Permissions (API)**: inventory:read, inventory:write, hosts:manage,
  agents:manage, snapshots:read, snapshots:manage, stats:read, backup:manage.

## Relationships

- A **host** has many **snapshots** (time series); `last_snapshot_id` points at the newest.
- A **snapshot** has many component rows (processors, memory modules, disks, NICs,
  software, services, monitors) and produces zero-or-more **change** rows vs its predecessor.
- A **host** has one **agent**; an **agent** is created from a consumed **enrollment token**.
- Live agent connection state is external (Valkey registry), keyed by agent id → instance + channel.

## Redaction / sealing

- `inventory_agents.credential_sealed` and enrollment-token secrets are sealed
  (KEK envelope) and never appear in any read, list, export or log.
- Snapshot payloads may contain serial numbers and user names — returned only to
  callers with inventory read permission; excluded from audit `detail`; backups
  carry inventory data but never agent/enrollment secrets.
