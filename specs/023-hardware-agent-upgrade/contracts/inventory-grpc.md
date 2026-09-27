# Contract: inventory.v1 proto additions (023)

File: `sdk/api/proto/inventory/v1/inventory.proto`. All changes are
**additive** (new fields/messages/enum values/RPCs only); `buf breaking
--against '.git#tag=sdk/v4.1.0,subdir=sdk'` must pass. Released as
inventory SDK **`sdk/v4.2.0`**.

## 1. Hardware collection (agent → ingest)

```proto
message Inventory {
  // ... fields 1-30 unchanged ...
  repeated Filesystem filesystems = 31;          // <= 1024 (new agents; replaces Disk.partitions)
  HardwareAvailability hardware_availability = 32;
  uint32 hardware_schema = 33;                   // 0/1 = legacy decoding, 2 = feature 023 (D2)
}

message ChassisInfo {
  // 1-6 unchanged; type (6) is now filled (DSP0134 7.4.1 names)
  string bootup_state = 7;
}

message Processor {
  // 1-11 unchanged
  string family = 12;    // DSP0134 7.5.2 (family 2 when family = FEh)
  string type = 13;      // DSP0134 7.5.1
  string upgrade = 14;   // DSP0134 7.5.5 (socket)
}

message MemoryInfo {
  // 1-3 unchanged; modules (3) now include EMPTY slots (populated=false)
  repeated MemoryArray arrays = 4;   // <= 64, every type-16 structure
  uint32 slots_total = 5;
  uint32 slots_populated = 6;
}

message MemoryArray {
  // 1-5 unchanged (location/use/error_correction now DSP0134 7.17 names)
  uint32 handle = 6;
}

message MemoryModule {
  // 1-10 unchanged (form_factor/memory_type now DSP0134 7.18.1/7.18.2 names)
  bool populated = 11;
  repeated string type_detail = 12;  // DSP0134 7.18.3 bit names, e.g. "Registered (Buffered)"
  uint32 array_handle = 13;
  string asset_tag = 14;
  uint32 rank_count = 15;            // 0 = unknown
}

message Disk {
  // 1-6 unchanged; media_type: ssd|hdd|nvme_ssd|unknown;
  // interface: nvme|sata|sas|scsi|usb|virtio|hyperv|xen|mmc|other;
  // partitions (6) = legacy (agents < 023: mounted partitions grouped by device)
  string name = 7;       // sda | nvme0n1 | PhysicalDrive0
  bool removable = 8;
  string vendor = 9;
}

message Filesystem {
  string mount = 1;
  string fs = 2;
  string device = 3;
  uint64 size_bytes = 4;
  uint64 free_bytes = 5;
  repeated string disks = 6;         // Disk.name values, <= 64
}

// HardwareAvailability values: ok | partial | unavailable | unsupported
message HardwareAvailability {
  string smbios = 1;
  string disks = 2;
}

message CollectionLimits {
  // 1-5 unchanged
  uint32 disks = 6;
  uint32 memory_slots = 7;
  uint32 memory_arrays = 8;
  uint32 processors = 9;
  uint32 filesystems = 10;
}
```

Bounds enforced by the agent, again by ingest `validateExtended` (excess
counted in `truncated`): disks 256, memory modules 1024, arrays 64,
processors 256, filesystems 1024, disks per filesystem 64, every string
≤ 256 bytes without control characters.

Enumerated names: exactly the DSP0134 3.7 table text (e.g. memory type
`DDR4`, form factor `DIMM`, array location `System board or motherboard`,
use `System memory`, ECC `Single-bit ECC`, chassis `Rack Mount Chassis`);
unknown code → `Unknown (code N)` (N decimal); field absent in the
structure version → empty string.

## 2. Hardware in the host report (inventory → ipam)

```proto
message HostReport {
  // 1-17 unchanged
  HardwareProfile hardware = 18;     // absent: legacy agent (hardware_schema < 2) or older inventory
}

message HardwareProfile {
  BIOSInfo bios = 1;
  SystemInfo system = 2;
  BaseboardInfo baseboard = 3;
  ChassisInfo chassis = 4;
  repeated Processor processors = 5;     // <= 256
  MemoryInfo memory = 6;                 // modules incl. empty slots, <= 1024
  repeated Disk disks = 7;               // <= 256, partitions never set
  repeated Filesystem filesystems = 8;   // <= 1024
  HardwareAvailability availability = 9;
  uint32 schema = 10;                    // = Inventory.hardware_schema
}
```

`report_digest` covers `hardware`. The page byte bound
(`host_reports.max_page_bytes`, default 3 MiB) is unchanged; a single report
with hardware stays well below 1 MiB at the bounds above.

SDK (`sdk/pkg/inventoryclient/hostreport.go`): `HostReport.Hardware
*Hardware` with plain-Go structs mirroring `HardwareProfile`
(`BIOS`, `System`, `Board`, `Chassis`, `Processors`, `Memory{Arrays,
Slots…}`, `Disks`, `Filesystems`, `Availability`, `Schema`).

## 3. Agent upgrade (ingest edge, per-agent credential)

```proto
enum CommandType {
  COMMAND_TYPE_UNSPECIFIED = 0;
  COMMAND_TYPE_REFRESH = 1;
  COMMAND_TYPE_UPGRADE = 2;          // NEW; old agents log and ignore it
}

message Command {
  string command_id = 1;
  CommandType type = 2;
  UpgradeCommand upgrade = 3;        // set when type = UPGRADE
}

message UpgradeCommand {
  string request_id = 1;             // uuid of the upgrade request
  string target_version = 2;
  bool allow_downgrade = 3;          // only for administrator pins (SR-003)
}

message AgentPlatform {
  string os = 1;                     // linux | windows
  string arch = 2;                   // amd64 | arm64
  string install_type = 3;           // deb | rpm | binary
}

message StreamRequest {
  string agent_id = 1;
  string agent_version = 2;
  AgentPlatform platform = 3;        // NEW
  repeated string capabilities = 4;  // NEW, <= 16 items of ^[a-z0-9.]{1,32}$; "upgrade.v1"
}

service IngestService {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);
  rpc SubmitInventory(SubmitRequest) returns (SubmitResponse);
  rpc StreamCommands(StreamRequest) returns (stream Command);
  // NEW (authenticated like every method except Enroll):
  rpc CheckAgentUpdate(CheckAgentUpdateRequest) returns (CheckAgentUpdateResponse);
  rpc DownloadAgentRelease(DownloadAgentReleaseRequest) returns (stream DownloadAgentReleaseResponse);
  rpc ReportUpgrade(ReportUpgradeRequest) returns (ReportUpgradeResponse);
}

message CheckAgentUpdateRequest {
  string current_version = 1;
  AgentPlatform platform = 2;
  bool apply = 3;                    // true: create a request (origin agent) when an upgrade is available
}

message CheckAgentUpdateResponse {
  bool available = 1;
  string target_version = 2;         // "" when none stored for the platform
  string request_id = 3;             // active or newly created request ("" when none)
  string reason = 4;                 // e.g. no_release_for_platform, up_to_date, upgrade_active
}

message DownloadAgentReleaseRequest {
  string request_id = 1;             // required unless version = the agent's current version (rollback package)
  string version = 2;
}

message DownloadAgentReleaseResponse {
  oneof part {
    ReleaseHeader header = 1;        // always the first message
    ArtifactChunk chunk = 2;
  }
}

message ReleaseHeader {
  bytes manifest = 1;                // exact signed bytes, <= 64 KiB
  bytes signature = 2;               // Ed25519, 64 bytes
  string key_id = 3;
  string file = 4;                   // artifact entry chosen for the agent's platform
  int64 size = 5;
  string sha256 = 6;
}

message ArtifactChunk {
  int64 offset = 1;
  bytes data = 2;                    // <= 1 MiB
}

// ReportUpgrade states: downloading | installing | succeeded | failed | rolled_back
// Reason codes (closed set): signature_invalid, unknown_key, checksum_mismatch,
// size_mismatch, platform_mismatch, version_mismatch, downgrade_refused,
// disk_full, download_failed, install_failed, start_timeout,
// unsupported_install, busy, package_db_mismatch, cancelled_locally
message ReportUpgradeRequest {
  string request_id = 1;
  string state = 2;
  string from_version = 3;
  string to_version = 4;
  string reason = 5;
  string detail = 6;                 // <= 256 bytes, sanitised, never shown as HTML
}

message ReportUpgradeResponse {
  bool accepted = 1;                 // false = transition ignored (terminal/duplicate)
}
```

### Server rules

| RPC | Rule | Error |
|---|---|---|
| all | agent from the credential; tenant from the agent row | `Unauthenticated` |
| `StreamCommands` | stores platform/capabilities (validated closed sets) on `inventory_agents`; after registering, delivers every `pending`/`delivered` request of this agent (marks `delivered`) | — |
| `CheckAgentUpdate` | target = tenant pin or platform current; `apply` creates a request (origin `agent`, audited) only when target > current and none active | `InvalidArgument` bad platform |
| `DownloadAgentRelease` | request must belong to this agent and be active (`delivered`/`downloading`/`installing`) and `version` = its target; or `version` = the agent's recorded current version; artifact for the agent's stored platform must exist and be `complete`; one stream per agent; global cap | `NotFound` (foreign/missing request or artifact), `FailedPrecondition` (request not active), `ResourceExhausted` (caps), `DeadlineExceeded` (10 min) |
| `ReportUpgrade` | request must belong to this agent; only valid transitions (data-model §1.4); `succeeded` requires `to_version` = target and the connection's `agent_version` = target | `NotFound`, `InvalidArgument` (unknown state/reason, oversize detail) |

The ingest `MaxRecvMsgSize` stays at `limits.max_snapshot_bytes`; the
server sends ≤ 1 MiB messages (client default 4 MiB receive limit).

## 4. Release manifest (`agent-release.json`, signed)

```json
{
  "schema": 1,
  "version": "4.4.0",
  "created_at": "2026-10-02T10:00:00Z",
  "key_id": "tangra-agent-2026",
  "artifacts": [
    {"os": "linux", "arch": "amd64", "install_type": "deb",
     "file": "tangra-inventory-agent_4.4.0_amd64.deb", "size": 9123456,
     "sha256": "<64 hex>"},
    {"os": "linux", "arch": "amd64", "install_type": "rpm", "file": "tangra-inventory-agent-4.4.0-1.x86_64.rpm", "size": 0, "sha256": ""},
    {"os": "linux", "arch": "amd64", "install_type": "binary", "file": "inventory-agent-linux-amd64", "size": 0, "sha256": ""},
    {"os": "windows", "arch": "amd64", "install_type": "binary", "file": "inventory-agent-windows-amd64.exe", "size": 0, "sha256": ""}
    // + arm64 variants: 8 entries in total
  ]
}
```

- `agent-release.json.sig`: base64 of `ed25519.Sign(priv, manifestBytes)`.
- Parser (`internal/agentrelease`): strict JSON (unknown fields rejected),
  ≤ 64 KiB, `schema` = 1, semantic version, ≤ 16 artifacts, unique
  `(os, arch, install_type)`, closed sets, file name pattern
  `^[A-Za-z0-9._+~-]{1,128}$`, size 1..150 MiB, lowercase hex sha256.
- Verification order: key id known → signature valid over the raw bytes →
  parse → version/platform match → stream size and sha256 match.

## 5. Policy (mesh)

No new mesh RPC; the ipam rule `ipam-hostsync` (three `HostReportService`
RPCs + health) is unchanged. `IngestService` stays off-mesh.
