# Tasks: Teleport Join Delivery Through the Inventory Agent

**Input**: spec.md; contract in `sdk/api/proto/inventory/v1/inventory.proto`;
module side go-tangra-teleport `specs/002-agent-ssh-enrollment`.
Mirrors 037 task by task.

## Phase 1: Contract

- [X] T001 Proto messages and services (done before this branch, unchanged)
- [X] T002 [P] Capability `teleport.v1`, `registry.Command.TeleportJoin`, `COMMAND_TYPE_TELEPORT_JOIN` mapping

## Phase 2: Inventory server

- [X] T003 Migration `0013_teleport_join.sql` with RLS, grants, active unique index per (host, cluster)
- [X] T004 [P] Store model, repo interface, repodb, memstore, repotest contract (+ integration test, compiles without Docker)
- [X] T005 Config `teleport_join` with bounds tests
- [X] T006 `internal/teleportjoin`: Create (eligibility, idempotent, supersede), GetMany, Cancel, ListTargets, push, replay, sweep (100 % coverage)
- [X] T007 Fetch relay from `TeleportJoinSource` (budget 5, NotFound → source_refused, Unavailable otherwise), Report (states, closed reasons, accepted=false on terminal/duplicate, detail sanitised)
- [X] T008 Audit events and `TeleportItemRow` without material
- [X] T009 `internal/ingest` FetchTeleportJoin / ReportTeleportJoin, per-agent and global fetch slots, plaintext refusal, replay in StreamCommands
- [X] T010 `internal/grpcapi/teleportjoin.go` + registration, source check, error mapping
- [X] T011 `internal/app` wiring (metrics, source client, sweeper, purge)
- [X] T012 `deploy/policy.yaml` + bundle rule `teleport-join`, policy test

## Phase 3: Agent

- [X] T013 Config `teleport` (enabled, timeout_minutes, allow_insecure_transport), packaging sample + test
- [X] T014 `internal/agentteleport`: material validation, download + checksum, safe extraction, atomic install, yaml.v3 config, unit, systemctl, wait, token removal on failure; injectable root/systemctl/HTTP/clock; every reason tested (100 % coverage, `_linux.go` excluded)
- [X] T015 `internal/sender/teleport.go` client
- [X] T016 `internal/daemon/teleport.go`: announce, one at a time (busy), preflight before fetch, fetch retries, installing → outcome report, unsent report kept
- [X] T017 `cmd/inventory-agent` wiring

## Phase 4: Polish

- [X] T018 Docs: `deploy/README.md`, `docs/help/operations.md`, `README.md`, `packaging/agent.yaml`
- [X] T019 Gates: gofmt, build, vet (+integration tag), test -race, coverage gate, agent cross-compile; integration suites not run (no Docker)
- [ ] T020 Release and rollout (needs explicit approval)
