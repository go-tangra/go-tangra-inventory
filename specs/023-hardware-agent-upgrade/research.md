# Research: Hardware Details for Devices and Agent Self-Upgrade (023)

Evidence from go-tangra-inventory-v4 (branch `023-hardware-agent-upgrade`,
service v4.3.1, inventory SDK `sdk/v4.1.0`), go-tangra-ipam-v4 (`main`,
v4.5.0, depends on inventory SDK v4.1.0), the v3 client go-tangra-client
(`internal/updater`, `internal/machine`), `github.com/siderolabs/go-smbios
v0.3.4` (module cache), and the production check of host node-1 on
2026-09-27. Paths without a prefix are in go-tangra-inventory-v4; ipam paths
are prefixed `go-tangra-ipam-v4/…`.

## Current state

### Hardware collection (agent)

- `internal/collector/hardware.go:29-35` opens SMBIOS with
  `smbios.New()` (go-smbios v0.3.4, `go.mod:18`) and maps it in the pure
  `mapHardware` (`hardware.go:39-92`). Enumerated values are produced by the
  library's `String()` methods: `WakeUpType.String()` (`:52`),
  `BoardType.String()` (`:63`), `pma.Location/Use/MemoryErrorCorrection
  .String()` (`:133-135`), `d.FormFactor.String()` and
  `d.MemoryType.String()` (`:152-153`). `ChassisInfo.Type` exists in the
  model (`internal/store/models.go` `ChassisInfo.Type`) and the proto
  (`ChassisInfo.type = 6`) but is **never set** — go-smbios does not parse the
  enclosure type byte (`system_enclosure.go:25-33` only reads strings).
  Processor family/type/upgrade are not parsed by the library at all
  (`processor_information.go:51-62`).
- `mapMemory` (`hardware.go:129-163`) **skips empty slots** (`capBytes == 0
  → continue`, `:144-146`), keeps only the **last** Physical Memory Array
  (go-smbios exposes one `PhysicalMemoryArray`), and has no type detail
  (registered/unbuffered).
- Disks: `collectDisks` (`internal/collector/osinfo.go:94-120`) groups
  gopsutil **mounted partitions** by device path into `store.Disk` values;
  `Disk.Model/Serial/SizeBytes/MediaType/Interface`
  (`models.go` `type Disk`) stay empty. There is no physical-disk
  enumeration on any platform. Windows uses PowerShell for software/users
  already (`internal/collector/software_windows.go:122,143`).
- Collection has per-area bounds for network/guests/updates (`models.go`
  `MaxInterfaces … MaxBmcPorts`) but none for disks, memory slots or
  processors.

### Inventory module

- Storage: snapshot `payload jsonb` is authoritative; component tables
  `inventory_processors`, `inventory_memory_modules`, `inventory_disks`
  (`internal/store/migrations/0003_components.sql:9-60`) are filled on ingest
  by `insertComponents` (`internal/repo/repodb/db.go:423-455`). RLS covers
  them (`0004_rls.sql:13-14`). Next migration: **0006**.
- Diff: disks keyed by `Serial` (`internal/diff/diff.go:71-72`) — with no
  serials today every disk has key `""`. Statistics sum `Disk.SizeBytes`
  (`db.go:946-947`), so `total_disk_bytes` is always 0 today.
- Host report projection `internal/hostreport/hostreport.go:18-50`
  (`Project`) + `Digest` (`:67-78`, sha256 of the deterministic encoding
  without snapshot id/times) — no hardware. `HostReport` fields 1–17
  (`sdk/api/proto/inventory/v1/inventory.proto:668-686`).
- Proto highest field numbers: `Inventory` 30, `MemoryInfo` 3,
  `MemoryModule` 10, `Processor` 11, `Disk` 6, `CollectionLimits` 5,
  `HostReport` 17, `StreamRequest` 2, `Command` 2, `CommandType` 1
  (REFRESH). `buf breaking` runs against `sdk/v4.0.0` in CI
  (`.github/workflows/ci.yaml:112-114`).

### Agent runtime and packaging

- `cmd/inventory-agent/main.go` is flag-only (no subcommands,
  `:29-39`); `version` stamped by ldflags (`:24`); modes one-shot,
  `-daemon`, `-service install|uninstall` (Windows SCM via
  `internal/winsvc`).
- `internal/daemon/daemon.go`: enroll-once, periodic submit, reconnecting
  `StreamCommands` (`:156-193`); unknown command types are logged and
  ignored (`:189-190`) — **old agents safely ignore a new command type**.
  `StreamRequest` carries only `agent_id`, `agent_version` (`:164-167`).
- Ingest edge: every method except `Enroll` requires the per-agent
  credential (`internal/ingest/auth.go:40-44`, interceptors `:79-112`) — new
  RPCs are authenticated by default. Tenant always from the verified agent
  (`internal/ingest/ingest.go:8-13`). `StreamCommands` registers the
  connection and forwards registry commands (`ingest.go:146-187`).
- Registry `Deliver` is fire-and-forget: `delivered=false` when the agent is
  offline or its 16-slot buffer is full (`internal/registry/registry.go:20-22,
  45-47, 102-117`); Valkey variant uses pub/sub (`registry_valkey.go:36,134`).
  **Nothing is persisted**, so offline agents never receive a command.
- `GET /api/inventory/v1/agents` lists only **live connections** from the
  registry (`internal/httpapi/handlers.go:184-198`); offline enrolled agents
  (table `inventory_agents`, `0001_schema.sql:40-52`, model
  `models.go` `type Agent`) are invisible in the agent list.
- systemd unit `packaging/inventory-agent.service`: `User=root`,
  `Restart=on-failure`, `NoNewPrivileges=yes`, `ProtectHome=yes`,
  `PrivateTmp=yes`, no `ProtectSystem` (so `/usr/bin` is writable by the
  agent). deb/rpm `postinstall.sh` runs `systemctl try-restart
  inventory-agent.service` on upgrade — **a package install started from
  inside the agent's own cgroup would kill the installer with the agent**.
- nfpm package `tangra-inventory-agent` (`packaging/nfpm.yaml`), no
  dependencies, `agent.yaml` is `config|noreplace`. `make packages`
  (`Makefile:83-93`, nfpm v2.47.0) builds Linux amd64/arm64 deb+rpm with
  `AGENT_LDFLAGS -X main.version` (`Makefile:76`). `agent-windows`
  (`Makefile:70-72`) builds **without** the version ldflag.
- CI `packages` job (`ci.yaml:148-195`) builds deb/rpm; `release`
  (`ci.yaml:199-222`) uploads them + `SHA256SUMS` to the GitHub release of a
  `v*` tag. **Windows binaries and raw Linux binaries are not published**;
  nothing is signed. The repository is public.
- Docker image (`Dockerfile`) contains only `inventorysvc` + `deploy/`.
- Coverage: `COVERPKG` excludes `internal/collector`, `internal/sender`,
  `internal/daemon`, `internal/winsvc` (`Makefile:27`);
  `SECURITY_PKGS` = authz, sealed, enroll, hostreport
  (`scripts/coverage-gate.sh:9`).

### Permissions and audit (inventory)

- `agents:manage` "Enroll, refresh, list and revoke endpoint agents"
  (`pkg/inventorymanifest/manifest.go:42`), granted to owner, admin,
  **operator** (`:55`) and the module role administrator (`:159`); CASL
  `{manage, InventoryAgent}` (`:66`); nav "Agents" (`:73`).
- Audit: closed vocabulary (`internal/audit/audit.go:22-36`), subject kinds
  host/snapshot/agent/token/backup/system (`:40-46`), actor kinds incl.
  `agent` and `system` (`:57-61`); buffered writer (`:186-240`) that drops on
  a full queue.

### IPAM (go-tangra-ipam-v4)

- Host sync from feature 020: normalisation
  `go-tangra-ipam-v4/internal/hostreport/normalize.go` (`Report` struct
  `:130-157`), pure planner `go-tangra-ipam-v4/internal/hostplan`
  (`Build` `plan.go:118`, op kinds `plan.go:16-33`, packages sub-planner
  `updates.go:16-64` — the replace-and-audit pattern to copy), executor
  `go-tangra-ipam-v4/internal/hostsync/apply.go:95-119`, transactional
  `HostTx` (`go-tangra-ipam-v4/internal/repo/repo.go:241-262`), audit
  vocabulary `go-tangra-ipam-v4/internal/audit/audit.go:70,155`.
- Devices keep `manufacturer/model/serial_number/firmware_version`
  (`go-tangra-ipam-v4/internal/store/models.go:277-322`);
  `firmware_version` is an administrator field (020 D8) and stays untouched.
- Next migration **0009** (`0001…0008` present). Device page
  `go-tangra-ipam-v4/ui/src/views/devices/detail.vue` tabs defined at
  `:134`, rendered `:198-225`; unit tests in
  `go-tangra-ipam-v4/ui/tests/unit/views.spec.ts`.
- Coverage gate 100 % for `internal/hostreport`, `internal/hostplan`.

### v3 reference (go-tangra-client)

- Update: `internal/updater/updater.go` — GitHub "latest release"
  (`:21,112-150`), `SHA256SUMS`-style checksum file (`:214-249`),
  `minio/selfupdate v0.6.0` apply with rollback (`:196-212`), version
  compare (`:251`), environment detection (`:290`);
  `internal/updater/executor_source.go` (server-pushed variant via the
  executor); `cmd/update/update.go` (`update --check`);
  `internal/executor/update_handler.go`.
- Host data: `internal/machine/machine.go` `getMemoryInfo` (`:357`),
  `getDisks` (`:511-560`, `/sys/block` without loop/ram/dm, size,
  `queue/rotational`, `device/model`), `getBoardInfo` (`:634`).

## Findings that change the plan

- **F1 — Root cause of the shifted SMBIOS values** is go-smbios v0.3.4, not
  our mapper: the typed enums are declared with `iota` starting at **0**
  (`smbios/memory_device.go:337` `MemoryTypeOther MemoryType = iota`,
  `:245` `FormFactorOther FormFactor = iota`,
  `physical_memory_array.go:76,146,188`, `baseboard_information.go:51`
  `BoardTypeUnknown = iota`) while DSP0134 codes start at **1** (Other=01h,
  Unknown=02h). The memory-type list additionally has one `Reserved` entry
  where the spec has three (15h–17h), so DDR4 (1Ah = 26) decodes as index 26
  = `LPDDR3` (shift 3); form factor DIMM (09h) decodes as `TSOP`; array
  location System board (03h) as `ISA add-on card`; use System memory (03h)
  as `Video memory`; ECC Single-bit (05h) as `Multi-bit`. `WakeUpType` is
  correct (`system_information.go:67`, Reserved = 0). Exactly the node-1
  symptoms.
- **F2 — Type detail is wrong too**: `TypeDetail(GetByte(s, 0x13))`
  (`memory_device.go:150`) reads one byte of a WORD field and maps bits by
  reversed iota (`:484-518, 561-571`); "Registered" (bit 13) and
  "Unbuffered" (bit 14) can never appear. Chassis type (offset 05h) and
  processor family/type/upgrade (06h/05h/19h, family 2 at 28h) are not
  decoded by the library.
- **F3 — The raw structures are available**: `SMBIOS.Structures`
  (`smbios.go:27`) exposes every structure's `Header.Type`, `Formatted`
  bytes and `Strings` (internal type, exported fields), and
  `smbios.Decode(io.Reader, Version)` (`smbios.go:62`) decodes a raw DMI
  table — so the agent can keep go-smbios for entry-point discovery and
  structure splitting and decode fields itself from a test corpus of raw
  tables.
- **F4 — Old snapshots carry wrong strings, not codes**: payloads store the
  decoded text (`MemoryModule.memory_type` etc.), so historical data cannot
  be repaired; hardware from agents < 023 must not reach IPAM, and the first
  corrected report would otherwise create a flood of "modified" change
  records.
- **F5 — No persistence for agent commands** (registry is pub/sub) and **no
  agent → server status channel** beyond `SubmitInventory`; upgrade requests
  must be persisted and delivered on (re)connect, and the agent needs an RPC
  to report progress.
- **F6 — The package postinstall restarts the service**; an installer must
  run outside the agent's cgroup (transient systemd unit), otherwise
  `dpkg`/`rpm` is killed mid-transaction.
- **F7 — Release artifacts are incomplete for self-upgrade**: no Windows or
  raw Linux binaries on the release, Windows builds lack the version stamp,
  nothing is signed, and the service image has no agent artifacts.
- **F8 — The agent list cannot show offline agents**, so "upgrade all" and
  "manual upgrade required" need a fleet view from `inventory_agents` merged
  with registry liveness.
- **F9 — `agents:manage` is granted to operator**; routine upgrades to the
  platform's current version fit it, but auto-upgrade policy and pinning
  another (possibly older) version are administrator decisions (SR-003).
- **F10 — The digest includes `agent_version`** (projection field 7), so
  every upgrade already re-applies the host in IPAM once; adding the
  hardware section changes all digests once (one extra apply per host on
  rollout; bounded by the 020 pacing).

## Decisions

### D1 — Own SMBIOS field decoding over go-smbios raw structures

**Decision**: new pure decoder `internal/agentfacts/smbios.go` with the
DSP0134 3.7 code tables (memory type 01h–24h incl. reserved codes, form
factor 01h–10h, type detail bit set, memory array location/use/ECC, chassis
type 01h–24h, processor type/family (incl. family 2 via 28h when 06h =
FEh)/upgrade, board type 01h–0Dh, wake-up type) and functions that read
fields from `Structure.Formatted` (offsets relative to the structure start;
`Formatted` begins at offset 04h) with version-aware length checks
(a field beyond `Header.Length` = not available). Unknown codes →
`"Unknown (code N)"`; out-of-range/short structures never panic. Every type 16
array and every type 17 device (including empty slots) is decoded; the
array of use "System memory" is the primary one; memory totals count only
arrays of that use. go-smbios stays (v0.3.4) for table discovery on Linux
(`/sys/firmware/dmi/tables`) and Windows (`GetSystemFirmwareTable`) and for
structure splitting; its typed enums and `String()` methods are no longer
used. `collector/hardware.go` maps via the new decoder.

**Rationale**: fixes F1/F2 at the root, testable with a corpus of raw
tables (SC-001), no new dependency. **Alternatives**: patch go-smbios
upstream (slow, and still iota-fragile; a PR is offered as a follow-up
only); replace with `github.com/digitalocean/go-smbios` (same raw layer, no
field decoding); shell out to `dmidecode` (not installed everywhere, parsing
text, Windows missing).

### D2 — Hardware schema version gate

**Decision**: new `Inventory.hardware_schema` (uint32, field 33); agents
from this feature send `2` (corrected SMBIOS + physical disks). The
projection includes the `hardware` section only when `hardware_schema ≥ 2`;
the inventory host page shows older snapshots with a notice "collected by
an agent with known decoding errors — upgrade the agent"; the diff
suppresses hardware categories (memory, disk, chassis, processor, bios)
when the previous snapshot has `hardware_schema < 2` and the next has ≥ 2,
recording one change `hardware_schema: 1 → 2` instead.

**Rationale**: F4 — wrong values must never reach IPAM, and history should
not be flooded. **Alternative**: gate on agent version strings (fragile for
dev builds).

### D3 — Physical disks

**Decision** (pure parsers in `internal/agentfacts/disks.go`, collectors
`internal/collector/disks_linux.go`, `disks_windows.go`, `disks_other.go`):

- **Linux** from sysfs `/sys/block/*` (read through an `fs.FS` rooted at `/`
  so tests use `testdata/sysblock/*` trees): exclude `loop*`, `ram*`,
  `zram*`, `dm-*`, `md*`, `nbd*`, `sr*`, `fd*`, `drbd*`, `rbd*`,
  `mmcblk*boot*` and devices with size 0; size = `size` × 512; model from
  `device/model` (NVMe: `device/model` of the controller), vendor prefix
  from `device/vendor` when the model lacks it; serial from `device/serial`
  (NVMe/virtio), else `device/vpd_pg80` (bytes 4.. of page 80h), else the
  udev database `/run/udev/data/b<major>:<minor>` `E:ID_SERIAL_SHORT=` when
  present; interface from the resolved device path (`/nvme/` → nvme,
  `/usb` → usb, `/virtio` → virtio, `/ata` → sata, `sas_` or
  `/end_device-` → sas, `/vmbus`/`storvsc` → hyperv, else scsi); media =
  nvme_ssd for NVMe, else `queue/rotational` 0 → ssd, 1 → hdd, and
  `unknown` for virtual transports (virtio, hyperv, xen) unless rotational
  is 0; removable from `removable` or usb transport. Bounded: ≤ 256 disks,
  sysfs reads ≤ 4 KiB each, whole disk collection ≤ 5 s.
- **Filesystems → disks**: mounted filesystems (gopsutil, as today) resolve
  their device via `/sys/class/block/<dev>` → partition parent, and for
  `dm-*`/`md*` recursively through `slaves/` (depth ≤ 8) to physical disks;
  a filesystem lists **all** disks it lives on (LVM/RAID spanning).
- **Windows** `Get-PhysicalDisk | Select-Object DeviceId, FriendlyName,
  SerialNumber, Size, MediaType, BusType | ConvertTo-Json -Compress` with a
  30 s timeout (fallback `Get-CimInstance Win32_DiskDrive` for systems
  without the Storage module), pure JSON parser (`FuzzWindowsDisks`);
  filesystems from gopsutil drive letters mapped via
  `Get-Partition | Select DiskNumber, DriveLetter`.
- **Model**: new `Disk` fields `name` (kernel/OS name: `sda`, `nvme0n1`,
  `PhysicalDrive0`), `removable`; a new top-level
  `Inventory.filesystems` (mount, fs, device, size, free, disks[]) replaces
  `Disk.partitions` for new agents (the field stays, documented as legacy;
  projection converts legacy partition groups into filesystems without
  disk links).
- Media values closed set `ssd|hdd|nvme_ssd|unknown`; interface closed set
  `nvme|sata|sas|scsi|usb|virtio|hyperv|xen|mmc|other`.

**Rationale**: v3 parity (`machine.go:511-560`) plus FR-004 fields without
new dependencies. **Alternatives**: `lsblk -J` (not on minimal images, text
contract of util-linux), udev library (cgo), ghw (`github.com/jaypipes/ghw`
— large dependency tree incl. YAML/PCI databases).

### D4 — Hardware in the host report (inventory → IPAM contract)

**Decision**: additive `HostReport.hardware = 18` of new message
`HardwareProfile` reusing existing messages (BIOSInfo, SystemInfo,
BaseboardInfo, ChassisInfo, Processor, MemoryInfo, Disk) plus
`Filesystem` and `HardwareAvailability` (smbios/disks:
`ok|partial|unavailable|unsupported`). `CollectionLimits` gains `disks`,
`memory_slots`, `filesystems`, `processors`. The projection
(`internal/hostreport`) fills it when D2 allows; the digest covers it.
Inventory SDK **`sdk/v4.2.0`** exposes plain-Go `Hardware` types on
`HostReport`.

**Rationale**: IPAM already pulls the projection; one contract, one
digest. **Alternative**: IPAM calls `GetLatestByHost` for hardware (moves
whole payloads, second change signal).

### D5 — Validation and bounds (inventory ingest and IPAM)

- Ingest `validateExtended` (`internal/ingest/validate.go`) adds: ≤ 256
  disks, ≤ 1024 memory slots (modules), ≤ 64 memory arrays, ≤ 256
  processors, ≤ 1024 filesystems, ≤ 64 disk refs per filesystem; strings
  clipped to 256 bytes, control characters removed, media/interface/
  availability closed sets (unknown → `other`/`unknown`); excess counted in
  `truncated`.
- IPAM `internal/hostreport/hardware.go` re-validates with the same bounds
  and a 256 KiB bound on the encoded profile; serials are ordinary data
  (SR-005).

### D6 — IPAM storage, planning and audit of hardware

**Decision**: migration `0009_device_hardware.sql` — table
`ipam_device_hardware` (one row per device: `profile jsonb`, `digest`,
lifted summary columns `cpu_model`, `cpu_sockets`, `cpu_cores`,
`cpu_threads`, `memory_total_bytes`, `memory_type`, `memory_slots_total`,
`memory_slots_used`, `disk_count`, `disk_total_bytes`, `reported_at`) with
RLS. Pure sub-planner `internal/hostplan/hardware.go`: when the report has
a hardware section and its normalised digest differs, emit
`OpReplaceHardware` + one `hardware_updated` audit row whose `changes` list
field-level differences (`bios.version`, `bios.release_date`,
`system.serial_number`, `memory.total_bytes`, `memory.slot[<locator>]`
added/removed/changed, `disk[<serial|name>]` added/removed/changed,
`processor[<socket>]` …; ≤ 100 entries + `changes_truncated`). First
hardware → `hardware_reported` (no field list, summary only). A report
**without** hardware (older inventory, old agent) leaves stored hardware
untouched (absence ≠ removal) and records nothing. Users cannot write
hardware (no write endpoint; not in device create/update). The device JSON
gains a read-only `hardware_summary`; `GET /devices/{id}/hardware` returns
the profile.

**Rationale**: reported data replaced per report (FR-008) with a pure,
fuzzable diff (100 % gate); jsonb avoids five child tables for data only
displayed as a whole. **Alternatives**: normalised tables per module/disk
(queryable but heavy, not needed by the spec); storing in device columns
(too many).

### D7 — Release artifacts, manifest and signature

**Decision**:

- Per version the release carries 8 artifacts: `deb` and `rpm` for linux
  amd64/arm64, raw `binary` for linux amd64/arm64 (non-package installs),
  `binary` `.exe` for windows amd64/arm64 — all built with
  `-trimpath -ldflags "-s -w -X main.version=<v>"`, `CGO_ENABLED=0`.
- **Signed manifest** `agent-release.json` (canonical JSON: `schema: 1`,
  `version`, `created_at`, `key_id`, `artifacts[]` of `{os, arch,
  install_type, file, size, sha256}`) and `agent-release.json.sig` =
  **Ed25519** signature (`crypto/ed25519`, standard library) over the exact
  manifest bytes, base64. The agent verifies signature → parses manifest →
  checks `version` = requested, the entry for its own
  `os/arch/install_type`, then size and sha256 of the streamed artifact.
- Keyring compiled into the agent and the server
  (`internal/agentrelease/keys.go`: `key_id → public key`, ≥ 1 active key,
  room for rotation). A dev key exists only under build tag `agentdevkey`
  (tests and freya-stack dev builds); release builds never use the tag
  (CI asserts the tag is absent and the dev key id is not in the binary).
- Signing tool `cmd/agent-release` (`keygen`, `sign`, `verify`) — standard
  library only; the private key (Ed25519 seed) lives only as the GitHub
  Actions secret `AGENT_RELEASE_SIGNING_KEY` in a protected `release`
  environment used by one CI job; it never enters the image or the
  inventory runtime (SR-001).

**Rationale** (Supply Chain, Constitution VI): crypto from the standard
library only, no new dependency, offline verification, `govulncheck`
unchanged. **Alternatives rejected**: cosign/sigstore keyless (hundreds of
transitive modules, online Fulcio/Rekor verification impossible on offline
sites); minisign format (`aead.dev/minisign`, pulled in by
`minio/selfupdate`; extra dependency for no gain); GPG-signed packages (key
management on every host, no Windows story); checksum only (does not stop
a compromised inventory or release bucket).

### D8 — How inventory obtains and stores releases

**Decision**: releases are **bundled in the inventory image** and
**persisted in PostgreSQL**:

- CI: the `packages` job builds all 8 artifacts; a new `sign` job (tags
  only, `release` environment) produces the manifest + signature; the
  `docker` job copies the signed bundle into the build context
  (`agent-releases/<version>/`) and the Dockerfile copies it to
  `/app/agent-releases/`; the `release` job uploads the same files to the
  GitHub release (plus `SHA256SUMS`). Non-tag images carry no bundle.
- At startup `internal/releases` verifies each bundled release with the
  compiled keyring (unsigned or invalid bundles are refused and logged) and
  upserts it into global tables `inventory_agent_releases`,
  `inventory_agent_artifacts`, `inventory_agent_artifact_chunks` (1 MiB
  `bytea` chunks) — idempotent by version + manifest digest, advisory lock
  for concurrent replicas.
- Offline sites / other versions: `inventorysvc agent-release import
  <dir>` (operator CLI on the server, like `authsvc reset-user`) imports a
  signed bundle downloaded from the GitHub release; same verification.
- Retention: the newest `agent_releases.keep_versions` (default 5)
  versions are kept, never deleting a version that is the platform current
  version, a tenant pin or the target of an active request.
- The platform **current agent version** = the newest bundled version of
  the running inventory (normally its own version); a tenant may pin
  another stored version (D12).

**Rationale**: no egress from the inventory container, works offline, the
agent version matches the server release, all replicas and later images
keep previous versions (needed as rollback packages, D10). Image grows by
~70 MB. **Alternatives rejected**: fetch from the GitHub release at startup
(egress + rate limits, fails offline — kept only as the manual import
path); RustFS/S3 (inventory has no object store today; a new
infrastructure dependency for ≤ 400 MB); local volume (not shared between
replicas).

### D9 — Agent ↔ inventory upgrade protocol (ingest edge only)

**Decision** (all on `IngestService`, authenticated by the per-agent
credential, tenant from the verified agent):

- `StreamRequest` gains `platform {os, arch, install_type}` (fields 3) and
  `capabilities` (4, e.g. `upgrade.v1`); stored on `inventory_agents`
  (`os`, `arch`, `install_type`, `capabilities`). Agents without
  `upgrade.v1` → state `manual_upgrade_required`.
- `CommandType` gains `COMMAND_TYPE_UPGRADE = 2`; `Command` gains
  `UpgradeCommand upgrade = 3` `{request_id, target_version,
  allow_downgrade}`. Old agents ignore it (F: `daemon.go:189-190`).
- `CheckAgentUpdate` (unary): current version + platform → `{available,
  target_version, request_id}`; with `apply=true` (CLI `update`) it
  creates a request with actor `agent` (audited) when an upgrade is
  available and none is active.
- `DownloadAgentRelease` (server stream): `{request_id, version}` → first
  message `ReleaseHeader {manifest, signature, key_id, artifact entry}`,
  then `ArtifactChunk {offset, data ≤ 1 MiB}`. Allowed only for the target
  version of the agent's active request, or for the agent's **current**
  version (rollback package); one concurrent download per agent, global cap
  `agent_releases.max_concurrent_downloads` (default 20, then
  `ResourceExhausted`), 10 min stream deadline.
- `ReportUpgrade` (unary): `{request_id, state, from_version, to_version,
  reason}`; states `downloading|installing|succeeded|failed|rolled_back`;
  reason is a closed code set (`signature_invalid`, `checksum_mismatch`,
  `platform_mismatch`, `downgrade_refused`, `disk_full`, `install_failed`,
  `start_timeout`, `unsupported_install`, `busy`, `download_failed`); the
  server accepts only transitions valid for that request and agent.
- Server delivers a request by `registry.Deliver` when created and again on
  every `StreamCommands` connect for requests in `pending|delivered` (F5);
  the agent deduplicates by `request_id` and by its local upgrade lock.

**Rationale**: SR-002 (only the existing authenticated channel), no new
listener, no HTTP download path. **Alternative**: HTTPS download URL with a
token (second auth path, proxy/CA issues).

### D10 — Installation, restart and rollback on the host

**Decision**: pure core `internal/selfupdate` (state machine, staging,
verification via `internal/agentrelease`, lock and state file, confirm /
rollback decision) with injected `Installer`, `FS`, `Clock`,
`Runner`; OS glue in `internal/upgrader` (excluded from coverage like
`internal/collector`).

1. Stage into `/var/lib/inventory-agent/upgrade/<version>/` (0700;
   Windows `%ProgramData%\go-tangra\inventory-agent\upgrade`), streaming
   sha256, max artifact size 150 MiB, free-space check (2× size) before
   download; verify **before** anything is replaced (SR-004).
2. Refuse lower versions unless `allow_downgrade` is set by the server
   (only for administrator pins, D12) and never below the compiled floor
   (the first self-upgrading version) (SR-003).
3. Install type detected at start (`internal/agentfacts/installtype.go`,
   pure over command results): executable is `/usr/bin/inventory-agent`
   and `dpkg-query -W -f='${Status}' tangra-inventory-agent` says
   installed → `deb`; `rpm -q tangra-inventory-agent` → `rpm`; Windows →
   `binary` (service `FreyaInventoryAgent`); otherwise `binary`.
4. Linux: the agent copies its own executable to the staging dir and
   starts it as a **transient unit** `systemd-run --unit
   inventory-agent-upgrade-<request8> --collect --property=Type=exec
   <staged-helper> upgrade-apply --state <file>` so the installer survives
   the service restart (F6). The helper runs `dpkg -i --force-confold
   <deb>` (env `DEBIAN_FRONTEND=noninteractive`) or `rpm -U --replacepkgs
   <rpm>`; the package's postinstall restarts the service. `binary`
   installs rename the current file to `.prev`, rename the staged binary
   into place (same filesystem, atomic) and `systemctl restart`.
   Without systemd → `unsupported_install`.
   `dpkg`/`rpm` directly, not apt/dnf: the artifact is local and verified,
   the package has no dependencies, and apt/dnf would touch repository
   metadata and the network. A busy dpkg lock is retried 3× over 60 s.
5. Windows: the agent starts the staged helper detached
   (`DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP |
   CREATE_BREAKAWAY_FROM_JOB`); the helper stops the service via SCM,
   renames `inventory-agent.exe` → `.prev`, moves the new exe in, starts
   the service. No `minio/selfupdate`: the needed rename logic is ~100 lines
   of standard library + `golang.org/x/sys/windows` (already a dependency).
6. Confirmation: the new agent, on start, finds the state file with
   `to = own version`, calls `ReportUpgrade(succeeded)` after a successful
   `StreamCommands` connect and the first `SubmitInventory`, then writes
   `confirmed`. The helper waits ≤ `upgrade_confirm_timeout`
   (default 5 min, FR-013); on timeout or install failure it rolls back:
   deb/rpm reinstall the **previous package** downloaded before the
   install (current version from inventory, D9; `dpkg -i` /
   `rpm -U --oldpackage`), otherwise restore the `.prev` binary (reason
   `package_db_mismatch` when a package install had to fall back to a
   binary restore); then restart; the old agent reports `rolled_back` with
   the reason. The server independently fails requests without a report
   for 15 min (`start_timeout`).
7. The agent keeps the previous version until confirmation and deletes
   staging directories older than the last two versions.

**Rationale**: FR-013/FR-017 with package-manager consistency; v3 had
in-place swap only. **Alternatives**: in-cgroup `dpkg` (killed by the
restart, F6); `systemd` path units shipped in the package (older agents
cannot install them; more moving parts); apt/dnf (network, locks).

### D11 — Upgrade requests on the server

**Decision**: `internal/upgrades` (100 % gate) with table
`inventory_agent_upgrades` (tenant-scoped, RLS): states `pending →
delivered → downloading → installing → succeeded | failed | rolled_back`,
plus `expired` (default 7 days, FR-011) and `cancelled`; one active request
per agent (partial unique index on `(tenant_id, agent_id)` where state is
active); `attempts`, `last_error`, `requested_by`, `origin`
(`user|policy|agent`), timestamps. Operations: request for one agent, a
selection (≤ 1000 ids) or all outdated upgrade-capable agents of the tenant;
cancel (pending/delivered only); expiry sweeper (1/min, `FOR UPDATE SKIP
LOCKED`); stale-progress sweeper (15 min). Every transition writes an audit
row **synchronously through the repo in the same transaction** (the
buffered writer can drop, `audit.go:230-238`; FR-016 requires every event).

Derived per-agent upgrade state for the agent list (FR-015):
`up_to_date | available | pending | in_progress | failed(reason) |
rolled_back | manual_upgrade_required | unsupported | offline`, plus
current and target version and last change.

### D12 — Permissions

**Decision**: upgrade requests and cancellations for the platform's current
version reuse **`agents:manage`** (routine, like refresh/revoke; operator
included). A new permission **`agentupgrades:manage`** ("Configure
automatic agent upgrades and the target agent version") governs the policy,
resume, and pinning a different target version — including an older listed
version (rollback to known-good, SR-003); granted to owner, admin and the
module role administrator only. CASL `{manage, InventoryAgentUpgradePolicy}`.

**Rationale**: F9; least privilege for fleet-wide automatic code changes
and downgrades. **Alternative**: reuse `agents:manage` for everything
(operators could downgrade the fleet).

### D13 — Automatic upgrade policy (US5)

**Decision**: table `inventory_agent_upgrade_policy` (per tenant, RLS):
`enabled` (default false), `window_start`, `window_end` (`HH:MM`, may wrap
midnight), `timezone` (IANA, default `UTC`), `max_concurrent` (1–100,
default 5), `target_version` (`''` = platform current), `paused`,
`paused_reason`, `updated_by/at`. Worker `internal/upgrades/scheduler.go`
every 60 s per tenant under a per-tenant advisory lock: the pure planner
`PlanAuto(now, policy, agents, active)` selects outdated, online,
upgrade-capable agents (oldest version first, then agent id) up to
`max_concurrent − active`, only inside the window. A policy-originated
request ending in `failed` or `rolled_back` sets `paused=true`
(`upgrade_policy_paused` audit) until an administrator resumes.

### D14 — Agent CLI `update [--check]`

**Decision**: `inventory-agent update [-check] [-config path]` subcommand
(detected as the first argument before flag parsing; existing flags keep
working). Uses the persisted credential; `-check` prints current, available
target and exits 0 (up to date) / 10 (upgrade available) / 1 (error);
without `-check` it calls `CheckAgentUpdate(apply=true)` and runs the same
`selfupdate` flow (lock shared with the daemon). Must run as root /
Administrator.

### D15 — Inventory UI

- Host page (`ui/src/views/hosts/detail.vue`): memory card with arrays
  (location/use/ECC/max capacity), slots populated/total and a slot table
  incl. empty slots, type detail and rated/configured speed; chassis type;
  processor family; disks table (name, model, serial, size, media,
  interface, removable) and a filesystems table with disk column; notice
  for `hardware_schema < 2`.
- Agent list (`ui/src/views/agents/index.vue`): fleet view (online and
  offline), version, target, upgrade state chip with reason, last change,
  actions Upgrade / Upgrade selected / Upgrade all outdated / Cancel;
  policy card (read-only without `{manage, InventoryAgentUpgradePolicy}`).

### D16 — IPAM UI

Device page (`go-tangra-ipam-v4/ui/src/views/devices/detail.vue`): a
**Hardware** tab (only for devices with stored hardware; an empty state
explains "reported by the inventory agent ≥ 4.4.0" for host-reported
devices without hardware) with cards BIOS, System/Board/Chassis,
Processors, Memory (total, type, slots table incl. empty), Disks (with
filesystem usage bars); summary line in the device summary
("2× Intel Xeon Silver 4310 · 24 cores / 48 threads · 512 GiB DDR4 ·
3 disks 11.8 TB"). Reported strings rendered as text only.

### D17 — Release and rollout order

1. **inventory v4.4.0** + inventory SDK **`sdk/v4.2.0`** (proto, D1–D5,
   D7–D15, migration 0006, signed agent release in the image and on the
   GitHub release). Deploy the server first.
2. **One manual agent install per host** (`dpkg -i`/`rpm -U` of the 4.4.0
   package or the Windows exe) — agents < 4.4.0 show
   `manual_upgrade_required`. From then on upgrades run from the agent list.
3. **ipam v4.6.0** (SDK v4.2.0, migration 0009, hardware apply, UI).
   Before inventory 4.4.0 is live, reports carry no hardware and IPAM shows
   none (compatible, FR-006).
4. go-tangra-docker pins (user). Tags, merges and pins are user-confirmed.

### D18 — Observability

Metrics (`inventory.upgrades`): requests by state, active downloads,
download bytes, verification failures by reason, policy runs/pauses;
IPAM `ipam.hostsync` gains `hardware_updates_total`. Logs carry agent ids,
versions and reason codes only — never manifest contents beyond version,
never credentials, never serials at info level.

## Constitution check notes

- I: auto-upgrade off by default; downloads refused without a verified
  bundle; dev key only under a build tag; no insecure opt-out added.
- II: agent traffic only on the ingest edge with the per-agent credential;
  admin API behind the gateway with module permissions; IPAM ↔ inventory
  unchanged (SPIFFE, existing `ipam-hostsync` rule — no new RPC).
- III: proto bounds documented; validation at agent, ingest, projection and
  IPAM; OpenAPI `additionalProperties: false`, body limits; artifact size,
  chunk size, stream deadline and concurrency bounds.
- IV: tests first in every phase; negative tests: tampered/truncated/
  wrong-platform/wrong-version/unsigned/unknown-key artifacts, downgrade
  without flag, download without request, cross-tenant request ids, state
  transitions out of order, permission checks; fuzz targets for the SMBIOS
  decoder, disk parsers, manifest parser, version parser, report
  validation and the hardware diff.
- V: every upgrade transition audited transactionally; metrics; no secrets
  in logs.
- VI: no new dependencies (Ed25519 from the standard library; go-smbios,
  gopsutil, `x/sys` existing); release artifacts signed, `SHA256SUMS`
  retained; SBOM job extended to the agent artifacts.
- VII: one decoder, one selfupdate core, typed config sections
  (`agent_releases`, agent `upgrade`); complexity recorded in plan.md.

## STRIDE threat model

| Threat | Scenario | Mitigation |
|---|---|---|
| **S**poofing | Attacker poses as inventory and pushes an upgrade | Agent accepts commands only on its TLS ingest connection (pinned CA optional, server name verified); artifacts must verify against the compiled Ed25519 keyring regardless of the channel (SR-001/002). |
| **S** | A stolen agent credential downloads artifacts or reports fake success for another agent | Download/report bound to the verified agent's own active request (tenant + agent id); request ids of other agents → `NotFound`; revocation via `RevokeAgent`. Artifacts are public binaries — disclosure is harmless. |
| **T**ampering | Modified artifact in the image, DB or in transit | Signature over the manifest + per-artifact sha256 + size, verified by the server at import and by the agent before install; staging dir 0700; verification before any replacement (SR-004). |
| **T** | Wrong-platform or wrong-install-type artifact (e.g. rpm on Debian) | Manifest entry must match the agent's os/arch/install_type and the requested version; mismatch → `platform_mismatch`. |
| **T** | Downgrade to an older, vulnerable signed release | Agent refuses lower versions unless `allow_downgrade`, which the server sets only for an administrator pin (`agentupgrades:manage`, audited); compiled floor version. Residual: an attacker controlling inventory **and** an administrator account can pin an older signed version — visible in audit. |
| **T** | Hardware report manipulated to overwrite IPAM data | Hardware stored only in `ipam_device_hardware`, never in administrator columns; bounded, validated twice; audited field by field. |
| **R**epudiation | Mass upgrade or policy change denied later | Transactional audit rows for every request, transition, policy change, pause/resume, and CLI-initiated upgrade (actor agent); release imports logged with version and key id. |
| **I**nformation disclosure | Cross-tenant listing of agents/upgrades | Tenant-scoped RLS tables, subject tenant from the gateway, request ids unguessable UUIDs; releases are global but contain only public binaries. |
| **I** | Private signing key leak | Key only in a GitHub environment secret with required reviewers; never in the image, repo or runtime; rotation via keyring (new key id, old key kept until all agents upgraded). |
| **D**enial of service | Fleet-wide broken release bricks agents | Staged rollout: the automatic policy upgrades one agent until the target runs somewhere (canary, `PlanAuto` proven flag), operators upgrade one host before "all outdated"; auto-upgrade off by default, max concurrency, pause on first failure, automatic rollback ≤ 5 min, server stale-progress timeout. |
| **D** | Download storms exhaust bandwidth or DB | Global concurrent download cap, one per agent, downloads only for active requests, 1 MiB chunks read per request, 10 min deadline. |
| **D** | Host disk full during staging | Free-space check before download, size cap, cleanup of old staging dirs; failure reported, old agent keeps running. |
| **D** | Malformed SMBIOS/sysfs/PowerShell data crashes the agent | Pure decoders with length checks, bounded reads, fuzz targets; collection best effort with timeouts. |
| **E**levation of privilege | Upgrade path used to run arbitrary code as root | Only signed artifacts are executed; helper binary is the verified staged copy of the running agent; installer commands without a shell, fixed argument lists; systemd transient unit runs the same verified binary. |
| **E** | Operator enables auto-upgrade or pins a downgrade | `agentupgrades:manage` required (owner/admin/administrator), audited. |
