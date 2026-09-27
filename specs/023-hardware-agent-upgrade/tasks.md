---

description: "Task list for 023 Hardware Details for Devices and Agent Self-Upgrade"
---

# Tasks: Hardware Details for Devices and Agent Self-Upgrade

**Input**: Design documents from `specs/023-hardware-agent-upgrade/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/

**Tests**: MANDATORY (Constitution IV). In every phase the tests are listed
first and must be written and seen failing before the implementation tasks
of that phase. Negative security tests and fuzz tests are listed explicitly.

**Paths**: paths without a prefix are in this repository
(go-tangra-inventory-v4: agent, inventory service, SDK, UI). IPAM paths are
prefixed `go-tangra-ipam-v4/…`; production stack notes
`go-tangra-docker/…` (not edited here).

**Release tasks** (tags, PR merges, stack pins, signing key, production
host access) require explicit user confirmation before they are executed.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an unfinished task)
- **[Story]**: US1–US5 from spec.md

---

## Phase 1: Setup

**Purpose**: gates, tooling, fixtures and package skeletons.

- [ ] T001 [P] Add `internal/agentrelease`, `internal/selfupdate`, `internal/upgrades` to `SECURITY_PKGS` in `scripts/coverage-gate.sh`; exclude `internal/upgrader` (OS glue) from `COVERPKG` in `Makefile` next to `internal/collector`; add the new fuzz targets of this feature to the `fuzz` target in `Makefile`
- [ ] T002 [P] Add `FuzzNormalizeHardware` and `FuzzHardwareDiff` to the `fuzz` target in `go-tangra-ipam-v4/Makefile` (100 % gate for `internal/hostreport`, `internal/hostplan` already present in `go-tangra-ipam-v4/scripts/coverage-gate.sh`)
- [ ] T003 Add a temporary `replace github.com/go-tangra/go-tangra-inventory/sdk/v4 => ../go-tangra-inventory-v4/sdk` to `go-tangra-ipam-v4/go.mod` for development (removed in T121); `GOWORK=off go build ./...`
- [ ] T004 [P] Package skeletons with doc comments stating their security role: `internal/agentrelease/doc.go`, `internal/selfupdate/doc.go`, `internal/upgrader/doc.go`, `internal/releases/doc.go`, `internal/upgrades/doc.go`, `cmd/agent-release/main.go` (usage only)
- [ ] T005 [P] Capture scripts `scripts/capture-smbios.sh` (copies `/sys/firmware/dmi/tables/DMI` + `smbios_entry_point` and `dmidecode -t 0,1,2,3,4,16,17` text as oracle) and `scripts/capture-sysblock.sh` (copies the relevant `/sys/block/*` attribute files, resolved device paths and `/run/udev/data/b*` into a tree); fixtures from a QEMU VM, a Hyper-V VM and a desktop into `internal/agentfacts/testdata/smbios/` and `internal/agentfacts/testdata/sysblock/`
- [ ] T006 Capture the node-1 SMBIOS table and sysblock tree with the T005 scripts into `internal/agentfacts/testdata/smbios/node-1.*` and `testdata/sysblock/node-1/` (**confirm with the user: production host access**; serials may be replaced by synthetic values of equal length before commit)

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: the whole proto contract, SDK, inventory storage, validation and projection, and the IPAM storage/normalisation foundation every story builds on.

**⚠️ CRITICAL**: no user story work starts before this phase is complete.

### Tests first — inventory

- [ ] T007 [P] Proto contract check: `buf breaking --against '.git#tag=sdk/v4.1.0,subdir=sdk'` in `Makefile` (`proto-check`) and `.github/workflows/ci.yaml` (buf job, currently against sdk/v4.0.0); must fail until T015 is additive-clean
- [ ] T008 [P] Migration test `internal/repo/repodb/hardware_upgrades_integration_test.go` (`//go:build integration`): database at 0005 with hosts/agents/components → 0006 applies; defaults on new columns; CHECKs reject bad `os`/`arch`/`install_type`, release `version`/`sha256`/`file`/`size`/chunk size, upgrade `state`/`origin`/`reason`, policy window/timezone/max_concurrent; partial unique index allows one active upgrade per agent; RLS isolates `inventory_agent_upgrades` and `inventory_agent_upgrade_policy` per tenant and admits `app.system`; release tables readable in tenant scope
- [ ] T009 [P] Ingest validation tests `internal/ingest/validate_test.go`: 257 disks, 1025 memory modules, 65 arrays, 257 processors, 1025 filesystems, 65 disk refs → first N kept and counted in `Inventory.truncated`; strings > 256 bytes clipped; control characters removed; unknown media/interface/availability → `unknown`/`other`; **negative**: payload > `max_snapshot_bytes` still `InvalidArgument`; `hardware_schema` > 2 clamped to 2
- [ ] T010 [P] Extend `FuzzSubmitMapper` in `internal/ingest/ingest_fuzz_test.go` to the new hardware fields (no panic, bounds hold after `validateExtended`, valid values round-trip)
- [ ] T011 [P] Projection tests `internal/hostreport/hostreport_test.go` (100 %): `hardware` present only for `hardware_schema ≥ 2`; mapping per data-model §1.2 (disks without legacy partitions, all slots, arrays, filesystems, availability, truncated counters); digest changes when any hardware field changes and is stable otherwise; legacy snapshot → no hardware, digest unchanged vs. 4.3.x for identical other fields; extend `FuzzHostReport` in `internal/hostreport/hostreport_fuzz_test.go`
- [ ] T012 [P] Mapper round-trip tests `internal/invpb/hardware_test.go` (store ↔ proto for Filesystem, HardwareAvailability, new Processor/MemoryInfo/MemoryModule/Disk/CollectionLimits fields)
- [ ] T013 [P] SDK tests `sdk/pkg/inventoryclient/hostreport_test.go`: `HostReport.Hardware` plain-Go mapping (nil when absent), `toInventory` of the new `Inventory` fields, against the in-process fake server
- [ ] T014 [P] Component insert tests: new columns (`populated`, `type_detail`, `name`, `removable`, `family`) written by `insertComponents` in `internal/repo/repodb/hardware_upgrades_integration_test.go` and mirrored in `internal/memstore/memstore_test.go`

### Implementation — inventory

- [ ] T015 Proto per contracts/inventory-grpc.md §1–§3 in `sdk/api/proto/inventory/v1/inventory.proto` (Inventory 31–33, ChassisInfo 7, Processor 12–14, MemoryInfo 4–6, MemoryArray 6, MemoryModule 11–15, Disk 7–9, Filesystem, HardwareAvailability, CollectionLimits 6–10, HostReport 18, HardwareProfile, CommandType UPGRADE, Command 3, UpgradeCommand, AgentPlatform, StreamRequest 3–4, CheckAgentUpdate/DownloadAgentRelease/ReportUpgrade + messages); `make generate`
- [ ] T016 [P] Domain structs and bounds constants per data-model §1.1 in `internal/store/models.go`
- [ ] T017 [P] Migration `internal/store/migrations/0006_hardware_upgrades.sql` per data-model §1.3
- [ ] T018 Mappers for every new field: `internal/invpb/hardware.go` (shared), `internal/sender/mapper.go` (store → proto), `internal/ingest/ingest.go` (`inventoryFromProto`), `internal/grpcapi/mapper.go`, `sdk/pkg/inventoryclient/inventory.go` (`toInventory`)
- [ ] T019 Hardware bounds and sanitisation in `validateExtended` in `internal/ingest/validate.go` (research D5)
- [ ] T020 Projection hardware section + schema gate in `internal/hostreport/hostreport.go`
- [ ] T021 New component columns in `insertComponents` in `internal/repo/repodb/db.go` and in `internal/memstore/memstore.go`
- [ ] T022 SDK plain-Go `Hardware` types and mapping in `sdk/pkg/inventoryclient/hostreport.go` per contracts/inventory-grpc.md §2

### Tests first — IPAM

- [ ] T023 [P] Migration test `go-tangra-ipam-v4/internal/repo/repodb/hardware_integration_test.go` (`//go:build integration`): database at 0008 with devices → 0009 applies; digest CHECK; profile size CHECK (> 256 KiB rejected); cascade on device delete; RLS isolates `ipam_device_hardware` and admits `app.system`
- [ ] T024 [P] Hardware normalisation tests `go-tangra-ipam-v4/internal/hostreport/hardware_test.go` (100 %): valid profile normalises (closed sets, summary inputs); report without hardware → `Report.Hardware == nil`; **negative**: 257 disks / 1025 slots / 257 processors / 1025 filesystems / 65 disk refs → first N + `hardware_truncated_*` issues; strings > 256 bytes, invalid UTF-8 and control characters cleaned; encoded profile > 256 KiB → hardware dropped with issue; digest deterministic; `FuzzNormalizeHardware` in `go-tangra-ipam-v4/internal/hostreport/hardware_fuzz_test.go` (no panic, output within bounds, valid UTF-8)
- [ ] T025 [P] Audit tests `go-tangra-ipam-v4/internal/audit/audit_test.go`: `hardware_reported`, `hardware_updated` known; detail keys used by the hardware planner (`changes`, `field`, `before`, `after`, `summary`, `changes_truncated`) survive the guard
- [ ] T026 [P] Negative API tests: device create/update over HTTP reject `hardware_summary`/`hardware` (strict decode 400) in `go-tangra-ipam-v4/internal/httpapi/hardware_test.go`; gRPC `CreateDevice`/`UpdateDevice` ignore a caller-supplied `hardware_summary` in `go-tangra-ipam-v4/internal/grpcapi/grpcapi_mapper_more_test.go`

### Implementation — IPAM

- [ ] T027 Migration `go-tangra-ipam-v4/internal/store/migrations/0009_device_hardware.sql` per data-model §2.1
- [ ] T028 [P] Models `DeviceHardware`, `HardwareSummary`, `HardwareProfile`, `Device.HardwareSummary` in `go-tangra-ipam-v4/internal/store/models.go`
- [ ] T029 [P] `go-tangra-ipam-v4/internal/hostreport/hardware.go` and `Report.Hardware` filled in `go-tangra-ipam-v4/internal/hostreport/normalize.go` (data-model §2.4)
- [ ] T030 [P] Audit vocabulary `hardware_reported`, `hardware_updated` in `go-tangra-ipam-v4/internal/audit/audit.go`
- [ ] T031 Store contract `HostTx.GetHardware`/`ReplaceHardware`, `Store.GetDeviceHardware`, device reads with `hardware_summary` in `go-tangra-ipam-v4/internal/repo/repo.go`, `go-tangra-ipam-v4/internal/repo/repodb/hostsync.go`, `go-tangra-ipam-v4/internal/repo/repodb/db.go`, `go-tangra-ipam-v4/internal/memstore/hostsync.go`, `go-tangra-ipam-v4/internal/memstore/memstore.go`

**Checkpoint**: proto/SDK complete; inventory stores and projects hardware (still collected by the old decoder, so no report carries `hardware` yet); IPAM can normalise and store it. Nothing visible changes.

---

## Phase 3: User Story 1 — Correct hardware facts from the agent (Priority: P1) 🎯 MVP

**Goal**: DSP0134-correct SMBIOS names, every memory slot, arrays, chassis type, processor family/socket; `hardware_schema = 2`; inventory host page shows it.

**Independent Test**: decoding the corpus yields the DSP0134 names (0 shifted values, SC-001); node-1 shows DDR4 / DIMM / System memory / System board / Single-bit ECC (quickstart 1).

### Tests for User Story 1 (MANDATORY) ⚠️

- [ ] T032 [P] [US1] Decoder table tests `internal/agentfacts/smbios_test.go`: every code of every table (memory type 01h–24h incl. reserved 15h–17h, form factor, type detail bits 1–15 of the WORD incl. Registered bit 13 / Unbuffered bit 14 / LRDIMM bit 15, array location/use/ECC, chassis type incl. lock bit masking, processor type/family/family-2 via FEh/upgrade, board type, wake-up type); unknown codes → `Unknown (code N)`; size 0 → empty slot, FFFFh → unknown size, 7FFFh → extended size; speed 0 → unknown, FFFFh → extended speed; fields beyond `Header.Length` → not available (SMBIOS 2.x structures)
- [ ] T033 [P] [US1] Corpus golden tests `internal/agentfacts/smbios_corpus_test.go`: every `testdata/smbios/*.bin` decoded via `smbios.Decode` + the new decoder equals its golden JSON; node-1 golden asserts DDR4, DIMM, `Registered (Buffered)`, `System board or motherboard`, `System memory`, `Single-bit ECC`, `Rack Mount Chassis`, 16/16 slots, 2 processors with family and socket (SC-001); VM fixtures → availability `partial` without errors
- [ ] T034 [P] [US1] `FuzzSMBIOSStructures` in `internal/agentfacts/smbios_fuzz_test.go`: arbitrary header/formatted/strings never panic, every output string ≤ 256 bytes, slot/array counts within bounds
- [ ] T035 [P] [US1] Mapper tests `internal/collector/collector_test.go` (`mapHardware` over decoded fixtures): `HardwareSchema = 2`, arrays/slots totals, only "System memory" arrays counted in the total, chassis type and bootup state set, processor family/type/upgrade set, no use of go-smbios enum `String()` (grep test on `hardware.go`); SMBIOS unavailable → `Availability.SMBIOS = unavailable`, collection still succeeds
- [ ] T036 [P] [US1] Diff tests `internal/diff/diff_test.go`: previous `hardware_schema < 2` and next = 2 → hardware categories suppressed, one `hardware_schema` change recorded; memory changes keyed by device locator; empty ↔ populated slot recorded; processor family change recorded; extend `FuzzDiff`
- [ ] T037 [P] [US1] UI tests `ui/tests/unit/hosts.spec.ts`: memory card shows arrays, slots populated/total, empty slots, type detail, rated/configured speed; chassis type; processor family; notice for `hardware_schema < 2`; reported strings rendered as text

### Implementation for User Story 1

- [ ] T038 [P] [US1] DSP0134 tables and raw field decoding in `internal/agentfacts/smbios.go` (research D1)
- [ ] T039 [US1] Rewrite `internal/collector/hardware.go` on top of `SMBIOS.Structures` and the decoder (all type 16/17 structures incl. empty slots, chassis, processors), set `HardwareSchema` and `Availability.SMBIOS`; keep go-smbios only for discovery/splitting
- [ ] T040 [P] [US1] Schema-transition suppression and new memory/processor comparisons in `internal/diff/diff.go`
- [ ] T041 [P] [US1] Host page memory/chassis/processor sections and the legacy notice in `ui/src/views/hosts/detail.vue`, types in `ui/src/api/types.ts`

**Checkpoint**: a 4.4.0-dev agent reports correct hardware; inventory shows it; the host report now carries `hardware` (without physical disks yet).

---

## Phase 4: User Story 2 — Physical disks are reported (Priority: P1)

**Goal**: physical disks with model, serial, size, media, interface, removable on Linux and Windows; filesystems linked to their disks; virtual block devices excluded.

**Independent Test**: simulated block trees → exactly the physical disks with correct fields, dm/md/loop/zram excluded, USB removable (quickstart 2, SC-002).

### Tests for User Story 2 (MANDATORY) ⚠️

- [ ] T042 [P] [US2] sysfs parser tests `internal/agentfacts/disks_test.go` over `testdata/sysblock/*`: NVMe (`nvme_ssd`, nvme), SATA SSD (`ssd`, sata), SATA HDD (`hdd`, sata), SAS, virtio (`unknown`, virtio), Hyper-V, USB (removable), mmc; excluded `loop*`, `ram*`, `zram*`, `dm-*`, `md*`, `nbd*`, `sr*`, `fd*`, size-0 devices; serial fallbacks `device/serial` → `vpd_pg80` → udev `ID_SERIAL_SHORT`; vendor prefix; attribute reads capped at 4 KiB; 257 disks → 256 + truncated
- [ ] T043 [P] [US2] Filesystem resolution tests `internal/agentfacts/disks_resolve_test.go`: partition → parent disk; LVM `dm-0` → `slaves/sda3` → `sda`; md RAID1 → two disks; LVM on md; depth > 8 and slave cycles stop without panic; unknown device → no disks; 65 disks on one filesystem → 64 + truncated
- [ ] T044 [P] [US2] Windows parser tests `internal/agentfacts/disks_windows_test.go` (pure, all platforms): `Get-PhysicalDisk` JSON (single object and array), `MediaType`/`BusType` codes and strings → closed sets, `Win32_DiskDrive` fallback JSON, drive-letter → disk mapping; `FuzzWindowsDisks` and `FuzzSysBlock` in `internal/agentfacts/disks_fuzz_test.go`
- [ ] T045 [P] [US2] Collector tests `internal/collector/disks_linux_test.go` with an `fs.FS` fake root: whole collection ≤ 5 s budget honoured (slow FS fake), `collect_disks: false` → no disks and availability `unsupported`, read errors → availability `partial`, filesystems still reported
- [ ] T046 [P] [US2] Diff and statistics tests: disks keyed by `name|serial` in `internal/diff/diff_test.go`; `total_disk_bytes` sums non-removable physical disks of schema-2 snapshots (legacy snapshots contribute 0 as today) in `internal/repo/repodb/hardware_upgrades_integration_test.go` and `internal/stats/stats_test.go`
- [ ] T047 [P] [US2] UI tests `ui/tests/unit/hosts.spec.ts`: disks table (name, model, serial, size, media, interface, removable badge) and filesystems table with disk column and usage

### Implementation for User Story 2

- [ ] T048 [P] [US2] `internal/agentfacts/disks.go` (sysfs parser, resolution, Windows JSON parser; research D3)
- [ ] T049 [US2] Collectors `internal/collector/disks_linux.go`, `internal/collector/disks_windows.go` (PowerShell, 30 s timeout, no shell interpolation), `internal/collector/disks_other.go`; replace `collectDisks` in `internal/collector/osinfo.go`; `AgentConfig.CollectDisks` in `internal/config/config.go`; `packaging/agent.yaml` comment
- [ ] T050 [P] [US2] Disk key in `internal/diff/diff.go`; physical-disk total in the statistics query in `internal/repo/repodb/db.go` and `internal/memstore/memstore.go`
- [ ] T051 [P] [US2] Disks and filesystems sections in `ui/src/views/hosts/detail.vue`

**Checkpoint**: inventory shows complete, correct hardware for new agents (FR-009); the host report carries the full hardware profile.

---

## Phase 5: User Story 3 — Hardware shown on IPAM devices (Priority: P1)

**Goal**: IPAM stores the reported hardware per host-reported device, audits changes field by field, shows a Hardware tab and a summary line.

**Independent Test**: a report with hardware updates the device; the tab shows it; a BIOS/disk change appears after the next report and is audited (quickstart 3).

### Tests for User Story 3 (MANDATORY) ⚠️

- [ ] T052 [P] [US3] Planner tests `go-tangra-ipam-v4/internal/hostplan/hardware_test.go` (100 %): first hardware → `OpReplaceHardware` + `hardware_reported`; equal digest → no op; BIOS version/date change → one `hardware_updated` with exactly those fields; memory slot added/removed/changed by locator; disk added/removed/changed by serial (name when no serial); processor change; > 100 changes → 100 + `changes_truncated`; report without hardware → no op, stored row untouched; summary values (cores/threads sums, memory type of populated modules, non-removable disk total); **negative**: no op ever touches device columns (`firmware_version`, `serial_number`, `description`, …) for any input
- [ ] T053 [P] [US3] Property/fuzz tests: re-planning the same report after apply yields zero hardware ops; `FuzzHardwareDiff` in `go-tangra-ipam-v4/internal/hostplan/hardware_fuzz_test.go` (no panic, bounded change list, deterministic order)
- [ ] T054 [P] [US3] Apply/store integration `go-tangra-ipam-v4/internal/repo/repodb/hardware_integration_test.go`: hardware replace + audit row in the host transaction; forced failure rolls both back; **negative (cross-tenant)**: tenant A apply cannot read or write tenant B hardware (RLS)
- [ ] T055 [P] [US3] End-to-end `go-tangra-ipam-v4/internal/hostsync/e2e_integration_test.go` (`//go:build integration`): in-process inventory `HostReportService` (SDK v4.2.0 via replace) serving a report with hardware → device hardware stored and summary visible; changed disk → `hardware_updated`; inventory without hardware (legacy snapshot) → device works, no hardware, no errors (FR-006 compatibility)
- [ ] T056 [P] [US3] HTTP tests `go-tangra-ipam-v4/internal/httpapi/hardware_test.go`: `GET /devices/{id}/hardware` (200, 404 without hardware, 404 for another tenant's device, `ipam:read` required), `hardware_summary` on device get/list, `has_hardware` filter; OpenAPI contract test in `go-tangra-ipam-v4/api/openapi/openapi_test.go` (new path, schemas, permission)
- [ ] T057 [P] [US3] UI tests `go-tangra-ipam-v4/ui/tests/unit/views.spec.ts`: Hardware tab only with `hardware_summary`; empty state for host-reported devices without hardware; none for manual devices; summary line formatting; empty slots greyed; reported strings rendered as text (no HTML injection)

### Implementation for User Story 3

- [ ] T058 [US3] Sub-planner `go-tangra-ipam-v4/internal/hostplan/hardware.go`; op kind `OpReplaceHardware` and state field in `go-tangra-ipam-v4/internal/hostplan/plan.go` (`Build` calls it)
- [ ] T059 [US3] Dispatch `OpReplaceHardware` in `go-tangra-ipam-v4/internal/hostsync/apply.go`; load stored hardware into the planner state in `go-tangra-ipam-v4/internal/hostsync/runner.go`
- [ ] T060 [P] [US3] `GET /devices/{id}/hardware` in `go-tangra-ipam-v4/internal/httpapi/hardware.go` registered from `go-tangra-ipam-v4/internal/httpapi/handlers.go`; summary + filter in `go-tangra-ipam-v4/internal/devices/devices.go`; OpenAPI in `go-tangra-ipam-v4/api/openapi/ipam.yaml`; `npm run gen:api` → `go-tangra-ipam-v4/ui/src/api/schema.d.ts`
- [ ] T061 [P] [US3] `HardwareSummary` message and `Device.hardware_summary` in `go-tangra-ipam-v4/sdk/api/proto/ipam/v1/ipam.proto` (`make generate`), mapper in `go-tangra-ipam-v4/internal/grpcapi/mapper.go`
- [ ] T062 [P] [US3] `go-tangra-ipam-v4/ui/src/components/HardwarePanel.vue`, Hardware tab + summary row in `go-tangra-ipam-v4/ui/src/views/devices/detail.vue`, optional CPU/memory columns in `go-tangra-ipam-v4/ui/src/views/devices/index.vue`, types in `go-tangra-ipam-v4/ui/src/api/types.ts`

**Checkpoint**: with a new agent + inventory 4.4.0-dev + ipam dev build, the IPAM device shows hardware (SC-003 in the lab).

---

## Phase 6: User Story 4 — Upgrade agents from the platform (Priority: P1)

**Goal**: signed releases in the image and DB; persisted upgrade requests delivered over the command stream; verified download over the ingest edge; package-manager or binary install with automatic rollback; fleet view; `update [--check]`.

**Independent Test**: an older agent receives a request, downloads, verifies, installs, reports the new version; tampered/wrong-platform artifacts are refused and the old agent keeps running (quickstart 4, SC-004/SC-005).

### Tests for User Story 4 (MANDATORY) ⚠️

- [ ] T063 [P] [US4] `internal/agentrelease/*_test.go` (100 %): strict manifest parsing (unknown field, > 64 KiB, schema ≠ 1, bad version, > 16 artifacts, duplicate platform, bad file name, size 0 / > 150 MiB, uppercase or short sha256); keyring (unknown key id, empty keyring refused); `Verify` (valid; one flipped manifest byte; flipped signature byte; truncated signature; signature by another key); platform selection (missing entry); version compare table (semver, pre-release, `4.3.1~11-gabc` < `4.3.1`, `dev`/garbage not comparable); `FuzzManifest`, `FuzzVersion` in `internal/agentrelease/fuzz_test.go`
- [ ] T064 [P] [US4] Release tool tests `cmd/agent-release/main_test.go`: keygen → sign → verify round trip in a temp dir; verify fails after modifying one artifact byte or the manifest; the private key is read only from the named environment variable (flag value refused)
- [ ] T065 [P] [US4] Release-binary check `scripts/check-release-binary.sh` + `make release-check`: `go version -m` of every release artifact shows no `agentdevkey` build tag and the dev key id string is absent (**negative**: a dev-tagged build fails the check)
- [ ] T066 [P] [US4] Release store tests `internal/releases/releases_test.go` (memstore): bundle verified and seeded idempotently; **negative**: invalid signature, unknown key, missing artifact file, size/sha mismatch, symlink or path traversal in the bundle dir → refused, logged and audited `agent_release_imported` refused; retention keeps the platform current version, tenant pins and active targets; artifact reader returns chunks in order with correct offsets; import path shares verification
- [ ] T067 [P] [US4] Release integration `internal/repo/repodb/releases_integration_test.go` (`//go:build integration`): two replicas seeding the same bundle concurrently → one release, complete artifacts (advisory lock); 25 MiB artifact round-trips through chunks
- [ ] T068 [P] [US4] Upgrade service tests `internal/upgrades/upgrades_test.go` (100 %): request one / selection (≤ 1000) / all outdated with skip reasons; target = pin or platform current; `allow_downgrade` only when a pin is lower; active request → `upgrade_active`; cancel only pending/delivered; delivery on create via fake registry (delivered true/false) and on connect; every valid transition and every invalid one (data-model §1.4); `succeeded` requires matching version; expiry and stale-progress sweepers; each transition writes its audit row in the same transaction (memstore tx failure removes both); fleet state derivation table (data-model §1.5); content-free realtime events after commit
- [ ] T069 [P] [US4] Ingest upgrade RPC tests `internal/ingest/upgrade_test.go`: `StreamCommands` stores platform/capabilities (invalid values dropped) and sends pending UPGRADE commands; registry `Command` carries the upgrade payload through the Valkey JSON encoding (`internal/registry/registry_test.go`); `CheckAgentUpdate` with/without `apply` (origin agent, audited); `DownloadAgentRelease` header then contiguous chunks; **negative**: no credential → `Unauthenticated`; another agent's or tenant's request id → `NotFound` + `agent_upgrade_refused`; inactive request → `FailedPrecondition`; version ≠ target → `NotFound`; current-version (rollback) download allowed without request; second concurrent stream of one agent and the global cap → `ResourceExhausted`; `ReportUpgrade` with unknown state/reason or detail > 256 bytes → `InvalidArgument`; `succeeded` from a connection reporting another version → ignored
- [ ] T070 [P] [US4] Download integration `internal/ingest/upgrade_integration_test.go` (`//go:build integration`): real ingest gRPC server (bufconn, TLS test certs, per-agent credential) + repodb: full download of a seeded artifact verifies with the agent-side verifier
- [ ] T071 [P] [US4] HTTP tests `internal/httpapi/upgrades_test.go`: fleet list (online + offline, filters, paging, legacy keys kept), `GET /agents/{id}`, `POST /agents/upgrades` (oneOf, ≤ 1000 ids, unknown fields 400, CSRF required, 413), cancel 409, `GET /agents/upgrades`, `GET /agent-releases`; `agents:manage` required (member/auditor/viewer → 403); OpenAPI contract test `api/openapi/openapi_test.go` for the new routes, schemas and permissions
- [ ] T072 [P] [US4] Selfupdate core tests `internal/selfupdate/*_test.go` (100 %, fakes for installer/runner/fs/clock/client): happy paths deb, rpm, binary, windows (stage 0700, streaming hash, lock, state file, helper argv); each verification failure (`signature_invalid`, `unknown_key`, `checksum_mismatch`, `size_mismatch`, `platform_mismatch`, `version_mismatch`) → `ReportUpgrade(failed, reason)` and the installer is never called; downgrade refused without flag, allowed with flag, never below the compiled floor; free-space check → `disk_full`; duplicate request id ignored; lock held → `busy`; confirm on start (phase `awaiting_confirm`, own version = `to`) only after connect + submit; rolled-back report on start; helper: confirm timeout → rollback with the previous package; missing rollback package → binary restore with `package_db_mismatch`; install failure → rollback; state file with wrong owner/mode/location → refused; cleanup keeps the last two versions
- [ ] T073 [P] [US4] Install-type detection tests `internal/agentfacts/installtype_test.go` (dpkg-query/rpm outputs and exit codes, executable path, Windows)
- [ ] T074 [P] [US4] OS glue tests `internal/upgrader/upgrader_test.go` with a fake runner: exact argv for `dpkg -i --force-confold`, `rpm -U --replacepkgs`, `rpm -U --oldpackage`, `systemd-run --unit … --collect --property=Type=exec`, `systemctl restart`; no shell; `DEBIAN_FRONTEND=noninteractive`; dpkg lock busy retried 3× over 60 s; no systemd → `unsupported_install`; Windows process creation flags and SCM stop/start sequence in `internal/upgrader/service_windows_test.go` (build-tagged)
- [ ] T075 [P] [US4] Agent entry tests `cmd/inventory-agent/main_test.go`: `update` subcommand detection, `-check` exit codes 0/10/1, lock-held exit 2, unenrolled refusal; `upgrade-apply` refuses a state file outside the staging dir; `internal/daemon/daemon_test.go` (new): `StreamRequest` carries platform + `upgrade.v1`, UPGRADE command dispatched to selfupdate, `upgrade.enabled: false` ignores server requests, REFRESH unchanged
- [ ] T076 [P] [US4] UI tests `ui/tests/unit/agents.spec.ts`: fleet table (online/offline, version, target, state chip with reason, last change), Upgrade / Upgrade selected / Upgrade all outdated / Cancel shown only with `{manage, InventoryAgent}`, `manual_upgrade_required` explained, reason codes rendered as text
- [ ] T077 [US4] Container e2e `tests/e2e/upgrade_test.go` (`//go:build e2e`, `make e2e-upgrade`, CI job): Debian 12 and Rocky 9 containers with systemd; install package N; in-process inventory serves a dev-signed N+1 → `dpkg -s`/`rpm -q` show N+1 and the agent reports N+1; N+1 whose binary exits at start → rollback to N within the confirm timeout, package DB shows N; tampered artifact → refused, N keeps running (SC-005)

### Implementation for User Story 4 — release pipeline

- [ ] T078 [P] [US4] `internal/agentrelease/{manifest.go,verify.go,keys.go,keys_dev.go,version.go,platform.go}` (`keys_dev.go` under build tag `agentdevkey`; production key added in T119)
- [ ] T079 [P] [US4] `cmd/agent-release/main.go` (keygen, sign, verify; contracts/agent-cli.md)
- [ ] T080 [US4] `Makefile`: `agent-release` (8 version-stamped artifacts in `dist/agent/`, deb/rpm via nfpm), `agent-release-dev` (dev-signed, copied to `agent-releases/<version>/`), version ldflags for `agent-windows`, `release-check`, `e2e-upgrade`
- [ ] T081 [US4] `.github/workflows/ci.yaml`: `packages` builds all 8 artifacts; new `sign` job (tags only, environment `release`, secret `AGENT_RELEASE_SIGNING_KEY`, runs `agent-release sign` + `verify` + `release-check`); `docker` downloads the signed bundle into `agent-releases/<version>/` before the build; `release` uploads artifacts, manifest, signature and `SHA256SUMS`; `e2e-upgrade` job; SBOM for the agent artifacts
- [ ] T082 [P] [US4] `Dockerfile` copies `agent-releases/` to `/app/agent-releases/`; `agent-releases/.gitkeep`; `.dockerignore` keeps the directory

### Implementation for User Story 4 — server

- [ ] T083 [US4] Release repo methods (upsert under advisory lock, chunk write/read, list, retention delete) in `internal/repo/repo.go`, `internal/repo/repodb/releases.go`, `internal/memstore/releases.go`
- [ ] T084 [US4] `internal/releases/releases.go` (bundle verification + seeding, import, retention, artifact reader); `agent_releases` config section with validation in `internal/config/config.go`; startup seeding in `internal/app/app.go`; `inventorysvc agent-release import` in `cmd/inventorysvc/main.go`
- [ ] T085 [US4] Upgrade and agent-platform repo methods with transactional audit in `internal/repo/repo.go`, `internal/repo/repodb/upgrades.go`, `internal/memstore/upgrades.go`
- [ ] T086 [P] [US4] Audit vocabulary and subject kinds per contracts/audit-events.md in `internal/audit/audit.go` (platform-scope rule for `agent_release_imported`); `internal/audit/audit_test.go`
- [ ] T087 [US4] `internal/upgrades/{service.go,delivery.go,transitions.go,sweeper.go,fleet.go}` + realtime events in `internal/events/` + metrics (`inventory.upgrades`); sweeper worker in `internal/app/app.go`
- [ ] T088 [US4] Ingest: platform/capabilities + pending delivery in `StreamCommands` (`internal/ingest/ingest.go`), new RPCs with caps in `internal/ingest/upgrade.go`, upgrade payload in `internal/registry/registry.go` and `internal/registry/registry_valkey.go`
- [ ] T089 [US4] HTTP `internal/httpapi/upgrades.go` (fleet, upgrades, cancel, releases) and the changed `GET /agents` in `internal/httpapi/handlers.go`; `Deps` in `internal/httpapi/deps.go`; OpenAPI in `api/openapi/inventory.yaml`; `agents:manage` description in `pkg/inventorymanifest/manifest.go`

### Implementation for User Story 4 — agent

- [ ] T090 [P] [US4] `internal/agentfacts/installtype.go`
- [ ] T091 [US4] `internal/selfupdate/{selfupdate.go,stage.go,state.go,lock.go,confirm.go,helper.go}` (research D10)
- [ ] T092 [US4] `internal/upgrader/{upgrader.go,install_deb.go,install_rpm.go,install_binary.go,systemd_linux.go,service_windows.go,detach_windows.go,upgrader_other.go}`
- [ ] T093 [US4] Daemon wiring in `internal/daemon/daemon.go` (platform + capabilities on `StreamRequest`, UPGRADE dispatch, confirmation/rollback report on start); `AgentConfig.Upgrade` in `internal/config/config.go`; `packaging/agent.yaml` `upgrade:` section
- [ ] T094 [US4] `cmd/inventory-agent/update.go` (`update [-check]`), `cmd/inventory-agent/apply.go` (`upgrade-apply`), subcommand dispatch in `cmd/inventory-agent/main.go`
- [ ] T095 [P] [US4] Fleet view and actions in `ui/src/views/agents/index.vue`, store `ui/src/stores/agents.ts`, types in `ui/src/api/types.ts`

**Checkpoint**: from the agent list one agent and then all outdated agents upgrade and roll back automatically on failure (quickstart 4).

---

## Phase 7: User Story 5 — Upgrade policy (Priority: P2)

**Goal**: per-tenant automatic upgrades (off by default) inside a window with a concurrency limit; pause on failure; administrator-only.

**Independent Test**: window 02:00–04:00 → upgrades only inside the window, never more than N at once; a failure pauses the policy (quickstart 5).

### Tests for User Story 5 (MANDATORY) ⚠️

- [ ] T096 [P] [US5] Planner tests `internal/upgrades/policy_test.go` (100 %): inside/outside the window incl. wrap past midnight and start = end (whole day); timezone with DST changes; capacity = `max_concurrent − active`; order oldest version then agent id; excludes offline, incapable, unsupported and already-active agents; disabled or paused → nothing
- [ ] T097 [P] [US5] Scheduler tests `internal/upgrades/scheduler_test.go`: per-tenant lock (two schedulers, one creates requests), requests with origin `policy`; a policy-origin `failed`/`rolled_back` pauses the policy with `upgrade_policy_paused`; a user-origin failure does not; resume clears the pause
- [ ] T098 [P] [US5] Manifest tests `pkg/inventorymanifest/manifest_test.go` and `roles_test.go`: `agentupgrades:manage` declared; granted to owner, admin and module role administrator; **not** to operator, member, auditor, editor, viewer; ability `{manage, InventoryAgentUpgradePolicy}`; every new route declares a known permission
- [ ] T099 [P] [US5] HTTP tests `internal/httpapi/upgrades_test.go` (policy): GET defaults without a row; PUT validation (window pattern, `time.LoadLocation`, `max_concurrent` 1–100, `target_version` stored or `unknown_version` 409), audit `upgrade_policy_updated` with before/after; resume audit; **negative**: operator PUT/resume → 403, missing CSRF → refused, unknown fields → 400, > 4 KiB → 413; a lower pin produces `allow_downgrade` requests only through the policy/pin path
- [ ] T100 [P] [US5] UI tests `ui/tests/unit/agents.spec.ts` (policy card): read-only without `{manage, InventoryAgentUpgradePolicy}`, paused banner with resume, window/timezone validation messages

### Implementation for User Story 5

- [ ] T101 [US5] `internal/upgrades/policy.go` (pure planner) and `internal/upgrades/scheduler.go`; scheduler worker in `internal/app/app.go`
- [ ] T102 [US5] Policy repo get/upsert/pause with transactional audit in `internal/repo/repodb/upgrades.go` and `internal/memstore/upgrades.go`
- [ ] T103 [US5] Permission, ability and policy routes: `pkg/inventorymanifest/manifest.go`, `api/openapi/inventory.yaml`, handlers in `internal/httpapi/upgrades.go`
- [ ] T104 [P] [US5] Policy card in `ui/src/views/agents/index.vue` and `ui/src/stores/agents.ts`

**Checkpoint**: all stories functional.

---

## Phase 8: Polish, Release & Cross-Cutting Concerns

- [ ] T105 [P] Docs `README.md` and `deploy/README.md`: corrected hardware fields, physical disks, `collect_disks`, self-upgrade (how it works, systemd requirement, one manual first install, `update [-check]`, rollback, fleet states, policy, permissions `agents:manage`/`agentupgrades:manage`, `agent_releases` config, import CLI, DB size ~80 MB per release)
- [ ] T106 [P] `SECURITY.md`: signing-key custody (GitHub environment `release` with required reviewers), rotation (new key id in the keyring, old key kept until every agent upgraded), compromise procedure
- [ ] T107 [P] Docs `go-tangra-ipam-v4/README.md`: Hardware tab, audit `hardware_*`, dependency on inventory ≥ 4.4.0
- [ ] T108 [P] Additional negative security tests `internal/upgrades/security_test.go`: forged request ids, replayed `ReportUpgrade` from another agent, bundle with oversized manifest, artifact > 150 MiB, agent reporting `succeeded` with a wrong version, request storm (1000 ids) stays bounded
- [ ] T109 [P] v3 parity checklist `specs/023-hardware-agent-upgrade/checklists/v3-parity.md` (BIOS, board/system serial, chassis, CPU model/count, memory total/type/speed, disks name/SSD-HDD/model/size, `update --check`, server-pushed update, checksum, atomic swap with rollback → task/test proving each)
- [ ] T110 Security and constitution review of both repositories (all seven principles, STRIDE table in research.md re-checked against the code; second reviewer for `internal/agentrelease`, `internal/selfupdate`, `internal/upgrader`, `internal/ingest/upgrade.go`, `internal/releases`, the CI `sign` job and IPAM `internal/hostplan/hardware.go`)
- [ ] T111 Gates — inventory: `make lint`, `make test`, `make cover` (100 % set incl. `internal/agentrelease`, `internal/selfupdate`, `internal/upgrades`; ≥ 80 % total), `make fuzz`, `make test-integration`, `make proto-check`, `make vuln`, `make release-check`, `make e2e-upgrade`, agent cross-compile matrix, `cd ui && npm run lint && npm run test:unit && npm run build`
- [ ] T112 Gates — ipam: `make lint`, `make test`, `make cover` (100 % for `internal/authz`, `internal/sealed`, `internal/ipnet`, `internal/hostreport`, `internal/hostplan`; ≥ 80 % total), `make fuzz`, `make test-integration`, `buf lint` + `buf breaking`, `make vuln`, `cd ui && npm run lint && npm run test:unit && npm run build`
- [ ] T113 Upstream report to siderolabs/go-smbios describing the enum offset (F1/F2) with a reproducer (**confirm with the user**; optional, no dependency on it)
- [ ] T114 Merge the feature branch of go-tangra-inventory-v4 after review (**confirm with the user**)
- [ ] T115 Merge the feature branch of go-tangra-ipam-v4 after T121 (**confirm with the user**)
- [ ] T116 go-tangra-docker notes (**confirm with the user; not part of this repo's change**): pin inventory 4.4.0 and ipam 4.6.0, optional `agent_releases` config, DB size note, manual agent 4.4.0 install instructions per host type
- [ ] T117 Run quickstart.md in freya-stack (bare-metal Linux, VM, Windows host) and record the results in `specs/023-hardware-agent-upgrade/quickstart-results.md`
- [ ] T118 Production manual first install of agent 4.4.0 on node-1 and the fleet, then a staged "Upgrade" test with the next patch release (**confirm with the user: production hosts**)

### Release (user-confirmed, in order)

- [ ] T119 Signing key ceremony (**confirm with the user**): `agent-release keygen`, commit the public key to `internal/agentrelease/keys.go`, store the private seed as `AGENT_RELEASE_SIGNING_KEY` in the GitHub environment `release` (required reviewers); re-run T111
- [ ] T120 Release **inventory v4.4.0** and inventory SDK **`sdk/v4.2.0`** — PR, CI (sign job, image with bundle, GitHub release with 8 artifacts + manifest + signature + `SHA256SUMS`), tags (**confirm with the user**)
- [ ] T121 In `go-tangra-ipam-v4/go.mod` replace the temporary `replace` with `github.com/go-tangra/go-tangra-inventory/sdk/v4 v4.2.0`; `go mod tidy`; re-run T112
- [ ] T122 Release **ipam v4.6.0** (and ipam SDK `sdk/v4.2.0` if the proto changed, T061) — PR, CI, tags (**confirm with the user**)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (T001–T006)** → **Foundational (T007–T031)** → user stories.
  T006 (node-1 corpus) only blocks T033's node-1 assertions; the other
  fixtures come from T005.
- Inside Foundational, inventory (T007–T022) and IPAM (T023–T031) run in
  parallel; IPAM normalisation (T029) needs the SDK types (T015, T022)
  through the local `replace` (T003).
- **US1 (T032–T041)** and **US2 (T042–T051)** depend only on Foundational
  and are independent of each other (different agentfacts/collector files;
  both touch `ui/src/views/hosts/detail.vue` and `internal/diff/diff.go` —
  sequence those edits).
- **US3 (T052–T062)** depends on Foundational; meaningful data needs US1
  (and US2 for disks), but the planner/store/UI are testable with fixture
  reports.
- **US4 (T063–T095)** depends on Foundational (proto, migration); it is
  independent of US1–US3 in code, but it is the vehicle that ships them.
- **US5 (T096–T104)** depends on US4 (requests, scheduler base).
- **Polish/Release (T105–T122)**: T119 before T120; T120 before T121 →
  T112 → T122; T116–T118 after the releases.

### User Story Dependencies

- US1 (P1): Foundational.
- US2 (P1): Foundational.
- US3 (P1): Foundational (+ US1/US2 for real data).
- US4 (P1): Foundational.
- US5 (P2): US4.

### Within Each User Story

- Tests first, confirmed failing; pure packages (agentfacts, agentrelease,
  selfupdate, upgrades planners, IPAM hostreport/hostplan) before glue;
  repo before service; service before API; API before UI.
- Inventory tasks can merge ahead of IPAM tasks (the `hardware` field stays
  unused until IPAM applies it).

### Parallel Opportunities

- T001, T002, T004, T005; T007–T014; T023–T026; T016/T017; T028–T030.
- US1 and US2 in parallel (two developers), US3 in parallel in the IPAM
  repo, US4 in parallel on the upgrade path.
- Inside US4: release pipeline (T078–T082), server (T083–T089) and agent
  (T090–T095) tracks after their tests.

---

## Parallel Example: Foundational + P1 stories

```bash
# Tests (all [P], different files):
Task: "Decoder table tests in internal/agentfacts/smbios_test.go"            # US1
Task: "sysfs parser tests in internal/agentfacts/disks_test.go"              # US2
Task: "Planner tests in go-tangra-ipam-v4/internal/hostplan/hardware_test.go" # US3
Task: "agentrelease tests in internal/agentrelease/*_test.go"                # US4
Task: "Upgrade service tests in internal/upgrades/upgrades_test.go"          # US4

# Implementation across tracks:
Task: "DSP0134 decoder in internal/agentfacts/smbios.go"
Task: "Disk parsers in internal/agentfacts/disks.go"
Task: "Sub-planner in go-tangra-ipam-v4/internal/hostplan/hardware.go"
Task: "internal/agentrelease + cmd/agent-release"
```

## Parallel Example: User Story 4

```bash
Task: "Manifest/keyring/verify in internal/agentrelease/"
Task: "Release store in internal/releases/ + internal/repo/repodb/releases.go"
Task: "Upgrade service in internal/upgrades/"
Task: "Selfupdate core in internal/selfupdate/"
Task: "Fleet UI in ui/src/views/agents/index.vue"
```

---

## Implementation Strategy

### MVP First

1. Phase 1 + Phase 2.
2. **US1** (correct SMBIOS) — the wrong values are the most harmful defect.
3. **STOP and VALIDATE**: quickstart 1 with a locally built agent on a
   node-1-class host; corpus test green (SC-001).

### Incremental Delivery

1. Foundation → US1 → US2 (inventory shows complete, correct hardware).
2. US4 (self-upgrade) — required before the rollout so the corrected
   agent reaches every host with one last manual install.
3. Release inventory 4.4.0 (US1, US2, US4 + projection) → manual first
   install → US3 released in ipam 4.6.0 → hardware appears in IPAM.
4. US5 (policy) can ship in 4.4.0 or a later minor; it changes nothing
   while disabled.

### Parallel Team Strategy

- Developer A: agent collection (US1 → US2).
- Developer B: IPAM hardware (Foundational IPAM → US3).
- Developer C: upgrade server + release pipeline (US4 server/pipeline → US5).
- Developer D: agent selfupdate + upgrader + CLI + e2e job (US4 agent).

---

## Notes

- [P] tasks touch different files and have no dependency on an unfinished task.
- Never execute an artifact that has not passed `agentrelease.Verify`; T072
  asserts the installer is untouched on every verification failure.
- Every upgrade transition must appear in `inventory_audit_events`; every
  IPAM hardware change in `ipam_audit_events` — tests assert audit rows
  next to data rows.
- The private signing key never appears in the repository, the image, a
  config file or a log.
- Commit after each task or logical group; stop at any checkpoint to
  validate the story on its own.
