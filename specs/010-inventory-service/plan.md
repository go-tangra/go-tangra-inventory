# Implementation Plan: Inventory Service

**Branch**: `010-inventory-service` | **Date**: 2026-09-21 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/010-inventory-service/spec.md`

## Summary

A tenant-scoped IT **asset inventory** platform module, replicating and extending
`go-tangra-inventory`. Endpoint **agents** collect hardware (SMBIOS/DMI),
software/OS and network/storage inventory and report immutable **snapshots** to
the central **inventory** server. The server resolves each snapshot to a stable
**host** identity, stores the full time series, computes per-snapshot **change
history**, and serves query/diff/statistics/backup over the platform gateway and
service-to-service gRPC, with a Module-Federation UI.

Two trust planes, like the source but Freya-native: the **query/admin API** is an
ordinary mesh module (SPIFFE mTLS + gateway platform token, gateway-registered),
while a separate **ingest edge** authenticates untrusted off-mesh agents that
enroll with a tenant-scoped token and thereafter present a sealed per-agent
credential. Agent connection state and push-refresh use a **shared registry**
(Valkey) so refresh works across horizontally-scaled instances.

## Technical Context

**Language/Version**: Go 1.26 (matches every other Freya service). The endpoint
agent is a cross-platform Go binary (Windows + Linux).

**Primary Dependencies**: the Freya framework (`github.com/go-freya/freya`) for
transport (SPIFFE mTLS gRPC + OpenAPI-validated HTTP edge), identity, audit,
sealed envelopes, and gateway registration; `github.com/siderolabs/go-smbios`
for SMBIOS/DMI hardware collection (from tangra); `github.com/shirou/gopsutil/v4`
for cross-platform software/OS, network and disk collection; `pgx` +
TimescaleDB for storage; Valkey for the shared agent registry and event bus.
New third-party deps (go-smbios, gopsutil) are justified in research.md
(Constitution VI). UI: Vue 3 + Vite + Vuetify (Materio) Module-Federation remote,
mirroring services/paperless/ui and services/deployer/ui.

**Storage**: TimescaleDB (Postgres) with per-tenant row-level security. Hosts and
change history are ordinary RLS tables; **snapshots are a hypertable** keyed by
(host, collected_at). The most-queried fields are lifted into typed, indexed
columns; the full inventory payload is retained as JSONB; and normalized child
tables (processors, memory modules, disks, network interfaces, installed
software, services, monitors) make hardware/software queryable — unlike tangra's
single opaque JSON blob. Retention is a configurable purge of old snapshots.

**Enrollment & ingest**: agents are off-mesh and untrusted until enrolled. An
operator mints a tenant-scoped, single-use, expiring **enrollment token** (auth's
token issuer / a sealed inventory-side token). The agent exchanges it at the
**ingest edge** for a persistent per-agent credential (sealed at rest). The
ingest edge is a separate listener (server-auth TLS + per-agent credential),
network-isolated from the mesh query API; it binds every submission to the
agent's tenant + host scope.

**Realtime**: agent command streams and refresh delivery use a Valkey-backed
shared registry (agent → owning instance mapping + a per-agent command channel via
Valkey pub/sub) so a refresh handled by any instance reaches the agent connected
to any other. snapshot-received / agent-online / agent-offline events publish to
`platform:events:<tenant>` for the gateway SSE hub.

**Testing**: Go `testing` with a `testrt` test runtime + a `memstore` fake repo;
contract tests over the OpenAPI + proto; unit tests per package; an integration
suite (testcontainers: TimescaleDB, Valkey) behind `//go:build integration`; fuzz
tests for the snapshot-ingest parser, the enrollment path and the diff engine.
Coverage gate ≥80% overall, 100% on sealed/authz/enroll.

**Target Platform**: Linux server container in `deploy/stack` behind the gateway;
the agent cross-compiles to Windows (service/MSI) and Linux (systemd).

**Project Type**: Web service (Go backend + gRPC + OpenAPI HTTP) + an off-mesh
ingest edge + a cross-platform endpoint agent + a Module-Federation UI remote.

**Performance Goals**: a submitted snapshot is stored and the host list reflects it
within ~1s; host search over 10k hosts returns in <3s; on-demand refresh reaches
a connected agent (any instance) within ~30s; sustains 5k hosts reporting on a
regular interval.

**Constraints**: agents are untrusted until enrolled; enrollment tokens are
single-use/expiring/tenant-scoped; per-agent + enrollment credentials are sealed
and never returned; the ingest listener is isolated from and size-bounds relative
to the mesh API; all data is per-tenant RLS-isolated; the shared registry/event
bus never leaks across tenants; snapshots are immutable.

**Scale/Scope**: thousands of hosts per tenant, each with periodic snapshots
retained as a time series; five prioritized user stories (enroll+ingest,
browse/inspect, history+diff, refresh+live, stats+backup).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Secure by Default**: zero-config refuses to start; the ingest edge requires
  a valid per-agent credential (no "empty = no auth" like tangra); enrollment
  tokens are single-use/expiring; sealed credentials; TLS everywhere. PASS.
- **II. Zero Trust Service Communication**: mesh calls are SPIFFE mTLS; browser
  via gateway platform token; off-mesh agents authenticated by per-agent
  credential bound to a tenant, never a shared secret. PASS.
- **III. Least Privilege & Tenant Isolation**: per-tenant RLS on every table;
  ingest binds to the agent's tenant/host scope; trusted worker paths use a
  scoped system subject, never an unauthenticated bypass; the agent runs with the
  least privilege needed to read SMBIOS. PASS.
- **IV. Test-First with Security Verification (NON-NEGOTIABLE)**: contract/unit/
  integration tests precede implementation per story; negative + fuzz tests for
  ingest, enrollment and diff; coverage + redaction tests. PASS.
- **V. Defense in Depth & Observability**: gateway edge + module authz + RLS;
  bounded ingest payloads and rate limits; append-only audit; health/readiness on
  the admin port; events for observability. PASS.
- **VI. Supply-Chain Integrity**: new deps (go-smbios, gopsutil) justified in
  research.md; `go.sum` pinned; `govulncheck` in CI. PASS.
- **VII. Simplicity & Explicitness**: explicit wiring (no reflection/global
  mutable state); the off-mesh agent complexity is inherent to the domain and
  isolated behind the ingest edge; documented in research.md. PASS.

No Constitution violations. Security Requirements from the spec (SR-001..006) map
to research.md decisions and to test tasks.

## Project Structure

### Documentation (this feature)

```
specs/010-inventory-service/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output (OpenAPI + proto + ingest/enroll + agent-collector ifaces + events)
└── tasks.md             # Phase 2 output (/speckit-tasks)
```

### Source Code (repository root)

```
services/inventory/
├── go.mod                       # module github.com/go-freya/freya/services/inventory (replaces ../.. ../auth ../gateway ../lcm)
├── cmd/
│   ├── inventorysvc/            # the SERVER (run + bootstrap/migrate)
│   └── inventory-agent/         # the AGENT (one-shot | daemon/service | install/uninstall)
├── api/
│   ├── openapi/inventory.yaml   # browser/query routes (x-freya-permission/x-freya-public)
│   └── proto/inventory/v1/      # gRPC: Host/Snapshot/Statistics/Agent services + Ingest
├── internal/
│   ├── app/                     # server wiring (freya.New, stores, ingest edge, registry, workers)
│   ├── config/                  # server + agent config
│   ├── store/ + repo/ + repodb/ # migrations (RLS + snapshot hypertable + child tables), models, SQL, repo iface
│   ├── memstore/                # in-memory repo fake for tests
│   ├── sealed/ authz/ audit/    # envelope seal, permission checks, audit vocabulary
│   ├── hosts/ snapshots/        # host + snapshot (ingest, resolve identity, change detect) services
│   ├── diff/                    # snapshot diff/change engine
│   ├── enroll/                  # enrollment-token mint/verify + per-agent credential issue (sealed)
│   ├── ingest/                  # off-mesh ingest edge (submit + StreamCommands) auth + handlers
│   ├── registry/                # Valkey-backed shared agent connection registry + refresh delivery
│   ├── stats/ backup/           # statistics + export/import
│   ├── events/ stream/          # platform event publisher + SSE relay
│   ├── httpapi/ grpcapi/        # mesh HTTP + gRPC surfaces
│   └── collector/               # AGENT: cross-platform hardware/software/network collectors
│       ├── smbios*.go           # go-smbios hardware (from tangra)
│       ├── software_*.go        # OS/programs/services/users (gopsutil + platform)
│       ├── network_*.go disk_*.go
│       └── monitor_windows.go user_windows.go   # Windows extras (EDID, token user)
├── pkg/
│   ├── inventorymanifest/       # gateway manifest (routes/permissions/abilities/nav) + SeedPermissions
│   └── inventoryclient/         # typed module-to-module gRPC client
├── ui/                          # Vue 3 + Vite + Vuetify MF remote (hosts, host detail, agents, dashboard)
├── deploy/                      # policy.yaml, kek.dev, README, systemd unit, agent packaging (MSI/winsvc)
├── Dockerfile Makefile buf.yaml buf.gen.yaml
```

**Structure Decision**: mirrors services/paperless (proven platform module layout)
plus two inventory-specific additions — `internal/ingest` + `internal/enroll` +
`internal/registry` for the off-mesh agent plane, and `cmd/inventory-agent` +
`internal/collector` for the cross-platform endpoint agent.

## Complexity Tracking

The off-mesh agent plane (ingest edge, enrollment, cross-instance refresh registry)
is additional surface beyond a pure gateway module, but it is inherent to an
agent-based inventory system and is isolated behind explicit packages
(`ingest`, `enroll`, `registry`). No Constitution violations to justify.
