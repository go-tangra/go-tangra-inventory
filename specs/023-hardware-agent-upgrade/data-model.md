# Data Model: Hardware Details for Devices and Agent Self-Upgrade (023)

Two repositories change: **inventory** (`go-tangra-inventory-v4`: agent,
ingest, storage, projection, upgrades) and **ipam** (`go-tangra-ipam-v4`:
device hardware). Proto text is in
[contracts/inventory-grpc.md](contracts/inventory-grpc.md).

## 1. Inventory

### 1.1 Domain structs (`internal/store/models.go`)

```go
// Inventory (additions)
type Inventory struct {
    // ... existing fields ...
    Filesystems    []Filesystem         `json:"filesystems,omitempty"`   // NEW (D3)
    HardwareSchema uint32               `json:"hardware_schema,omitempty"` // NEW (D2): 0/1 legacy, 2 = this feature
    Availability   HardwareAvailability `json:"hardware_availability"`   // NEW
}

type ChassisInfo struct { /* existing */ Type string; BootupState string } // Type now filled; BootupState NEW
type BaseboardInfo struct { /* existing; BoardType now decoded per DSP0134 */ }

type Processor struct {
    // existing fields ...
    Family  string `json:"family,omitempty"`  // NEW e.g. "Intel Xeon Processor"
    Type    string `json:"type,omitempty"`    // NEW e.g. "Central Processor"
    Upgrade string `json:"upgrade,omitempty"` // NEW socket, e.g. "Socket LGA4189"
}

type MemoryInfo struct {
    TotalPhysicalBytes uint64         // sum of populated modules in "System memory" arrays
    Array              MemoryArray    // primary array (first with use "System memory")
    Modules            []MemoryModule // EVERY slot, populated or empty (≤ 1024)
    Arrays             []MemoryArray  // NEW: all type-16 arrays (≤ 64)
    SlotsTotal         uint32         // NEW
    SlotsPopulated     uint32         // NEW
}

type MemoryArray struct {
    // existing: Location, Use, ErrorCorrection, MaximumCapacity, NumberOfDevices
    Handle uint32 `json:"handle,omitempty"` // NEW: SMBIOS handle, links modules
}

type MemoryModule struct {
    // existing: DeviceLocator, BankLocator, CapacityBytes, FormFactor, MemoryType,
    //           SpeedMTs, ConfiguredSpeedMTs, Manufacturer, SerialNumber, PartNumber
    Populated   bool     `json:"populated"`              // NEW: false = empty slot
    TypeDetail  []string `json:"type_detail,omitempty"`  // NEW: e.g. ["Synchronous","Registered (Buffered)"]
    ArrayHandle uint32   `json:"array_handle,omitempty"` // NEW
    AssetTag    string   `json:"asset_tag,omitempty"`    // NEW
    RankCount   uint32   `json:"rank_count,omitempty"`   // NEW (attributes bits 3:0)
}

type Disk struct {
    // existing: Model, Serial, SizeBytes, MediaType, Interface, Partitions (legacy)
    Name      string `json:"name,omitempty"`      // NEW: sda | nvme0n1 | PhysicalDrive0
    Removable bool   `json:"removable,omitempty"` // NEW
    Vendor    string `json:"vendor,omitempty"`    // NEW (when separate from model)
}
// MediaType closed set: ssd | hdd | nvme_ssd | unknown
// Interface closed set: nvme | sata | sas | scsi | usb | virtio | hyperv | xen | mmc | other

type Filesystem struct { // NEW (replaces Disk.Partitions for new agents)
    Mount     string   `json:"mount"`
    FS        string   `json:"fs,omitempty"`
    Device    string   `json:"device,omitempty"` // /dev/sda1, /dev/mapper/vg-root, C:
    SizeBytes uint64   `json:"size_bytes,omitempty"`
    FreeBytes uint64   `json:"free_bytes,omitempty"`
    Disks     []string `json:"disks,omitempty"`  // Disk.Name values (≤ 64)
}

type HardwareAvailability struct { // NEW; values ok | partial | unavailable | unsupported
    SMBIOS string `json:"smbios,omitempty"`
    Disks  string `json:"disks,omitempty"`
}

type CollectionLimits struct {
    // existing: Interfaces, Addresses, Guests, Packages, BmcPorts
    Disks, MemorySlots, MemoryArrays, Processors, Filesystems uint32 // NEW
}

const (
    MaxDisks        = 256
    MaxMemorySlots  = 1024
    MaxMemoryArrays = 64
    MaxProcessors   = 256
    MaxFilesystems  = 1024
    MaxFSDisks      = 64
    MaxHWString     = 256 // bytes, after control-char removal
    HardwareSchemaCurrent = 2
)

// Agent (additions; platform reported on StreamCommands, D9)
type Agent struct {
    // existing fields ...
    OS           string   `json:"os,omitempty"`           // linux | windows
    Arch         string   `json:"arch,omitempty"`         // amd64 | arm64
    InstallType  string   `json:"install_type,omitempty"` // deb | rpm | binary
    Capabilities []string `json:"capabilities,omitempty"` // e.g. upgrade.v1
    PlatformSeenAt time.Time `json:"platform_seen_at"`
}

// Releases (global, not tenant data)
type AgentRelease struct {
    Version        string
    Manifest       []byte // exact signed bytes
    Signature      []byte // 64-byte Ed25519
    KeyID          string
    ManifestSHA256 string
    Source         string // bundled | import
    ImportedAt     time.Time
    Artifacts      []AgentArtifact
}
type AgentArtifact struct {
    Version, OS, Arch, InstallType, File string
    Size   int64
    SHA256 string
}

// Upgrade request (tenant-scoped)
type AgentUpgrade struct {
    ID, TenantID, AgentID, HostID string
    FromVersion, TargetVersion    string
    AllowDowngrade                bool
    State       string // pending|delivered|downloading|installing|succeeded|failed|rolled_back|expired|cancelled
    Origin      string // user | policy | agent
    RequestedBy string // user id, "upgrade-policy", or agent id
    Reason      string // closed code set (contracts/inventory-grpc.md)
    Attempts    int
    CreatedAt, UpdatedAt, ExpiresAt time.Time
    DeliveredAt, StartedAt, FinishedAt *time.Time
}

// Upgrade policy (tenant-scoped, one row per tenant)
type AgentUpgradePolicy struct {
    TenantID      string
    Enabled       bool   // default false
    WindowStart   string // "HH:MM"
    WindowEnd     string // "HH:MM" (may wrap midnight; equal = whole day)
    Timezone      string // IANA, default "UTC"
    MaxConcurrent int    // 1-100, default 5
    TargetVersion string // "" = platform current
    Paused        bool
    PausedReason  string // e.g. "failed:<request id>"
    UpdatedBy     string
    UpdatedAt     time.Time
}

// Derived per-agent view (not stored)
type AgentFleetEntry struct {
    Agent
    Online         bool
    Hostname       string
    TargetVersion  string
    UpgradeState   string // up_to_date|available|pending|in_progress|failed|rolled_back|manual_upgrade_required|unsupported|offline
    UpgradeReason  string
    UpgradeID      string
    StateChangedAt time.Time
}
```

### 1.2 Host report projection (`internal/hostreport`)

`Project` adds `HardwareProfile` when `Payload.HardwareSchema ≥ 2`:
BIOS, System, Baseboard, Chassis, Processors (≤ 256), Memory (total,
primary array, arrays, all slots, slots total/populated), Disks (without
legacy `partitions`), Filesystems, Availability; `Truncated` carries the new
counters. Legacy snapshots (`HardwareSchema < 2`) → `hardware` absent. The
digest (`Digest`) covers the hardware section (deterministic proto
encoding, slices in collection order).

### 1.3 Migration `internal/store/migrations/0006_hardware_upgrades.sql`

```sql
-- +goose Up
-- Component tables: new hardware columns (payload stays authoritative).
ALTER TABLE inventory_memory_modules
  ADD COLUMN populated   boolean NOT NULL DEFAULT true,
  ADD COLUMN type_detail text    NOT NULL DEFAULT '';
ALTER TABLE inventory_disks
  ADD COLUMN name      text    NOT NULL DEFAULT '',
  ADD COLUMN removable boolean NOT NULL DEFAULT false;
ALTER TABLE inventory_processors
  ADD COLUMN family text NOT NULL DEFAULT '';

-- Agent platform (reported on StreamCommands).
ALTER TABLE inventory_agents
  ADD COLUMN os               text   NOT NULL DEFAULT '' CHECK (os IN ('', 'linux', 'windows')),
  ADD COLUMN arch             text   NOT NULL DEFAULT '' CHECK (arch IN ('', 'amd64', 'arm64')),
  ADD COLUMN install_type     text   NOT NULL DEFAULT '' CHECK (install_type IN ('', 'deb', 'rpm', 'binary')),
  ADD COLUMN capabilities     text[] NOT NULL DEFAULT '{}',
  ADD COLUMN platform_seen_at timestamptz;

-- Agent releases: GLOBAL (no tenant_id, no RLS) — public signed binaries.
CREATE TABLE inventory_agent_releases (
  version         text PRIMARY KEY CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
  manifest        bytea NOT NULL CHECK (octet_length(manifest) <= 65536),
  signature       bytea NOT NULL CHECK (octet_length(signature) = 64),
  key_id          text  NOT NULL CHECK (key_id ~ '^[a-z0-9-]{1,32}$'),
  manifest_sha256 text  NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  source          text  NOT NULL CHECK (source IN ('bundled', 'import')),
  imported_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE inventory_agent_artifacts (
  version      text   NOT NULL REFERENCES inventory_agent_releases(version) ON DELETE CASCADE,
  os           text   NOT NULL CHECK (os IN ('linux', 'windows')),
  arch         text   NOT NULL CHECK (arch IN ('amd64', 'arm64')),
  install_type text   NOT NULL CHECK (install_type IN ('deb', 'rpm', 'binary')),
  file         text   NOT NULL CHECK (file ~ '^[A-Za-z0-9._+~-]{1,128}$'),
  size         bigint NOT NULL CHECK (size > 0 AND size <= 157286400),
  sha256       text   NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  complete     boolean NOT NULL DEFAULT false,
  PRIMARY KEY (version, os, arch, install_type)
);
CREATE TABLE inventory_agent_artifact_chunks (
  version      text    NOT NULL,
  os           text    NOT NULL,
  arch         text    NOT NULL,
  install_type text    NOT NULL,
  seq          integer NOT NULL CHECK (seq >= 0),
  data         bytea   NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 1048576),
  PRIMARY KEY (version, os, arch, install_type, seq),
  FOREIGN KEY (version, os, arch, install_type)
    REFERENCES inventory_agent_artifacts (version, os, arch, install_type) ON DELETE CASCADE
);

-- Upgrade requests (tenant-scoped).
CREATE TABLE inventory_agent_upgrades (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  agent_id        uuid NOT NULL REFERENCES inventory_agents(id) ON DELETE CASCADE,
  host_id         uuid REFERENCES inventory_hosts(id) ON DELETE SET NULL,
  from_version    text NOT NULL DEFAULT '',
  target_version  text NOT NULL,
  allow_downgrade boolean NOT NULL DEFAULT false,
  state           text NOT NULL CHECK (state IN ('pending','delivered','downloading','installing',
                                                 'succeeded','failed','rolled_back','expired','cancelled')),
  origin          text NOT NULL CHECK (origin IN ('user','policy','agent')),
  requested_by    text NOT NULL DEFAULT '',
  reason          text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
  attempts        integer NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,
  delivered_at    timestamptz,
  started_at      timestamptz,
  finished_at     timestamptz
);
CREATE UNIQUE INDEX agent_upgrades_one_active ON inventory_agent_upgrades (tenant_id, agent_id)
  WHERE state IN ('pending','delivered','downloading','installing');
CREATE INDEX agent_upgrades_tenant_state ON inventory_agent_upgrades (tenant_id, state, updated_at);
CREATE INDEX agent_upgrades_expiry ON inventory_agent_upgrades (expires_at)
  WHERE state IN ('pending','delivered');

-- Upgrade policy (tenant-scoped).
CREATE TABLE inventory_agent_upgrade_policy (
  tenant_id      uuid PRIMARY KEY,
  enabled        boolean NOT NULL DEFAULT false,
  window_start   text NOT NULL DEFAULT '02:00' CHECK (window_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  window_end     text NOT NULL DEFAULT '04:00' CHECK (window_end   ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  timezone       text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) BETWEEN 1 AND 64),
  max_concurrent integer NOT NULL DEFAULT 5 CHECK (max_concurrent BETWEEN 1 AND 100),
  target_version text NOT NULL DEFAULT '',
  paused         boolean NOT NULL DEFAULT false,
  paused_reason  text NOT NULL DEFAULT '',
  updated_by     text NOT NULL DEFAULT '',
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- RLS like 0004 for the two tenant tables.
ALTER TABLE inventory_agent_upgrades ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_agent_upgrades FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inventory_agent_upgrades
  USING (current_setting('app.system', true) = 'on' OR tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (current_setting('app.system', true) = 'on' OR tenant_id = current_setting('app.tenant_id', true)::uuid);
-- (same for inventory_agent_upgrade_policy; exact expressions copied from 0004_rls.sql)

-- +goose Down
-- drops in reverse order; ALTER TABLE ... DROP COLUMN IF EXISTS for the added columns.
```

The release tables are deliberately outside RLS (not tenant data, only
public signed binaries); only the startup seeder and the import CLI write
them (system scope). Expiry and stale-progress sweepers run in system
scope.

### 1.4 State transitions — upgrade request

```text
pending ──deliver──▶ delivered ──ReportUpgrade(downloading)──▶ downloading
   │                    │                                         │
   │ cancel / expiry    │ cancel / expiry                         ├─(installing)──▶ installing ──(succeeded)──▶ succeeded
   ▼                    ▼                                         │                    │
cancelled / expired  cancelled / expired                          └─(failed)──▶ failed ◀┘ (failed)
                                                                                     installing ──(rolled_back)──▶ rolled_back
downloading/installing ──no report for 15 min──▶ failed (start_timeout)
```

- `succeeded` is accepted only when `to_version` = `target_version` and the
  agent's reported `agent_version` equals it on the same connection.
- Reports for terminal requests are ignored (idempotent), reports for
  another agent's request → `NotFound`.
- A new request for an agent with an active one → `409 upgrade_active`.

### 1.5 Derived agent upgrade state (fleet view)

| Condition (first match) | State |
|---|---|
| active request pending/delivered | `pending` |
| active request downloading/installing | `in_progress` |
| agent lacks capability `upgrade.v1` and version < current | `manual_upgrade_required` |
| install_type `binary` on Linux without systemd (reason `unsupported_install` on last request) | `unsupported` |
| last request failed within 7 days and version < target | `failed` (+reason) |
| last request rolled_back within 7 days | `rolled_back` (+reason) |
| version < target | `available` (shown `offline` badge when not connected) |
| otherwise | `up_to_date` |

Version comparison: semantic versions (`MAJOR.MINOR.PATCH[-pre]`); git
describe versions `4.3.1~11-gabc` = pre-release of 4.3.1; `dev` or
unparsable versions are never "outdated" automatically (manual only).

### 1.6 Configuration

Service (`internal/config/config.go`, new section, typed and validated):

```yaml
agent_releases:
  bundle_dir: /app/agent-releases     # read at startup; missing = no bundle
  keep_versions: 5                    # 2-50
  max_concurrent_downloads: 20        # 1-500
  chunk_bytes: 1048576                # 65536-1048576
  request_ttl_hours: 168              # 1-720 (FR-011, 7 days)
  progress_timeout_minutes: 15        # 5-120
```

Agent (`AgentConfig`, `KnownFields(true)`):

```yaml
upgrade:
  enabled: true                       # accept server upgrade requests (false = manual `update` only)
  confirm_timeout_seconds: 300        # 60-1800 (FR-013)
  staging_dir: ""                     # default /var/lib/inventory-agent/upgrade | %ProgramData%\go-tangra\inventory-agent\upgrade
collect_disks: true
```

## 2. IPAM (`go-tangra-ipam-v4`)

### 2.1 Migration `go-tangra-ipam-v4/internal/store/migrations/0009_device_hardware.sql`

```sql
-- +goose Up
CREATE TABLE ipam_device_hardware (
  device_id          uuid PRIMARY KEY REFERENCES ipam_devices(id) ON DELETE CASCADE,
  tenant_id          uuid NOT NULL,
  profile            jsonb NOT NULL CHECK (pg_column_size(profile) <= 262144),
  digest             text  NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  cpu_model          text  NOT NULL DEFAULT '',
  cpu_sockets        integer NOT NULL DEFAULT 0,
  cpu_cores          integer NOT NULL DEFAULT 0,
  cpu_threads        integer NOT NULL DEFAULT 0,
  memory_total_bytes bigint  NOT NULL DEFAULT 0,
  memory_type        text    NOT NULL DEFAULT '',
  memory_slots_total integer NOT NULL DEFAULT 0,
  memory_slots_used  integer NOT NULL DEFAULT 0,
  disk_count         integer NOT NULL DEFAULT 0,
  disk_total_bytes   bigint  NOT NULL DEFAULT 0,
  reported_at        timestamptz NOT NULL,
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX device_hardware_tenant ON ipam_device_hardware (tenant_id);
ALTER TABLE ipam_device_hardware ENABLE ROW LEVEL SECURITY;
ALTER TABLE ipam_device_hardware FORCE ROW LEVEL SECURITY;
-- tenant_isolation policy copied from 0003_rls.sql (app.tenant_id / app.system)

-- +goose Down
DROP TABLE IF EXISTS ipam_device_hardware;
```

### 2.2 Go model (`go-tangra-ipam-v4/internal/store/models.go`)

```go
type DeviceHardware struct {
    DeviceID, TenantID string
    Profile            HardwareProfile // JSON
    Digest             string
    Summary            HardwareSummary
    ReportedAt, UpdatedAt time.Time
}
type HardwareSummary struct {
    CPUModel string; CPUSockets, CPUCores, CPUThreads int
    MemoryTotalBytes int64; MemoryType string; MemorySlotsTotal, MemorySlotsUsed int
    DiskCount int; DiskTotalBytes int64 // physical, non-removable disks
}
type HardwareProfile struct {
    BIOS      struct{ Vendor, Version, ReleaseDate string }
    System    struct{ Manufacturer, Product, Version, Serial, UUID, SKU, Family string }
    Board     struct{ Manufacturer, Product, Serial string }
    Chassis   struct{ Type, Manufacturer, Serial, AssetTag string }
    Processors []struct{ Socket, Manufacturer, Model, Family string; MaxMHz, CurMHz, Cores, Threads int; Populated bool }
    Memory    struct {
        TotalBytes int64; ErrorCorrection, Location, Use string; MaxCapacityBytes int64
        SlotsTotal, SlotsUsed int
        Slots []struct{ Locator, Bank string; Populated bool; SizeBytes int64; Type, FormFactor string
                        TypeDetail []string; SpeedMTs, ConfiguredMTs int; Manufacturer, PartNumber, Serial string }
    }
    Disks       []struct{ Name, Model, Vendor, Serial string; SizeBytes int64; Media, Interface string; Removable bool }
    Filesystems []struct{ Mount, FS string; SizeBytes, FreeBytes int64; Disks []string }
    Availability struct{ SMBIOS, Disks string }
    Truncated    map[string]int
}
// Device gains read-only: HardwareSummary *HardwareSummary `json:"hardware_summary,omitempty"`
```

### 2.3 Repo contract (`go-tangra-ipam-v4/internal/repo/repo.go`)

- `HostTx.GetHardware(deviceID) (*store.DeviceHardware, error)` (state for
  the planner, loaded with the device state).
- `HostTx.ReplaceHardware(h store.DeviceHardware) error` (upsert by
  device id; `UPDATE` sets exactly the profile, digest, summary and
  timestamps).
- `Store.GetDeviceHardware(ctx, tenantID, deviceID)` for the HTTP read.
- Device reads `LEFT JOIN ipam_device_hardware` for `hardware_summary`.

### 2.4 Normalisation (`go-tangra-ipam-v4/internal/hostreport/hardware.go`)

`hostreport.Report.Hardware *Hardware` (nil when the report has none).
Rules: bounds 256 disks / 1024 slots / 256 processors / 1024 filesystems /
64 disk refs; strings ≤ 256 bytes, valid UTF-8, control characters
removed; media/interface/availability closed sets; placeholder serials
(`To Be Filled By O.E.M.`, `Default string`, `0`, all zeros) kept as data
but never used for matching; digest = sha256 of the canonical JSON of the
normalised profile; issues `hardware_truncated_<list>`.

### 2.5 Entity: device hardware lifecycle

| Report | Stored | Result |
|---|---|---|
| hardware present, no row | — | insert, audit `hardware_reported` |
| hardware present, digest equal | row | no op |
| hardware present, digest differs | row | replace, audit `hardware_updated` with `changes` |
| hardware absent | row or none | untouched (old agent/inventory) |
| device deleted | row | cascade |
