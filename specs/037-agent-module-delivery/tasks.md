# Tasks: Deliver Module Install Bundles Through the Inventory Agent

**Input**: spec.md, plan.md, research.md, data-model.md, contracts/
Tests come first in each phase. `[P]` = parallel (different files).

## Phase 1: Contract (inventory SDK)

- [X] T001 Add the proto messages and services of contracts/proto.md to `sdk/api/proto/inventory/v1/inventory.proto`; `make generate`; `make proto-check` (wire compatible)
- [X] T002 [P] Capability constant `module.v1` and `registry.Command.Module` (`internal/store/models.go`, `internal/registry`)

## Phase 2: Inventory server (US1)

- [X] T003 Migration `0012_module_delivery.sql` (data-model.md) with RLS and grants like 0010
- [X] T004 [P] Store models + repo interface + repodb + memstore for module items (create idempotent, supersede, get by delivery, agent's active items, fetch/report transitions, sweep)
- [X] T005 Tests: create (idempotent replay, ineligible host refused with reason, ambiguous agent, supersede), source check, disabled → FailedPrecondition
- [X] T006 `internal/moduledelivery`: Create, Get, ListTargets (capability + IPs from latest snapshot), push to online agents, replay at connect
- [X] T007 Tests: fetch only own active item (NotFound + audit otherwise), fetch budget 5, expired, plaintext edge refused, source error → Unavailable and item counted, size > 12 MiB / digest mismatch refused, buffer zeroed
- [X] T008 Fetch relay: stream from `ModuleBundleSource` (mesh client via Freya), verify header size/sha256, forward with `item_id` set; report handling (own item only, closed reasons, idempotent duplicate)
- [X] T009 Sweeper: fetched without report after `report_timeout_minutes` → failed(report_timeout); past `expires_at` → expired
- [X] T010 `internal/grpcapi/moduledelivery.go` + registration; `internal/ingest` FetchModuleBundle/ReportModule + replay in StreamCommands; config `module_delivery` with validation; wiring in `internal/app`
- [X] T011 Audit events (created, fetched, reported, refused) without material

## Phase 3: Agent (US1–US3)

- [X] T012 Tests (`internal/agentmodules`): archive rules (abs, `..`, links, outside `<module>/`, > 300 files, > 16 MiB, duplicate names), modes (private/.env 0600, exec 0755, dirs 0750), existing dir → already_installed, staging cleaned on failure
- [X] T013 `internal/agentmodules` writer on `os.Root` (O_EXCL|O_NOFOLLOW), owner/group from config, disk-space check, hook via the 033 hook runner with MODULE_* env
- [X] T014 Config `modules` (contracts/agent-config.md) with validation; capability announce rule
- [X] T015 `internal/daemon/modules.go`: queue (dedupe item#attempt), fetch stream with checks, write, hook, report (retry unsent reports as 033)
- [X] T016 `packaging/agent.yaml` section, `packaging/examples/module-compose-up.sh`, packaging test if needed

## Phase 4: Gateway (go-tangra-portal-v4)

- [X] T017 Migration 0008 + store/memstore/adapter: agent join fields, render counter, JTI update
- [X] T018 Tests: deliver (non-admin 403, bad host_id/inputs 400, ineligible 409, inventory down 503), targets, progress with delivery, RenderModuleBundle (wrong caller, download join, host/tenant mismatch, expired, budget → errors; success mints token with join expiry, records JTI, audits without material)
- [X] T019 `internal/grpcapi/modulebundle.go`, `internal/httpapi/catalogue_deliver.go`, config `catalogue.agent_delivery`, wiring, OpenAPI (+ spec 003 copy), policy rule
- [X] T020 Shell wizard: Download / Deliver to a host, host picker with eligibility, pre-fill, delivery progress; vitest

## Phase 5: Polish

- [X] T021 Docs: inventory `deploy/README.md` (module delivery, policy note, agent sample, example hook), gateway `docs/operations.md`
- [X] T022 `go vet` (incl. integration tag), unit tests with -race, coverage gate (inventory 94.1%, gateway 84.1%), shell lint/test/build; integration suites not run (no Docker)
- [ ] T023 Release per plan.md and prod rollout (needs explicit approval)
