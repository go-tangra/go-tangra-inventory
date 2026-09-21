# Tasks: Inventory Service

**Feature**: 010-inventory-service | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Organized by phase; user-story phases are independently testable. Tests are
MANDATORY and precede implementation (Constitution IV). `[P]` = parallelizable
(different files, no incomplete deps). Module path:
`github.com/go-freya/freya/services/inventory`. Mirror services/paperless for
platform wiring; add the off-mesh agent/ingest/registry surface.

## Phase 1: Setup (Shared Infrastructure)

- [X] T001 Create the module skeleton `services/inventory/` with the package tree from plan.md (cmd/{inventorysvc,inventory-agent}, api/{openapi,proto/inventory/v1}, internal/{app,config,store,repo,memstore,sealed,authz,audit,hosts,snapshots,diff,enroll,ingest,registry,stats,backup,events,stream,httpapi,grpcapi,collector}, pkg/{inventorymanifest,inventoryclient}, ui, deploy).
- [X] T002 Add `services/inventory/go.mod` (module github.com/go-freya/freya/services/inventory, Go 1.26) with replaces for ../.. ../auth ../gateway ../lcm; add siderolabs/go-smbios + shirou/gopsutil/v4; seed go.sum from services/paperless.
- [X] T003 [P] Add `buf.yaml` + `buf.gen.yaml` and `api/proto/inventory/v1/*.proto` stubs (inventory.v1 Host/Snapshot/Statistics/Agent + inventory.ingest.v1 Enroll/Submit/StreamCommands); wire proto codegen into the Makefile (mirror services/paperless).
- [X] T004 [P] Add `services/inventory/Dockerfile` (build UI, embed with -tags ui, build inventorysvc) and `Makefile` (test/cover/vuln/generate/build/image + agent cross-build targets) mirroring services/paperless.
- [X] T005 [P] Scaffold `services/inventory/ui/` (Vue 3 + Vite + Vuetify Module-Federation remote named `inventory`) from services/paperless/ui (package.json, vite.config base /m/inventory/, main.ts, api/client BASE /api/inventory/v1, remote/{routes,nav}, embed.go/embed_stub.go).
- [X] T006 [P] Scaffold `cmd/inventory-agent/` (cross-platform agent CLI: modes one-shot | daemon | service install/uninstall) — flags/entrypoint only, collectors added in later phases.

## Phase 2: Foundational (Blocking Prerequisites)

- [X] T007 Typed, validated config in `internal/config/config.go` — server (server/admin addrs, ingest_addr, db, valkey, kek, registry, retention_days, stale_after, jobs, events, gateway) and agent (ingest endpoint, interval, token/credential paths) sections + `config_test.go` asserting secure defaults and refusal to start without kek/db/ingest identity.
- [X] T008 Store migrations in `internal/store/migrations/`: `0001_schema.sql` (inventory_hosts w/ partial-unique identity indexes + tags jsonb; inventory_agents w/ credential_sealed; inventory_enrollment_tokens; inventory_changes), `0002_hypertables.sql` (inventory_snapshots + inventory_audit_events hypertables), `0003_components.sql` (normalized child tables: processors, memory_modules, disks, network_interfaces, software, services, monitors + indexes), `0004_rls.sql` (per-tenant RLS on every table + inventory_app grants).
- [X] T009 Store models + repos in `internal/store/{models.go,repos.go}` and the interface in `internal/repo/repo.go` (host upsert-by-identity, snapshot insert + child rows in one tx, changes, agents, enrollment tokens, list/filter, stats aggregations, purge, tenant ids).
- [X] T010 [P] In-memory `internal/memstore/memstore.go` implementing `repo.Store` (filters, identity resolution, change records, agents/tokens, error injection) for tests.
- [X] T011 [P] `internal/sealed/` envelope-seal/open + redaction helpers (reuse the platform sealed pattern) + `sealed_test.go` (100% — round-trip, AD binding, never leaks).
- [X] T012 [P] `internal/authz/` API-permission checks + Subjects/IsAdmin (recognize platform "owner"+"admin" as tenant super-users) + tenant scoping + `authz_test.go` (100%).
- [X] T013 [P] `internal/audit/` writer adapter to the framework append-only audit (action vocabulary per data-model) + redaction of credential/secret/serial fields.
- [X] T014 `internal/enroll/` — mint (tenant-scoped, single-use, expiring) enrollment tokens (store token_hash, return secret once), verify+consume, and issue+seal per-agent credentials + revoke + `enroll_test.go` (100% — token reuse/expiry refused, credential sealed).
- [X] T015 `internal/registry/` — Valkey-backed shared agent connection registry (agent→instance TTL heartbeat + per-agent pub/sub command channel; online/offline; ListConnected) with an in-memory fake + `registry_test.go`.
- [X] T016 `internal/events/` — publisher for inventory.snapshot.received / agent.online / agent.offline / host.changed to platform:events:<tenant>; wire the Valkey client; `internal/stream/` SSE relay (reuse paperless/lcm pattern).
- [X] T017 App build/wire/run in `internal/app/app.go` — freya.New, identity/enroll for the mesh module, store/KEK, authz, registry, events, gateway registration via pkg/inventorymanifest, mesh HTTP mux (OpenAPI-validated) + gRPC servers, the SEPARATE ingest-edge listener, worker pool (stale-marker, retention purge), admin health/readiness; refuses to start insecure.
- [X] T018 `cmd/inventorysvc/main.go` + `bootstrap` subcommand (config load, migrate, run) mirroring paperlesssvc.
- [X] T019 [P] `pkg/inventorymanifest/manifest.go` — derive gateway routes/permissions from api/openapi/inventory.yaml + Grants/Abilities/Nav (Hosts, Agents, Dashboard) + `SeedPermissions` granting inventory:read/write, hosts:manage, agents:manage, snapshots:read/manage, stats:read, backup:manage to built-in roles.
- [X] T020 `api/openapi/inventory.yaml` skeleton (info, components, CSRF/id/cursor params, error shapes, body limits) + `embed.go`; contract test `tests/contract/openapi_test.go` (parses, every mounted route declared).
- [X] T021 `internal/repo/repodb/` implementing the store over TimescaleDB + integration test (testcontainers) for schema/RLS/hypertable/identity-uniqueness/child-tables (`//go:build integration`).

## Phase 3: User Story 1 — Enroll an endpoint and ingest its first inventory (Priority: P1) 🎯 MVP

### Tests (write first, must fail)
- [X] T022 [P] [US1] Contract test `tests/contract/ingest_test.go` — Enroll (valid token → credential; reused/expired token refused), Submit (valid credential → snapshot+host; missing/invalid credential rejected), cross-tenant hostname isolation.
- [X] T023 [P] [US1] Unit test `internal/enroll/enroll_test.go` — token single-use/expiry/tenant-scope; credential sealed + never returned after first issue.
- [X] T024 [P] [US1] Unit test `internal/snapshots/ingest_test.go` — identity resolution (hardware_uuid→machine_id→hostname), host upsert, atomic snapshot+child insert, host summary/last_seen update; oversized/malformed rejected with no partial write.
- [X] T025 [P] [US1] Security test — agent bound to its tenant/host scope (cannot write another tenant/host); credentials/serials never in responses/logs/audit (SR-002/003/005).

### Implementation
- [X] T026 [US1] `internal/hosts/hosts.go` — Host service: resolve/upsert by identity, Get, List (filters + pagination), Tag, Retire, Delete.
- [X] T027 [US1] `internal/snapshots/snapshots.go` + `ingest.go` — Submit (resolve host, store snapshot + normalized child rows + summary update, enqueue change detection, publish event), Get, GetLatestByHost, ListForHost.
- [X] T028 [US1] `internal/ingest/` — off-mesh ingest edge: per-agent-credential auth interceptor (bind tenant/host scope, size cap), Enroll + SubmitInventory handlers; wire the separate listener in app.
- [X] T029 [US1] Agent collect+submit path: `internal/collector/` hardware collectors (go-smbios: bios/system/baseboard/chassis/cpu/cache/memory/ports/slots/oem/bios_language) + `internal/sender` (Enroll + Submit over TLS with retry/backoff) + `cmd/inventory-agent` one-shot mode.
- [X] T030 [US1] Mesh read for MVP: `internal/httpapi/hosts.go` (GET /hosts, GET /hosts/{id}, GET /hosts/{id}/latest) + register routes + OpenAPI entries.
- [X] T031 [P] [US1] gRPC `internal/grpcapi/` InventoryHostService (List/Get/GetByIdentity) + register.

**Checkpoint**: US1 independently demoable — enroll an agent, submit a snapshot, see the host + its latest inventory (MVP).

## Phase 4: User Story 2 — Browse hosts and inspect full inventory (Priority: P1)

### Tests (write first, must fail)
- [X] T032 [P] [US2] Contract test `tests/contract/hosts_query_test.go` — list filters (os/manufacturer/status/tag/last-seen), host detail shape, tenant isolation, permission gating.
- [X] T033 [P] [US2] Unit test `internal/snapshots/detail_test.go` — full snapshot projection (hardware/software/network) and content redaction rules.
- [X] T034 [P] [US2] Unit test `internal/collector/software_test.go` + `network_disk_test.go` — cross-platform software/OS/network/disk collectors populate expected fields; unavailable categories yield empty, not error.

### Implementation
- [X] T035 [US2] Agent expansion: `internal/collector` software/OS collectors (gopsutil: os, installed_programs, services, users, patches, environment) + network_interfaces + disks/partitions; Windows monitor EDID + token user.
- [X] T036 [US2] `internal/snapshots` full-detail projections + normalized-component queries; `internal/httpapi/snapshots.go` (GET /snapshots/{id}, GET /hosts/{id}/snapshots) + OpenAPI.
- [X] T037 [US2] gRPC InventorySnapshotService (GetSnapshot/ListSnapshots/GetLatestByHost) + register.
- [X] T038 [P] [US2] UI: `ui/src/views/hosts/` list (filters) + host detail with Hardware/Software/Network tabs + `stores/hosts.ts` + api client.

## Phase 5: User Story 3 — Track changes over time (history & diff) (Priority: P2)

### Tests (write first, must fail)
- [X] T039 [P] [US3] Unit test `internal/diff/diff_test.go` — added/removed/modified per category with stable component keys; deterministic ordering.
- [X] T040 [P] [US3] Fuzz test `internal/diff/diff_fuzz_test.go` — arbitrary snapshot pairs never panic; empty/huge components handled.
- [X] T041 [P] [US3] Contract test `tests/contract/changes_test.go` — diff two snapshots + list host changes shapes.

### Implementation
- [X] T042 [US3] `internal/diff/diff.go` — pure snapshot diff engine (per-category component keys → added/removed/modified).
- [X] T043 [US3] Wire change detection into ingest (record inventory_changes vs previous snapshot); `internal/httpapi` GET /snapshots/{id}/diff/{other} + GET /hosts/{id}/changes + OpenAPI; gRPC DiffSnapshots/ListChanges.
- [X] T044 [P] [US3] UI: host detail Snapshot-History tab + diff view (added/removed/changed) in `ui/src/views/hosts/`.

## Phase 6: User Story 4 — On-demand refresh and live agent status (Priority: P2)

### Tests (write first, must fail)
- [X] T045 [P] [US4] Unit test `internal/registry/refresh_test.go` — refresh delivered to the owning instance via pub/sub; not-connected → not-delivered; cross-instance delivery.
- [X] T046 [P] [US4] Contract test `tests/contract/agents_test.go` — ListConnectedAgents, RefreshInventory (delivered flag), StreamCommands registration; permission gating.
- [X] T047 [P] [US4] Security test — refresh/registry never crosses tenants; only agents:manage may refresh/enroll.

### Implementation
- [X] T048 [US4] `internal/ingest` StreamCommands handler (register agent→instance in the shared registry, deliver commands, unregister on disconnect) + agent `internal/daemon` (reconnecting stream + periodic submit loop).
- [X] T049 [US4] `internal/httpapi/agents.go` (GET /agents, POST /agents/{host_id}/refresh, POST /agents/enroll-token, POST /agents/{id}/revoke) + OpenAPI; gRPC InventoryAgentService (ListConnectedAgents/RefreshInventory/MintEnrollmentToken/RevokeAgent); publish agent.online/offline.
- [X] T050 [US4] SSE `internal/httpapi/stream.go` (GET /stream) relaying snapshot-received/agent-online/offline; wire the hub in app.
- [X] T051 [P] [US4] UI: `ui/src/views/agents/` (online/offline, refresh, issue enrollment token) + `stores/agents.ts` + `stores/live.ts` (SSE) for live host/agent updates.

## Phase 7: User Story 5 — Fleet statistics and backup (Priority: P3)

### Tests (write first, must fail)
- [X] T052 [P] [US5] Unit test `internal/stats/stats_test.go` — host counts by status/os/manufacturer, hardware/software rollups, agents online/offline, stale hosts, snapshots/day; system-wide admin breakdown.
- [X] T053 [P] [US5] Unit test `internal/backup/backup_test.go` — export/import round-trip (hosts + latest/full snapshots + tags + changes); ids preserved; skip vs overwrite; schema version; no cross-tenant leakage.
- [X] T054 [P] [US5] Contract test `tests/contract/stats_backup_test.go` — /statistics + /backup shapes; credentials never exported.

### Implementation
- [X] T055 [US5] `internal/stats/stats.go` + `internal/httpapi/statistics.go` + gRPC InventoryStatisticsService + OpenAPI (tenant + system-wide admin).
- [X] T056 [US5] `internal/backup/backup.go` + `internal/httpapi/backup.go` (export include_history flag, import mode) + OpenAPI; agent/enrollment secrets excluded.
- [X] T057 [P] [US5] UI: `ui/src/views/dashboard/` statistics widgets (hosts by status/os, hardware/software rollups, agents online/offline, stale hosts) + export/import controls.

## Phase 8: Platform integration & polish

- [X] T058 Stack wiring in `deploy/stack/`: add an `inventory` DB + `inventory_app` role to init-db.sql; an `inventory` Valkey user; an `inventory` compose service (enrolls for its mesh SVID, mounts policy + kek, exposes the ingest-edge port), an `inventory-token` mint init, and `configs/inventory.yaml`.
- [X] T059 Gateway allow-list: add `spiffe://example.org/svc/inventory=/api/inventory;inventory` to gateway-bootstrap; confirm the service registers (registered:true) and the Inventory menu renders.
- [X] T060 `services/inventory/deploy/policy.yaml` (service-to-service: gateway-forwards; module callers of Inventory Host/Snapshot/Statistics/Agent) + `deploy/kek.dev` fixture.
- [X] T061 Agent packaging: retain/port cross-platform packaging — Windows service (`internal/winsvc`) + MSI and a Linux systemd unit + enrollment/config docs in `deploy/`.
- [X] T062 [P] `services/inventory/deploy/README.md` (server + agent ops, enrollment, ingest edge, security notes) + update `deploy/stack/README.md` to list inventory.
- [X] T063 [P] `pkg/inventoryclient/` typed Go client for module-to-module use + `inventoryclient_test.go`.
- [X] T064 Coverage gate: `go -C services/inventory test ./...` ≥80% overall (with the integration harness), 100% on sealed/authz/enroll; `govulncheck` clean; wire `scripts/coverage-gate.sh` into `make cover`.
- [X] T065 Stack smoke test: bring up the stack, confirm inventory registers (registered:true), enroll a test agent, submit a snapshot, and run quickstart Scenario 1 end-to-end.
- [X] T066 [P] Fuzz + negative tests for the snapshot-ingest parser (oversized/malformed), the enrollment path (reused/expired/cross-tenant token) and the diff engine (Constitution IV).

## Dependencies & sequencing

- Setup (P1) → Foundational (P2) block everything.
- US1 (P1) is the MVP (enroll + ingest + host list). US2 (P1) depends on US1 (hosts/snapshots exist to inspect). US3/US4 (P2) depend on US1/US2. US5 (P3) depends on data existing.
- The off-mesh agent/ingest/registry (T014/T015/T028/T048) and the cross-platform collectors (T029/T035) are the inventory-specific novel work vs prior modules.

## Implementation strategy

MVP first: Setup + Foundational + US1 (enroll+ingest+host list) → demoable. Then US2
(browse/inspect) → US3 (history/diff) → US4 (refresh/live) → US5 (stats/backup) →
Phase 8 (stack + agent packaging + coverage + smoke).

## Summary

- **Total tasks**: 66 across 8 phases.
- **Per story**: US1=10 (T022–T031), US2=7 (T032–T038), US3=6 (T039–T044), US4=7 (T045–T051), US5=6 (T052–T057).
- **Parallelizable**: tasks marked [P] (distinct files) — notably tests, UI views, client, docs.
- **MVP scope**: Setup + Foundational + US1.
- **Independent tests**: each user-story phase lists its own tests (write-first) and a checkpoint/independent-test criterion in spec.md.
