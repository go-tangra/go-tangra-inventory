# Phase 1 Contracts: Inventory Service

Three surfaces: (A) the **browser/query API** proxied by the gateway under
`/api/inventory` (OpenAPI, `x-freya-permission`); (B) **module-to-module gRPC**
(`inventory.v1`, SPIFFE mTLS, not gateway-proxied); (C) the **off-mesh ingest
edge** used by agents (separate listener, per-agent credential auth). Plus the
event contract and the agent-collector interface.

## A. Browser/query HTTP API — prefix `/api/inventory/v1` (gateway-proxied)

Hosts
- `GET  /hosts` — list; filters hostname, os, manufacturer, status, tag, last_seen_from/to, agent_online, cursor/limit. perm `inventory:read`.
- `GET  /hosts/{id}` — host + latest snapshot summary. perm `inventory:read`.
- `GET  /hosts/{id}/latest` — latest full snapshot. perm `inventory:read`.
- `POST /hosts/{id}/tags` — set/replace tags. perm `hosts:manage`.
- `POST /hosts/{id}/retire` — mark retired. perm `hosts:manage`.
- `DELETE /hosts/{id}` — delete host + its snapshots. perm `hosts:manage`.

Snapshots
- `GET  /hosts/{id}/snapshots` — history (newest-first, paginated). perm `snapshots:read`.
- `GET  /snapshots/{id}` — one snapshot (full). perm `snapshots:read`.
- `GET  /snapshots/{id}/diff/{other}` — diff two snapshots → added/removed/modified. perm `snapshots:read`.
- `DELETE /snapshots/{id}` — delete a snapshot. perm `snapshots:manage`.
- `GET  /hosts/{id}/changes` — change history for a host. perm `snapshots:read`.

Agents & enrollment (operator)
- `GET  /agents` — connected agents (from the shared registry). perm `agents:manage`.
- `POST /agents/enroll-token` — mint a tenant-scoped, expiring enrollment token; returns the secret ONCE. perm `agents:manage`.
- `POST /agents/{host_id}/refresh` — trigger an on-demand refresh; returns delivered=bool, command_id. perm `agents:manage`.
- `POST /agents/{id}/revoke` — revoke a per-agent credential. perm `agents:manage`.

Statistics & backup
- `GET  /statistics/tenant` — per-tenant rollups. perm `stats:read`.
- `GET  /statistics/system` — per-tenant breakdown (admin). perm `stats:read` + admin.
- `POST /backup/export` — export (include_history flag). perm `backup:manage`.
- `POST /backup/import` — import (mode skip|overwrite). perm `backup:manage`.

Realtime
- `GET  /stream` — SSE relay of agent-online/offline + snapshot-received (x-freya-permission `inventory:read`).

Redaction: no response includes agent/enrollment credentials. Mutating routes
require the platform CSRF header; the OpenAPI declares body-size limits.

## B. Module-to-module gRPC — `inventory.v1` (SPIFFE mTLS, not gateway-proxied)

- `InventoryHostService`: ListHosts, GetHost, GetHostByIdentity, TagHost, RetireHost, DeleteHost
- `InventorySnapshotService`: GetSnapshot, ListSnapshots, GetLatestByHost, DiffSnapshots, ListChanges, DeleteSnapshot
- `InventoryStatisticsService`: GetStatistics (tenant; system for admin)
- `InventoryAgentService`: ListConnectedAgents, RefreshInventory, MintEnrollmentToken, RevokeAgent

Every request carries `tenant_id`; the caller identity comes from the mTLS peer
(authn.FromContext). Messages never carry credentials; snapshot messages exclude
sealed fields. Errors map: not-found→NotFound, forbidden→PermissionDenied,
validation→InvalidArgument, conflict/precondition→FailedPrecondition.

## C. Off-mesh ingest edge — `inventory.ingest.v1` (separate listener, per-agent credential)

Served on a dedicated listener, network-isolated from A/B, server-auth TLS.
- `Enroll(EnrollRequest{enrollment_token, host_identity{hardware_uuid, machine_id, hostname}, agent_version}) → EnrollResponse{agent_id, agent_credential /*once*/}`
  — consumes the token (single-use), issues + seals the per-agent credential, creates the agent row.
- `SubmitInventory(SubmitRequest{Inventory}) → SubmitResponse{snapshot_id, host_id, received_at}`
  — auth = per-agent credential (metadata); resolves host by identity within the agent's tenant; stores snapshot + child rows + change record atomically; bounded size.
- `StreamCommands(StreamRequest{agent_id, agent_version}) → stream Command{command_id, type}`
  — long-lived; registers agent→instance in the shared registry; delivers refresh commands.

Auth interceptor: reject missing/invalid/revoked credential (Unauthenticated);
bind tenant+host scope from the agent row (no cross-tenant writes, SR-003);
enforce max message size (SR-005).

`Inventory` message (mirrors data-model payload): host_identity, collected_at,
agent_version, os{...}, bios, system, baseboard, chassis, processors[], cache[],
memory{array, modules[]}, ports[], slots[], oem_strings[], bios_language,
monitors[], installed_programs[], services[], users[], patches[], environment{},
network_interfaces[], disks[]{partitions[]}.

## D. Event contract (platform bus `platform:events:<tenant>`)

- `inventory.snapshot.received` {host_id, snapshot_id, hostname, collected_at}
- `inventory.agent.online` / `inventory.agent.offline` {agent_id, host_id, hostname}
- `inventory.host.changed` {host_id, change_count}
Consumed by the gateway SSE hub → the module's `/stream` → the UI live store.
Payloads carry no credentials or serial numbers.

## E. Agent-collector interface (endpoint, internal)

`Collector.Collect(ctx) (Inventory, error)` composed of platform collectors:
- hardware (go-smbios): bios/system/baseboard/chassis/cpu/cache/memory/ports/slots/oem/bios_language
- monitors (Windows EDID via WMI; Linux /sys where available; else empty)
- os/software/services/users/patches/environment (gopsutil + platform sources)
- network interfaces + disks/partitions (gopsutil)
Each sub-collector returns empty (not error) when a category is unavailable on the
platform. `Sender.Submit(inv)` and `Sender.Enroll(token)` speak surface C over TLS
with retry/backoff; `Daemon` holds the reconnecting `StreamCommands` and the
periodic submit loop.
