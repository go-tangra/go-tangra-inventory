# Phase 0 Research: Inventory Service

Decisions resolving the Technical Context, each with rationale and alternatives.
All NEEDS CLARIFICATION items were resolved during specification (scope confirmed
with the requester: full server module + off-mesh enrolled agent; breadth =
hardware + software/OS + network).

## D1. Off-mesh agent authentication (enrollment + per-agent credential)

**Decision**: Agents are untrusted until enrolled. An operator mints a
tenant-scoped, single-use, expiring **enrollment token**. On first run the agent
POSTs the token to the ingest edge's enroll endpoint and receives a persistent
**per-agent credential** (an opaque bearer secret; a client certificate issued via
lcm is a supported upgrade). The credential is stored sealed on the server (KEK
envelope) and presented on every subsequent submit/stream over server-auth TLS.

**Rationale**: A random Windows/Linux endpoint cannot hold a mesh SPIFFE SVID, so
the mesh's mTLS cannot gate ingest. A per-agent credential bound to a tenant at
enrollment replaces tangra's static shared secret (`x-client-secret`) and gives
per-agent revocation, tenant binding, and no "empty = no auth" fallback.

**Alternatives**: (a) mesh SVID for agents — rejected, endpoints aren't mesh
members. (b) static shared secret (tangra) — rejected, no tenant binding, no
revocation, cross-tenant risk. (c) mTLS client cert per agent via lcm — kept as an
optional hardening path; the bearer-credential path is the baseline.

## D2. Host identity resolution

**Decision**: Resolve a submission to a host by **hardware_uuid → machine_id →
hostname**, within the agent's tenant. Create on first sighting; update otherwise.
Record which key matched. Tangra keyed on hostname only.

**Rationale**: Hostnames are reused/duplicated and change; the SMBIOS system UUID
is the most stable hardware identity, with a stable OS machine id as fallback and
hostname as last resort. Prevents duplicate hosts and merges re-imaged machines.

**Alternatives**: hostname-only (tangra) — rejected, weak/ambiguous. MAC-based —
rejected, multi-NIC and virtualization make it unstable.

## D3. Storage model (time-series snapshots + normalized components)

**Decision**: TimescaleDB with per-tenant RLS. `inventory_hosts` (RLS table),
`inventory_snapshots` (**hypertable** on collected_at, keyed by host), with the
full payload in a JSONB column AND the most-queried fields lifted to typed columns.
Normalized child tables for queryable components (processors, memory modules,
disks, network interfaces, installed software, services, monitors) reference the
snapshot. `inventory_changes` records diffs between consecutive snapshots.
Retention = a scheduled purge of snapshots older than the configured window
(Timescale retention-friendly), never deleting a host's latest snapshot.

**Rationale**: Snapshots are naturally time-series; a hypertable gives efficient
time-range queries and retention. Lifting queryable fields + normalized child
tables makes hardware/software searchable, unlike tangra's opaque blob, while the
JSONB payload preserves full fidelity for detail views and diffing.

**Alternatives**: single denormalized JSON row (tangra) — rejected, unqueryable,
no multi-tenancy. Fully normalized (no JSONB) — rejected, lossy for rarely-queried
fields and heavier to evolve.

## D4. Change detection & diff

**Decision**: On ingest, compare the new snapshot to the host's previous snapshot
per component category using stable component keys (e.g. memory module by
device_locator+serial, disk by serial, program by name+version) and record
added/removed/modified into `inventory_changes`. A `diff` API recomputes between
any two snapshots on demand. The diff engine is pure and fuzz-tested.

**Rationale**: Change history is the primary value over point-in-time inventory
and must be deterministic and side-effect free for testability.

## D5. Cross-instance refresh (shared agent registry)

**Decision**: Each server instance that holds an agent's `StreamCommands` stream
registers `agent → instance` in Valkey with a TTL heartbeat and subscribes to a
per-agent Valkey pub/sub channel. `RefreshInventory` publishes a command to that
channel; the owning instance delivers it down the live stream. agent-online/offline
+ snapshot-received events publish to `platform:events:<tenant>`.

**Rationale**: Tangra's in-memory registry is process-local and breaks under
horizontal scaling / gateway routing. A Valkey-backed registry + pub/sub makes
refresh work regardless of which instance holds the stream, and drives live UI.

**Alternatives**: sticky routing to the owning instance — rejected, brittle behind
the gateway. Poll-based refresh — rejected for latency; kept as a degraded
fallback when no stream is connected.

## D6. Cross-platform collection libraries

**Decision**: Hardware via `github.com/siderolabs/go-smbios` (as tangra). Software/
OS, network interfaces and disks via `github.com/shirou/gopsutil/v4` (cross-platform,
CGO-free). Windows extras retained: monitor EDID via WMI/PowerShell, current user
via token APIs; Linux equivalents where available (e.g. /sys EDID, /etc/os-release,
package managers) with graceful empties otherwise.

**Rationale**: go-smbios is proven in tangra; gopsutil is the de-facto cross-platform
inventory library (CGO-free, wide OS coverage) and covers the software/OS/network/
disk expansion without shelling out. Missing platform data yields empty sections,
not errors (SC/edge cases).

**Alternatives**: shelling out to wmic/dmidecode/lshw — rejected, fragile,
availability-dependent. CGO libraries — rejected, complicate cross-compilation.

## D7. Ingest edge isolation & payload safety

**Decision**: The ingest edge is a distinct listener (server-auth TLS + per-agent
credential auth interceptor), separate from the mesh gRPC/HTTP query API, with a
strict max snapshot size and rate limiting. Submissions are validated and written
atomically (host upsert + snapshot insert + child rows + change record in one tx);
malformed/oversized payloads are rejected with no partial write.

**Rationale**: Untrusted off-mesh input must not share the trusted mesh surface;
bounding size + atomic writes prevent DoS and partial-state corruption (SR-005).

## D8. Agent packaging & lifecycle

**Decision**: One cross-platform agent binary with modes one-shot / daemon /
service; installable as a Windows service (retain tangra's winsvc + MSI) and a
Linux systemd unit. Daemon mode enrolls once, submits at startup and on a
configurable interval (tangra had none) with retry/backoff, and holds a
reconnecting command stream. Least-privilege guidance documented (SMBIOS read
needs elevation on most OSes).

**Rationale**: Matches operational reality of fleet agents and the source's
packaging, adds the missing periodic interval, and keeps the endpoint footprint
minimal.

## Supply-chain note (Constitution VI)

New third-party dependencies beyond the Freya baseline: `siderolabs/go-smbios`
(hardware DMI, already used by tangra) and `shirou/gopsutil/v4` (cross-platform
software/network/disk). Both are widely used, CGO-free, and pinned in `go.sum`;
`govulncheck` runs in CI. No other new runtime deps for the server.

## STRIDE summary

- **Spoofing**: a rogue endpoint posing as an agent → enrollment tokens
  (single-use, expiring, tenant-scoped) + per-agent sealed credentials bound to a
  tenant; no shared secret; optional mTLS client cert (D1). Cross-tenant host
  spoofing prevented by tenant-scoped identity resolution (D2, SR-003).
- **Tampering**: forged/oversized snapshots → bounded, validated, atomic ingest;
  immutable snapshots; append-only audit (D7, SR-005/006).
- **Repudiation**: append-only tamper-evident audit of ingest/enroll/refresh/
  delete/admin with actor+tenant+outcome (SR-006).
- **Information disclosure**: serials/user names/credentials → per-tenant RLS,
  sealed credentials never returned, redaction in logs/audit/backups; ingest edge
  isolated; registry/event bus tenant-partitioned (SR-002/004).
- **Denial of service**: snapshot floods/oversize → ingest rate limits + size caps
  + atomic writes; retention purge bounds growth (D3, D7).
- **Elevation of privilege**: enrollment-token minting and destructive ops gated by
  operator/admin permissions; agent bound to its tenant/host scope; system worker
  paths use a scoped subject, never a bypass (SR-003, Principle III).
