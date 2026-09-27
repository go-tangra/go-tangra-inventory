# Implementation Plan: Hardware Details for Devices and Agent Self-Upgrade

**Branch**: `023-hardware-agent-upgrade` | **Date**: 2026-09-27 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/023-hardware-agent-upgrade/spec.md`

## Summary

The inventory agent decodes SMBIOS enumerations itself from the raw
structures go-smbios exposes (the library's enums are numbered from 0
instead of 1 — the root cause of "LPDDR3/TSOP/Video memory/Multi-bit" on
node-1), reports every memory slot (empty ones included), chassis type,
processor family/socket, and — new — the **physical disks** (Linux sysfs,
Windows `Get-PhysicalDisk`) with filesystems linked to the disks they live
on. A `hardware_schema = 2` marker keeps the wrongly decoded history of
older agents out of IPAM and out of the change log. The inventory host
report gains an optional `hardware` section (SDK `sdk/v4.2.0`) that IPAM's
existing host sync validates, stores per device (`ipam_device_hardware`),
audits field by field and shows in a new **Hardware** tab.

To roll the fixed agent out without touching every host again, agents
upgrade themselves from the inventory module they are enrolled with: the
release pipeline signs an Ed25519 manifest over all eight agent artifacts
(deb/rpm/binary for Linux amd64/arm64, exe for Windows amd64/arm64), the
signed bundle ships inside the inventory image and is persisted in
PostgreSQL, and agents receive a persisted upgrade request over their
command stream, download the artifact over the authenticated ingest edge,
verify it with a key compiled into the agent, install it — deb/rpm through
`dpkg`/`rpm` from a transient systemd unit, other installs by atomic binary
swap — and roll back automatically if the new version does not report
within 5 minutes. Administrators upgrade one agent, a selection or all
outdated agents from a fleet view; an optional per-tenant policy (off by
default) upgrades inside a maintenance window with a concurrency limit and
pauses on the first failure. The agent also gets `update [--check]`.

## Technical Context

**Language/Version**: Go 1.26 (inventory service, agent, release tool;
IPAM), TypeScript/Vue 3 (inventory and IPAM UI remotes)

**Primary Dependencies**: go-tangra/v4 framework, pgx/goose,
`github.com/siderolabs/go-smbios v0.3.4` (existing; raw structures only),
gopsutil v4 (existing), `golang.org/x/sys` (existing; Windows process
flags/SCM), standard library `crypto/ed25519`/`crypto/sha256` (signing and
verification), nfpm v2.47.0 (existing, build-time), inventory SDK
**`sdk/v4.2.0`** (IPAM), `@go-tangra/ui`. **No new third-party
dependency.**

**Storage**: PostgreSQL/TimescaleDB with RLS — inventory migration
**0006** (component columns, agent platform columns, global release tables
with 1 MiB `bytea` chunks, tenant-scoped `inventory_agent_upgrades` and
`inventory_agent_upgrade_policy`); IPAM migration **0009**
(`ipam_device_hardware`). Agent host state in
`/var/lib/inventory-agent/upgrade/` (Windows `%ProgramData%`).

**Testing**: `go test -race` with memstore and fakes (SMBIOS raw-table
corpus incl. node-1, sysfs block trees, PowerShell JSON fixtures, fake
installer/runner/clock/fs for `selfupdate`, fake registry), negative
security tests (tampered/truncated/unsigned/unknown-key/wrong-platform/
wrong-version artifacts, downgrade, foreign request ids, out-of-order
transitions, permissions), fuzz targets for every new parser (SMBIOS
structures, sysfs attributes, Windows disk JSON, manifest, version,
report hardware validation, hardware diff), testcontainers integration
(migrations 0006/0009 on populated databases, release seeding by two
replicas, chunked download through the real ingest gRPC server over
bufconn, upgrade state machine with RLS), contract tests (`buf breaking`,
OpenAPI routes/permissions), vitest (both UIs), a package upgrade/rollback
test in Debian and Rocky containers with systemd (CI job, `//go:build
e2e`).

**Target Platform**: Linux containers (freya-stack) for both modules;
agent on Linux amd64/arm64 (deb, rpm, binary; systemd required for
self-upgrade) and Windows amd64/arm64 (service).

**Project Type**: multi-repo platform change — go-tangra-inventory-v4
(agent, ingest, storage, projection, upgrades, release pipeline, UI, SDK),
go-tangra-ipam-v4 (hardware normalisation, planner, store, API, UI);
production notes for go-tangra-docker.

**Performance Goals**: collection adds ≤ 5 s (disks) and ≤ 1 s (SMBIOS);
95 % of online agents report the new version ≤ 10 min after "Upgrade all"
(SC-004; 20 concurrent downloads × ~10 MB); rollback ≤ 5 min (SC-005);
IPAM apply p95 unchanged (≤ 150 ms/host incl. hardware diff).

**Constraints**: additive protos; forward-only migrations; old agents keep
working (ignore UPGRADE commands, no hardware in reports); auto-upgrade
off by default; agents download only from their inventory (SR-002); the
private signing key never enters the platform runtime (SR-001); coverage
gates — inventory 100 % for `internal/authz`, `internal/sealed`,
`internal/enroll`, `internal/hostreport` + new `internal/agentrelease`,
`internal/selfupdate`, `internal/upgrades`; IPAM 100 % for
`internal/authz`, `internal/sealed`, `internal/ipnet`,
`internal/hostreport`, `internal/hostplan`; ≥ 80 % total in both.

**Scale/Scope**: ≤ 1000 agents per tenant; ≤ 256 disks, 1024 memory
slots, 1024 filesystems per host; 8 artifacts × ~10 MB per release,
≤ 5 versions kept (~400 MB); 2 repos, 6 new inventory packages + 1 tool,
1 new IPAM file set, 2 migrations, 9 inventory + 1 IPAM HTTP operations,
3 new ingest RPCs, 1 new permission.

## Constitution Check

*GATE: checked before Phase 0 and re-checked after Phase 1 design — all PASS.*

- [x] **I. Secure by Default**: automatic upgrades off by default;
      unsigned or unverifiable bundles are refused by the server and the
      agent; downgrades refused unless an administrator pins a listed
      version; dev signing key only under build tag `agentdevkey`
      (CI asserts release binaries do not contain it); agent `upgrade`
      section can disable server-pushed upgrades per host.
- [x] **II. Zero Trust**: agents use only the existing ingest edge with the
      per-agent credential (new RPCs authenticated by the existing
      interceptor, `auth.go:40-44`); download/report bound to the agent's
      own request; admin API behind the gateway with module permissions
      (`agents:manage`, new `agentupgrades:manage`); IPAM ↔ inventory
      unchanged (SPIFFE, `ipam-hostsync` rule, no new RPC).
- [x] **III. Boundary Validation**: proto bounds documented; validation at
      agent, ingest (`validateExtended`), projection and IPAM
      (`internal/hostreport/hardware.go`); strict manifest parser; OpenAPI
      `additionalProperties: false` + body limits; artifact size, chunk,
      stream deadline, concurrency and request-count bounds; DB CHECKs.
- [x] **IV. Test-First**: every phase lists tests first; negative tests
      and fuzz targets explicit (tasks.md); new security-relevant packages
      at 100 %.
- [x] **V. Observability**: every upgrade transition and policy change
      audited transactionally; IPAM hardware changes audited field by
      field; metrics for requests, downloads, verification failures;
      content-free realtime events; no secrets/serials in info logs.
- [x] **VI. Supply Chain**: no new dependency; Ed25519 from the standard
      library (research D7, alternatives cosign/minisign/selfupdate
      rejected); release artifacts signed + `SHA256SUMS`; signing key in a
      protected CI environment; SBOM for agent artifacts; `govulncheck`
      in both repos.
- [x] **VII. Simplicity**: one SMBIOS decoder, one selfupdate core with
      injected OS glue, one upgrade service; typed config sections
      (`agent_releases`, agent `upgrade`); complexity recorded below.
- [x] **Threat Model**: STRIDE in [research.md](research.md#stride-threat-model).

## Project Structure

### Documentation (this feature)

```text
specs/023-hardware-agent-upgrade/
├── spec.md  plan.md  research.md  data-model.md  quickstart.md  tasks.md
├── contracts/{inventory-grpc.md,inventory-http.md,ipam-http.md,audit-events.md,agent-cli.md}
└── checklists/            # requirements.md (from /speckit.specify, if present)
```

### Source Code

```text
go-tangra-inventory-v4 (this repo)
  sdk/api/proto/inventory/v1/inventory.proto (+ generated)      # contracts/inventory-grpc.md
  sdk/pkg/inventoryclient/{hostreport.go,inventory.go}          # SDK v4.2.0 Hardware types
  internal/agentfacts/smbios.go                 # NEW DSP0134 tables + raw field decoding (D1)
  internal/agentfacts/disks.go                  # NEW sysfs block parser, fs→disk resolution, Windows JSON (D3)
  internal/agentfacts/installtype.go            # NEW install type detection (D10)
  internal/agentfacts/testdata/{smbios/,sysblock/,windisks/}
  internal/collector/{hardware.go,disks_linux.go,disks_windows.go,disks_other.go,osinfo.go,collector.go}
  internal/agentrelease/                        # NEW manifest, keyring, verify, version compare (100 %)
  internal/selfupdate/                          # NEW agent upgrade core: stage, verify, lock, state, confirm/rollback (100 %)
  internal/upgrader/{upgrader.go,install_deb.go,install_rpm.go,install_binary.go,
                     systemd_linux.go,service_windows.go,detach_windows.go,*_other.go}   # NEW OS glue (excluded like collector)
  internal/daemon/daemon.go                     # platform on StreamRequest, UPGRADE command, confirm on start
  cmd/inventory-agent/{main.go,update.go,apply.go}   # update [-check], upgrade-apply
  cmd/agent-release/main.go                     # NEW keygen/sign/verify (release tooling)
  internal/config/config.go                     # agent_releases section; AgentConfig upgrade + collect_disks
  internal/store/models.go
  internal/store/migrations/0006_hardware_upgrades.sql
  internal/ingest/{ingest.go,validate.go,upgrade.go}   # validation of new fields; CheckAgentUpdate/Download/Report
  internal/invpb/                               # new proto mappers (hardware, filesystems)
  internal/sender/mapper.go, internal/grpcapi/mapper.go
  internal/hostreport/hostreport.go             # hardware section + D2 gate
  internal/diff/diff.go                         # disk key, schema transition suppression, new fields
  internal/repo/repo.go, internal/repo/repodb/{db.go,releases.go,upgrades.go}, internal/memstore/{memstore.go,releases.go,upgrades.go}
  internal/releases/                            # NEW bundle verification + DB seeding, import, retention, artifact reader
  internal/upgrades/                            # NEW requests, delivery, reports, sweepers, fleet view, policy + scheduler (100 %)
  internal/audit/audit.go                       # vocabulary, subject kinds
  internal/events/                              # agent.upgrade events
  internal/httpapi/{handlers.go,upgrades.go,deps.go}
  internal/app/app.go                           # wiring, workers, import subcommand
  cmd/inventorysvc/main.go                      # `agent-release import`
  api/openapi/inventory.yaml, pkg/inventorymanifest/manifest.go
  ui/src/views/hosts/detail.vue, ui/src/views/agents/index.vue, ui/src/stores/agents.ts, ui/src/api/types.ts
  packaging/nfpm.yaml (unchanged contents), Makefile, Dockerfile, agent-releases/.gitkeep
  .github/workflows/ci.yaml                     # artifacts for 8 platforms, sign job, bundle into image, release upload, e2e upgrade job
  scripts/coverage-gate.sh, README.md, deploy/README.md, SECURITY.md (key rotation note)

go-tangra-ipam-v4
  go-tangra-ipam-v4/go.mod                                      # inventory SDK v4.2.0 (temporary replace during development)
  go-tangra-ipam-v4/internal/hostreport/{normalize.go,hardware.go}   # Hardware normalisation (100 %)
  go-tangra-ipam-v4/internal/hostplan/{plan.go,hardware.go}          # OpReplaceHardware + field diff (100 %)
  go-tangra-ipam-v4/internal/hostsync/apply.go
  go-tangra-ipam-v4/internal/store/models.go
  go-tangra-ipam-v4/internal/store/migrations/0009_device_hardware.sql
  go-tangra-ipam-v4/internal/repo/repo.go, go-tangra-ipam-v4/internal/repo/repodb/{db.go,hostsync.go}, go-tangra-ipam-v4/internal/memstore/{memstore.go,hostsync.go}
  go-tangra-ipam-v4/internal/audit/audit.go
  go-tangra-ipam-v4/internal/httpapi/{handlers.go,hardware.go}, go-tangra-ipam-v4/internal/devices/devices.go
  go-tangra-ipam-v4/internal/grpcapi/mapper.go, go-tangra-ipam-v4/sdk/api/proto/ipam/v1/ipam.proto
  go-tangra-ipam-v4/api/openapi/ipam.yaml
  go-tangra-ipam-v4/ui/src/views/devices/{detail.vue,index.vue}, go-tangra-ipam-v4/ui/src/components/HardwarePanel.vue,
  go-tangra-ipam-v4/ui/src/api/{types.ts,schema.d.ts}
  go-tangra-ipam-v4/README.md
```

**Structure Decision**: inventory owns collection, the host-report
contract, releases and the upgrade lifecycle; IPAM only stores and shows
reported hardware. Security-relevant logic lives in pure packages
(`agentrelease`, `selfupdate`, `upgrades`, `agentfacts`, IPAM
`hostreport`/`hostplan`) proven by unit + fuzz tests at 100 %; OS side
effects (dpkg/rpm/systemd-run/SCM) sit behind interfaces in
`internal/upgrader` and are covered by the container e2e job.

## Rollout

1. **inventory v4.4.0** + inventory SDK **`sdk/v4.2.0`**: proto, SMBIOS
   decoder, disks, migration 0006, projection hardware, releases and
   upgrades, UI; CI signs the agent release and bundles it in the image;
   GitHub release carries all 8 artifacts + manifest + signature +
   `SHA256SUMS`. Prerequisite (user): Ed25519 key pair generated, public
   key committed, private seed in the `release` environment secret.
2. Deploy inventory 4.4.0 (the image seeds release 4.4.0 into the DB).
3. **One manual install of agent 4.4.0 per host** (`dpkg -i` / `rpm -U` /
   Windows exe + `-service install`); agents < 4.4.0 show "manual upgrade
   required". Test "Upgrade" on one host with a later patch release
   (4.4.1) before "Upgrade all".
4. **ipam v4.6.0** (inventory SDK v4.2.0, migration 0009, hardware apply,
   Hardware tab). Hardware appears once the host's agent is ≥ 4.4.0.
5. **go-tangra-docker** (user): pin inventory 4.4.0 and ipam 4.6.0;
   optional `agent_releases` config; volume/DB size note (~400 MB for five
   releases).

Tags, merges and pins are user-confirmed steps (tasks.md Phase 8).

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Own SMBIOS field decoder next to go-smbios | go-smbios enums are shifted (F1/F2) and lack chassis/processor fields | Patching upstream is slow and still leaves missing fields; `dmidecode` is not everywhere and has no Windows build |
| `hardware_schema` marker + diff suppression | Wrong historical strings must not reach IPAM or flood the change log (F4) | Version-string gating breaks for dev/describe builds |
| Agent releases stored in PostgreSQL (chunked `bytea`) | Previous versions needed as rollback packages across replicas and image upgrades; offline sites | Image-only: loses old versions on upgrade; GitHub fetch: egress/offline; object store: new infrastructure |
| Transient systemd unit running a staged helper | The package postinstall restarts the service and would kill an in-cgroup `dpkg` (F6) | In-process install corrupts the package state; shipping path units needs an agent that already has them |
| Persisted upgrade requests + delivery on connect | Registry delivery is fire-and-forget (F5); offline agents must upgrade on reconnect (FR-011) | Pub/sub only loses requests |
| New permission `agentupgrades:manage` | Policy and version pins (incl. downgrades) are fleet-wide code changes; `agents:manage` is held by operators (F9) | Reusing `agents:manage` lets operators downgrade every host |
| Custom signed manifest (Ed25519) instead of a signing ecosystem | Offline verification with the standard library only (Constitution VI) | cosign/sigstore: heavy dependency tree, online transparency log; minisign: extra dependency |
