// Package store holds the inventory domain types and the SQL-backed store.
package store

import "time"

// Host status vocabulary.
const (
	HostActive  = "active"
	HostStale   = "stale"
	HostRetired = "retired"
)

// Snapshot source vocabulary.
const (
	SourceAgent  = "agent"
	SourceManual = "manual"
	SourceImport = "import"
)

// Change type vocabulary.
const (
	ChangeAdded    = "added"
	ChangeRemoved  = "removed"
	ChangeModified = "modified"
)

// Host identity key vocabulary (which key resolved the host).
const (
	IdentityHardwareUUID = "hardware_uuid"
	IdentityMachineID    = "machine_id"
	IdentityHostname     = "hostname"
)

// Identity carries the fields an agent reports to resolve a stable host.
type Identity struct {
	HardwareUUID string `json:"hardware_uuid"`
	MachineID    string `json:"machine_id"`
	Hostname     string `json:"hostname"`
}

// Host is a managed endpoint (one row per host per tenant).
type Host struct {
	ID             string            `json:"id"`
	TenantID       string            `json:"tenant_id"`
	Hostname       string            `json:"hostname"`
	MachineID      string            `json:"machine_id"`
	HardwareUUID   string            `json:"hardware_uuid"`
	SystemSerial   string            `json:"system_serial"`
	IdentityKey    string            `json:"identity_key"`
	Manufacturer   string            `json:"manufacturer"`
	Model          string            `json:"model"`
	OSName         string            `json:"os_name"`
	OSVersion      string            `json:"os_version"`
	OSArch         string            `json:"os_arch"`
	AgentVersion   string            `json:"agent_version"`
	AssignedUser   string            `json:"assigned_user"`
	Status         string            `json:"status"`
	Tags           map[string]string `json:"tags,omitempty"`
	FirstSeen      time.Time         `json:"first_seen"`
	LastSeen       time.Time         `json:"last_seen"`
	LastSnapshotID string            `json:"last_snapshot_id,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	// ReportDigest is the hex sha256 of the host's report projection (see
	// internal/hostreport); "" until the first snapshot after migration 0005 or
	// after a status change that invalidated it. ReportChangedAt is when the
	// digest last changed (zero = never recorded).
	ReportDigest    string    `json:"-"`
	ReportChangedAt time.Time `json:"-"`
}

// Snapshot is an immutable inventory report bound to a host. Payload holds the
// full inventory; the lifted columns support fast summaries and filters.
type Snapshot struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	HostID       string    `json:"host_id"`
	CollectedAt  time.Time `json:"collected_at"`
	ReceivedAt   time.Time `json:"received_at"`
	AgentVersion string    `json:"agent_version"`
	Source       string    `json:"source"`
	OSName       string    `json:"os_name"`
	OSVersion    string    `json:"os_version"`
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	Payload      Inventory `json:"payload"`
}

// Inventory is the full collected payload (hardware + software/OS + network).
type Inventory struct {
	Identity     Identity      `json:"identity"`
	CollectedAt  time.Time     `json:"collected_at"`
	AgentVersion string        `json:"agent_version"`
	OS           OSInfo        `json:"os"`
	BIOS         BIOSInfo      `json:"bios"`
	System       SystemInfo    `json:"system"`
	Baseboard    BaseboardInfo `json:"baseboard"`
	Chassis      ChassisInfo   `json:"chassis"`
	Processors   []Processor   `json:"processors,omitempty"`
	Cache        []CacheInfo   `json:"cache,omitempty"`
	Memory       MemoryInfo    `json:"memory"`
	Ports        []string      `json:"ports,omitempty"`
	Slots        []string      `json:"slots,omitempty"`
	OEMStrings   []string      `json:"oem_strings,omitempty"`
	BIOSLanguage string        `json:"bios_language,omitempty"`
	Monitors     []Monitor     `json:"monitors,omitempty"`
	Programs     []Program     `json:"installed_programs,omitempty"`
	Services     []Service     `json:"services,omitempty"`
	Users        []UserAccount `json:"users,omitempty"`
	Patches      []Patch       `json:"patches,omitempty"`
	Environment  Environment   `json:"environment"`
	Networks     []NetIface    `json:"network_interfaces,omitempty"`
	Disks        []Disk        `json:"disks,omitempty"`

	// Host report fields (feature 020). Zero values from older agents.
	PrimaryIPv4      string            `json:"primary_ipv4,omitempty"`
	PrimaryIPv6      string            `json:"primary_ipv6,omitempty"`
	Virtualization   Virtualization    `json:"virtualization"`
	Bmc              *Bmc              `json:"bmc,omitempty"` // nil = no BMC or not readable
	HypervisorGuests []HypervisorGuest `json:"hypervisor_guests,omitempty"`
	UpdateState      UpdateState       `json:"update_state"`
	Truncated        CollectionLimits  `json:"truncated"`

	// Hardware details (feature 023). Filesystems replaces Disk.Partitions
	// for new agents; HardwareSchema 0/1 marks the legacy (shifted) SMBIOS
	// decoding of older agents, HardwareSchemaCurrent the DSP0134-correct one.
	Filesystems    []Filesystem         `json:"filesystems,omitempty"`
	HardwareSchema uint32               `json:"hardware_schema,omitempty"`
	Availability   HardwareAvailability `json:"hardware_availability"`
}

// --- hardware ---

type BIOSInfo struct {
	Vendor      string `json:"vendor,omitempty"`
	Version     string `json:"version,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

type SystemInfo struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	ProductName  string `json:"product_name,omitempty"`
	Version      string `json:"version,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	UUID         string `json:"uuid,omitempty"`
	WakeUpType   string `json:"wake_up_type,omitempty"`
	SKUNumber    string `json:"sku_number,omitempty"`
	Family       string `json:"family,omitempty"`
}

type BaseboardInfo struct {
	Manufacturer      string `json:"manufacturer,omitempty"`
	Product           string `json:"product,omitempty"`
	Version           string `json:"version,omitempty"`
	SerialNumber      string `json:"serial_number,omitempty"`
	AssetTag          string `json:"asset_tag,omitempty"`
	LocationInChassis string `json:"location_in_chassis,omitempty"`
	BoardType         string `json:"board_type,omitempty"`
}

type ChassisInfo struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Version      string `json:"version,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
	AssetTag     string `json:"asset_tag,omitempty"`
	SKUNumber    string `json:"sku_number,omitempty"`
	Type         string `json:"type,omitempty"`         // DSP0134 7.4.1 name
	BootupState  string `json:"bootup_state,omitempty"` // DSP0134 7.4.2 name
}

type Processor struct {
	SocketDesignation string `json:"socket_designation,omitempty"`
	Manufacturer      string `json:"manufacturer,omitempty"`
	Version           string `json:"version,omitempty"`
	MaxSpeedMHz       uint32 `json:"max_speed_mhz,omitempty"`
	CurrentSpeedMHz   uint32 `json:"current_speed_mhz,omitempty"`
	CoreCount         uint32 `json:"core_count,omitempty"`
	CoreEnabled       uint32 `json:"core_enabled,omitempty"`
	ThreadCount       uint32 `json:"thread_count,omitempty"`
	PartNumber        string `json:"part_number,omitempty"`
	SerialNumber      string `json:"serial_number,omitempty"`
	SocketPopulated   bool   `json:"socket_populated,omitempty"`
	Family            string `json:"family,omitempty"`  // DSP0134 7.5.2, e.g. "Xeon"
	Type              string `json:"type,omitempty"`    // DSP0134 7.5.1, e.g. "Central Processor"
	Upgrade           string `json:"upgrade,omitempty"` // DSP0134 7.5.5 socket, e.g. "Socket LGA4189"
}

type CacheInfo struct {
	SocketDesignation string `json:"socket_designation,omitempty"`
}

// MemoryInfo is the memory subsystem. From HardwareSchemaCurrent on Modules
// lists every slot (empty ones with Populated false), Array is the primary
// array (the first of use "System memory") and TotalPhysicalBytes sums the
// populated modules of "System memory" arrays; older agents reported
// populated modules only.
type MemoryInfo struct {
	TotalPhysicalBytes uint64         `json:"total_physical_bytes,omitempty"`
	Array              MemoryArray    `json:"array"`
	Modules            []MemoryModule `json:"modules,omitempty"`
	Arrays             []MemoryArray  `json:"arrays,omitempty"`
	SlotsTotal         uint32         `json:"slots_total,omitempty"`
	SlotsPopulated     uint32         `json:"slots_populated,omitempty"`
}

type MemoryArray struct {
	Location        string `json:"location,omitempty"`
	Use             string `json:"use,omitempty"`
	ErrorCorrection string `json:"error_correction,omitempty"`
	MaximumCapacity uint64 `json:"maximum_capacity,omitempty"`
	NumberOfDevices uint32 `json:"number_of_devices,omitempty"`
	Handle          uint32 `json:"handle,omitempty"` // SMBIOS handle; MemoryModule.ArrayHandle refers to it
}

type MemoryModule struct {
	DeviceLocator      string `json:"device_locator,omitempty"`
	BankLocator        string `json:"bank_locator,omitempty"`
	CapacityBytes      uint64 `json:"capacity_bytes,omitempty"`
	FormFactor         string `json:"form_factor,omitempty"`
	MemoryType         string `json:"memory_type,omitempty"`
	SpeedMTs           uint32 `json:"speed_mt_s,omitempty"`
	ConfiguredSpeedMTs uint32 `json:"configured_speed_mt_s,omitempty"`
	Manufacturer       string `json:"manufacturer,omitempty"`
	SerialNumber       string `json:"serial_number,omitempty"`
	PartNumber         string `json:"part_number,omitempty"`
	// Feature 023. Populated is meaningful only for HardwareSchemaCurrent
	// payloads (older agents reported populated modules only and leave it
	// false).
	Populated   bool     `json:"populated,omitempty"`
	TypeDetail  []string `json:"type_detail,omitempty"` // e.g. ["Synchronous", "Registered (Buffered)"]
	ArrayHandle uint32   `json:"array_handle,omitempty"`
	AssetTag    string   `json:"asset_tag,omitempty"`
	RankCount   uint32   `json:"rank_count,omitempty"` // 0 = unknown
}

type Monitor struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
}

// --- software / OS ---

type OSInfo struct {
	Name        string    `json:"name,omitempty"`
	Version     string    `json:"version,omitempty"`
	Build       string    `json:"build,omitempty"`
	Arch        string    `json:"arch,omitempty"`
	Kernel      string    `json:"kernel,omitempty"`
	InstallDate time.Time `json:"install_date,omitempty"`
	LastBoot    time.Time `json:"last_boot,omitempty"`
	UptimeSec   uint64    `json:"uptime_sec,omitempty"`
	Family      string    `json:"family,omitempty"` // linux|windows
}

type Program struct {
	Name            string `json:"name"`
	Version         string `json:"version,omitempty"`
	Publisher       string `json:"publisher,omitempty"`
	InstallDate     string `json:"install_date,omitempty"`
	InstallLocation string `json:"install_location,omitempty"`
	SizeBytes       uint64 `json:"size_bytes,omitempty"`
	// AvailableVersion is a newer version offered by the package manager ("" =
	// none/unknown); SecurityUpdate marks it as a security update.
	AvailableVersion string `json:"available_version,omitempty"`
	SecurityUpdate   bool   `json:"security_update,omitempty"`
}

type Service struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	State       string `json:"state,omitempty"`
	StartMode   string `json:"start_mode,omitempty"`
	Account     string `json:"account,omitempty"`
}

type UserAccount struct {
	Name      string    `json:"name"`
	IsAdmin   bool      `json:"is_admin,omitempty"`
	LastLogon time.Time `json:"last_logon,omitempty"`
}

type Patch struct {
	ID          string `json:"id"`
	InstalledOn string `json:"installed_on,omitempty"`
}

type Environment struct {
	Domain    string `json:"domain,omitempty"`
	Workgroup string `json:"workgroup,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Locale    string `json:"locale,omitempty"`
}

// --- network / storage ---

type NetIface struct {
	Name         string      `json:"name"`
	MAC          string      `json:"mac,omitempty"`
	IPAddresses  []string    `json:"ip_addresses,omitempty"`
	Subnet       string      `json:"subnet,omitempty"`
	Gateway      string      `json:"gateway,omitempty"`
	DNS          []string    `json:"dns,omitempty"`
	DHCP         bool        `json:"dhcp,omitempty"`
	SpeedBps     uint64      `json:"speed_bps,omitempty"`
	Type         string      `json:"type,omitempty"` // kind, see the Iface* constants
	Up           bool        `json:"up,omitempty"`
	Addresses    []IfAddress `json:"addresses,omitempty"`
	DefaultRoute bool        `json:"default_route,omitempty"`
	Master       string      `json:"master,omitempty"` // bond/bridge master
	VLANID       uint32      `json:"vlan_id,omitempty"`
}

// Interface kinds (NetIface.Type).
const (
	IfaceEthernet = "ethernet"
	IfaceWireless = "wireless"
	IfaceBond     = "bond"
	IfaceBridge   = "bridge"
	IfaceVLAN     = "vlan"
	IfaceVirtual  = "virtual"
	IfaceLoopback = "loopback"
	IfaceOther    = "other"
)

// IfAddress is one address assigned to an interface.
type IfAddress struct {
	Address      string `json:"address"` // netip canonical form, no prefix
	PrefixLength uint32 `json:"prefix_length"`
	Family       string `json:"family"` // ipv4|ipv6
	DHCP         bool   `json:"dhcp,omitempty"`
	Temporary    bool   `json:"temporary,omitempty"`
	Deprecated   bool   `json:"deprecated,omitempty"`
	Scope        string `json:"scope,omitempty"` // global|site|link|host
}

// Virtualization is the detected virtualization role of a host.
type Virtualization struct {
	Role   string `json:"role,omitempty"` // physical|vm|container|unknown
	Kind   string `json:"kind,omitempty"`
	Source string `json:"source,omitempty"`
}

// Virtualization roles.
const (
	RolePhysical  = "physical"
	RoleVM        = "vm"
	RoleContainer = "container"
	RoleUnknown   = "unknown"
)

// Bmc is the out-of-band controller LAN configuration. It deliberately has no
// credential-bearing field (SR-005).
type Bmc struct {
	Address      string    `json:"address,omitempty"`
	PrefixLength uint32    `json:"prefix_length,omitempty"`
	Gateway      string    `json:"gateway,omitempty"`
	IPSource     string    `json:"ip_source,omitempty"` // static|dhcp|bios|other|""
	VLANID       uint32    `json:"vlan_id,omitempty"`
	Ports        []BmcPort `json:"ports,omitempty"`
}

// BmcPort is one BMC LAN channel.
type BmcPort struct {
	Channel uint32 `json:"channel"`
	MAC     string `json:"mac,omitempty"`
	Address string `json:"address,omitempty"`
}

// HypervisorGuest is a guest defined on a hypervisor host.
type HypervisorGuest struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Kind     string   `json:"kind,omitempty"`     // vm|container
	Platform string   `json:"platform,omitempty"` // proxmox
	MACs     []string `json:"macs,omitempty"`
}

// UpdateState is the host's package update state.
type UpdateState struct {
	PackageManager     string    `json:"package_manager,omitempty"`
	Status             string    `json:"status,omitempty"`            // unknown|up_to_date|updates_available|unsupported|error
	RebootRequired     string    `json:"reboot_required,omitempty"`   // unknown|true|false
	AutomaticUpdates   string    `json:"automatic_updates,omitempty"` // unknown|true|false
	SecurityClassified bool      `json:"security_classified,omitempty"`
	CheckedAt          time.Time `json:"checked_at,omitempty"`
	PendingCount       uint32    `json:"pending_count,omitempty"`
	SecurityCount      uint32    `json:"security_count,omitempty"`
}

// Update statuses.
const (
	UpdateUnknown     = "unknown"
	UpdateUpToDate    = "up_to_date"
	UpdateAvailable   = "updates_available"
	UpdateUnsupported = "unsupported"
	UpdateError       = "error"
	TriUnknown        = "unknown"
	TriTrue           = "true"
	TriFalse          = "false"
)

// CollectionLimits counts entries dropped because a bound was reached.
type CollectionLimits struct {
	Interfaces uint32 `json:"interfaces,omitempty"`
	Addresses  uint32 `json:"addresses,omitempty"`
	Guests     uint32 `json:"guests,omitempty"`
	Packages   uint32 `json:"packages,omitempty"`
	BmcPorts   uint32 `json:"bmc_ports,omitempty"`
	// Feature 023. Disks also counts dropped filesystem->disk references.
	Disks        uint32 `json:"disks,omitempty"`
	MemorySlots  uint32 `json:"memory_slots,omitempty"`
	MemoryArrays uint32 `json:"memory_arrays,omitempty"`
	Processors   uint32 `json:"processors,omitempty"`
	Filesystems  uint32 `json:"filesystems,omitempty"`
}

// Collection bounds shared by the agent, the ingest edge and the projection.
const (
	MaxInterfaces     = 256
	MaxIfaceAddresses = 64
	MaxGuests         = 1000
	MaxGuestMACs      = 32
	MaxPendingUpdates = 5000
	MaxBmcPorts       = 8

	// Hardware bounds (feature 023).
	MaxDisks        = 256
	MaxMemorySlots  = 1024
	MaxMemoryArrays = 64
	MaxProcessors   = 256
	MaxFilesystems  = 1024
	MaxFSDisks      = 64
	MaxHWString     = 256 // bytes, after control-character removal

	// HardwareSchemaCurrent marks DSP0134-correct SMBIOS decoding and
	// physical disks (feature 023); 0/1 is the legacy decoding.
	HardwareSchemaCurrent = 2
)

// Hardware availability values (HardwareAvailability.SMBIOS/Disks).
// AvailUnknown is what ingest stores for a value outside the closed set.
const (
	AvailOK          = "ok"
	AvailPartial     = "partial"
	AvailUnavailable = "unavailable"
	AvailUnsupported = "unsupported"
	AvailUnknown     = "unknown"
)

// Disk media types (closed set).
const (
	MediaSSD     = "ssd"
	MediaHDD     = "hdd"
	MediaNVMeSSD = "nvme_ssd"
	MediaUnknown = "unknown"
)

// Disk interfaces (closed set).
const (
	IfNVMe   = "nvme"
	IfSATA   = "sata"
	IfSAS    = "sas"
	IfSCSI   = "scsi"
	IfUSB    = "usb"
	IfVirtio = "virtio"
	IfHyperV = "hyperv"
	IfXen    = "xen"
	IfMMC    = "mmc"
	IfOther  = "other"
)

// Disk is a physical disk (feature 023). Agents before 023 reported mounted
// partitions grouped by device in Partitions and left the rest empty.
type Disk struct {
	Model      string      `json:"model,omitempty"`
	Serial     string      `json:"serial,omitempty"`
	SizeBytes  uint64      `json:"size_bytes,omitempty"`
	MediaType  string      `json:"media_type,omitempty"` // ssd | hdd | nvme_ssd | unknown
	Interface  string      `json:"interface,omitempty"`  // nvme | sata | sas | scsi | usb | virtio | hyperv | xen | mmc | other
	Partitions []Partition `json:"partitions,omitempty"` // legacy
	Name       string      `json:"name,omitempty"`       // sda | nvme0n1 | PhysicalDrive0
	Removable  bool        `json:"removable,omitempty"`
	Vendor     string      `json:"vendor,omitempty"`
}

// PhysicalDiskBytes is the capacity of the non-removable physical disks of
// a hardware-schema-2 payload; legacy payloads (partition groups without
// sizes) count 0.
func PhysicalDiskBytes(inv Inventory) uint64 {
	if inv.HardwareSchema < HardwareSchemaCurrent {
		return 0
	}
	var total uint64
	for _, d := range inv.Disks {
		if !d.Removable {
			total += d.SizeBytes
		}
	}
	return total
}

// Filesystem is a mounted filesystem and the physical disks (Disk.Name) it
// lives on — several for LVM or software RAID.
type Filesystem struct {
	Mount     string   `json:"mount"`
	FS        string   `json:"fs,omitempty"`
	Device    string   `json:"device,omitempty"`
	SizeBytes uint64   `json:"size_bytes,omitempty"`
	FreeBytes uint64   `json:"free_bytes,omitempty"`
	Disks     []string `json:"disks,omitempty"`
}

// HardwareAvailability says how complete the hardware collection was.
type HardwareAvailability struct {
	SMBIOS string `json:"smbios,omitempty"`
	Disks  string `json:"disks,omitempty"`
}

type Partition struct {
	Mount     string `json:"mount,omitempty"`
	FS        string `json:"fs,omitempty"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
	FreeBytes uint64 `json:"free_bytes,omitempty"`
}

// --- change history ---

type Change struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	HostID         string    `json:"host_id"`
	SnapshotID     string    `json:"snapshot_id"`
	PrevSnapshotID string    `json:"prev_snapshot_id,omitempty"`
	DetectedAt     time.Time `json:"detected_at"`
	Category       string    `json:"category"`
	ChangeType     string    `json:"change_type"`
	ComponentKey   string    `json:"component_key"`
	Before         string    `json:"before,omitempty"` // JSON
	After          string    `json:"after,omitempty"`  // JSON
}

// --- agent / enrollment ---

type Agent struct {
	ID               string    `json:"id"`
	TenantID         string    `json:"tenant_id"`
	HostID           string    `json:"host_id,omitempty"`
	CredentialSealed []byte    `json:"-"` // sealed; never serialized
	EnrolledAt       time.Time `json:"enrolled_at"`
	LastSeen         time.Time `json:"last_seen"`
	AgentVersion     string    `json:"agent_version,omitempty"`
	Revoked          bool      `json:"revoked"`
	IdentityHint     string    `json:"identity_hint,omitempty"`
	// Platform reported on StreamCommands (feature 023).
	OS             string    `json:"os,omitempty"`           // linux | windows
	Arch           string    `json:"arch,omitempty"`         // amd64 | arm64
	InstallType    string    `json:"install_type,omitempty"` // deb | rpm | binary
	Capabilities   []string  `json:"capabilities,omitempty"` // e.g. upgrade.v1
	PlatformSeenAt time.Time `json:"platform_seen_at"`
	// How the agent enrolled (feature 029): EnrolledViaToken or
	// EnrolledViaAuto with the public id of the auto-enrollment key.
	EnrolledVia     string `json:"enrolled_via,omitempty"`
	AutoEnrollKeyID string `json:"auto_enroll_key_id,omitempty"`
}

// Enrollment methods of an agent.
const (
	EnrolledViaToken = "token"
	EnrolledViaAuto  = "auto"
)

// HasCapability reports whether the agent announced capability c.
func (a Agent) HasCapability(c string) bool {
	for _, x := range a.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// Agent capabilities.
const (
	CapUpgradeV1 = "upgrade.v1"
	// CapCertV1: the agent installs delivered certificates (feature 033).
	CapCertV1 = "cert.v1"
)

// --- agent releases and upgrades (feature 023) ---

// AgentRelease is a signed agent release stored in the database (global:
// public binaries, not tenant data).
type AgentRelease struct {
	Version        string          `json:"version"`
	Manifest       []byte          `json:"-"` // exact signed bytes
	Signature      []byte          `json:"-"` // 64-byte Ed25519
	KeyID          string          `json:"key_id"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	Source         string          `json:"source"` // bundled | import
	ImportedAt     time.Time       `json:"imported_at"`
	Artifacts      []AgentArtifact `json:"artifacts,omitempty"`
}

// Release sources.
const (
	ReleaseBundled = "bundled"
	ReleaseImport  = "import"
)

// AgentArtifact is one platform artifact of a release.
type AgentArtifact struct {
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	InstallType string `json:"install_type"`
	File        string `json:"file"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Complete    bool   `json:"complete"`
}

// AgentUpgrade is a persisted upgrade request (tenant-scoped).
type AgentUpgrade struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"-"`
	AgentID        string     `json:"agent_id"`
	HostID         string     `json:"host_id,omitempty"`
	FromVersion    string     `json:"from_version"`
	TargetVersion  string     `json:"target_version"`
	AllowDowngrade bool       `json:"allow_downgrade"`
	State          string     `json:"state"`
	Origin         string     `json:"origin"`
	RequestedBy    string     `json:"requested_by"`
	Reason         string     `json:"reason,omitempty"`
	Attempts       int        `json:"attempts"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// Upgrade request states.
const (
	UpgradePending     = "pending"
	UpgradeDelivered   = "delivered"
	UpgradeDownloading = "downloading"
	UpgradeInstalling  = "installing"
	UpgradeSucceeded   = "succeeded"
	UpgradeFailed      = "failed"
	UpgradeRolledBack  = "rolled_back"
	UpgradeExpired     = "expired"
	UpgradeCancelled   = "cancelled"
)

// Active reports whether the request still occupies the agent (the partial
// unique index agent_upgrades_one_active).
func (u AgentUpgrade) Active() bool {
	switch u.State {
	case UpgradePending, UpgradeDelivered, UpgradeDownloading, UpgradeInstalling:
		return true
	}
	return false
}

// Upgrade request origins.
const (
	OriginUser   = "user"
	OriginPolicy = "policy"
	OriginAgent  = "agent"
)

// AgentUpgradePolicy is a tenant's automatic upgrade policy (one row per
// tenant; defaults apply when there is none).
type AgentUpgradePolicy struct {
	TenantID      string    `json:"-"`
	Enabled       bool      `json:"enabled"`
	WindowStart   string    `json:"window_start"`
	WindowEnd     string    `json:"window_end"`
	Timezone      string    `json:"timezone"`
	MaxConcurrent int       `json:"max_concurrent"`
	TargetVersion string    `json:"target_version"`
	Paused        bool      `json:"paused"`
	PausedReason  string    `json:"paused_reason"`
	UpdatedBy     string    `json:"updated_by"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DefaultUpgradePolicy is the policy of a tenant without a stored row:
// automatic upgrades off.
func DefaultUpgradePolicy(tenantID string) AgentUpgradePolicy {
	return AgentUpgradePolicy{TenantID: tenantID, WindowStart: "02:00", WindowEnd: "04:00", Timezone: "UTC", MaxConcurrent: 5}
}

type EnrollmentToken struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenant_id"`
	TokenHash string     `json:"-"` // hash only; secret returned once at mint
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	Revoked   bool       `json:"revoked"`
	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Label     string     `json:"label,omitempty"`
}

// --- automatic enrollment (feature 029) ---

// AutoEnrollSettings is a tenant's master switch for automatic enrollment.
type AutoEnrollSettings struct {
	TenantID  string    `json:"tenant_id"`
	Enabled   bool      `json:"enabled"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AutoEnrollKey is a reusable key agents prove possession of to enroll into
// the key's tenant from its allowed networks. The secret is sealed and never
// serialized; KeyID is the public identifier agents send.
type AutoEnrollKey struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	KeyID          string     `json:"key_id"`
	Name           string     `json:"name"`
	SecretSealed   []byte     `json:"-"`
	AllowedCIDRs   []string   `json:"allowed_cidrs"`
	Enabled        bool       `json:"enabled"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	MaxEnrollments int        `json:"max_enrollments"` // 0 = unlimited
	Enrollments    int        `json:"enrollments"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP     string     `json:"last_used_ip,omitempty"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Expired reports whether the key has an expiry at or before now.
func (k AutoEnrollKey) Expired(now time.Time) bool {
	return k.ExpiresAt != nil && !now.Before(*k.ExpiresAt)
}

// Exhausted reports whether the key reached its enrollment limit.
func (k AutoEnrollKey) Exhausted() bool {
	return k.MaxEnrollments > 0 && k.Enrollments >= k.MaxEnrollments
}

// AutoEnrollment is one accepted automatic enrollment, applied atomically:
// the nonce is recorded (older nonces of the key are pruned), the key's
// counters are advanced if it is still usable and the tenant switch is on,
// the agent is created and the audit row written.
type AutoEnrollment struct {
	KeyUUID     string // AutoEnrollKey.ID
	KeyID       string // public key id
	TenantID    string
	Nonce       string
	At          time.Time
	PruneBefore time.Time // nonces seen before this are dropped
	IP          string
	Agent       Agent
	Audit       AuditRow
}

// --- certificate delivery (feature 033) ---

// CertDelivery is one delivery request of a mesh caller (the deployer): a
// reference to an lcm certificate, a certificate name and a host selection.
// No delivery, item or host certificate row ever holds certificate or key
// material (data-model.md).
type CertDelivery struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"-"`
	Source          string    `json:"source"`          // SPIFFE service name, e.g. "deployer"
	IdempotencyKey  string    `json:"idempotency_key"` // deployer job id (unique per tenant+source)
	ConfigurationID string    `json:"configuration_id,omitempty"`
	TargetID        string    `json:"target_id,omitempty"` // "" for direct jobs
	Trigger         string    `json:"trigger"`             // manual | auto_deploy | retry
	CertificateID   string    `json:"certificate_id"`      // lcm certificate id
	Name            string    `json:"name"`                // certmaterial.ValidName
	KeyPolicy       string    `json:"key_policy"`          // require | certificate_only
	HostIDs         []string  `json:"host_ids,omitempty"`  // requested explicit ids (<= MaxDeliveryHosts)
	HostTags        []string  `json:"host_tags,omitempty"` // requested selectors (<= MaxHostTags)
	RequestedBy     string    `json:"requested_by"`        // SPIFFE id of the caller
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"` // min(created + pending_ttl, certificate not_after)
}

// CertDeliveryItem is the delivery of a CertDelivery to one host. Its id is
// the only identifier the agent sees.
type CertDeliveryItem struct {
	ID                string     `json:"id"`
	TenantID          string     `json:"-"`
	DeliveryID        string     `json:"delivery_id"`
	HostID            string     `json:"host_id"`
	AgentID           string     `json:"agent_id,omitempty"` // "" when the host has no agent
	Name              string     `json:"name"`               // copy of the delivery name (one active item per host+name)
	CertificateID     string     `json:"certificate_id"`
	State             string     `json:"state"`
	Reason            string     `json:"reason,omitempty"`
	Attempts          int        `json:"attempts"` // 1..MaxItemAttempts (re-arms)
	Fetches           int        `json:"fetches"`  // 0..MaxItemFetches per attempt
	RerunHook         bool       `json:"rerun_hook,omitempty"`
	Serial            string     `json:"serial,omitempty"`
	FingerprintSHA256 string     `json:"fingerprint_sha256,omitempty"`
	CommonName        string     `json:"common_name,omitempty"` // leaf subject CN served at fetch (<= MaxCommonNameBytes)
	NotAfter          *time.Time `json:"not_after,omitempty"`
	HookExitCode      *int       `json:"hook_exit_code,omitempty"`
	Detail            string     `json:"detail,omitempty"` // <= MaxDetailBytes, sanitised
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	DeliveredAt       *time.Time `json:"delivered_at,omitempty"`
	FetchedAt         *time.Time `json:"fetched_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

// Active reports whether the item still waits for its agent (the partial
// unique index inventory_cert_items_active_uq: one active item per host and
// name).
func (i CertDeliveryItem) Active() bool { return CertDeliveryActive(i.State) }

// CertDeliveryActive reports whether state is pending, delivered or fetched.
func CertDeliveryActive(state string) bool {
	switch state {
	case DeliveryPending, DeliveryDelivered, DeliveryFetched:
		return true
	}
	return false
}

// HostCertificate is the current certificate of a host under a name (one
// row per host and name), updated from the agents' reports.
type HostCertificate struct {
	TenantID          string     `json:"-"`
	HostID            string     `json:"host_id"`
	Name              string     `json:"name"`
	CertificateID     string     `json:"certificate_id"`
	ConfigurationID   string     `json:"configuration_id,omitempty"`
	CommonName        string     `json:"common_name,omitempty"`
	Serial            string     `json:"serial,omitempty"`
	FingerprintSHA256 string     `json:"fingerprint_sha256,omitempty"`
	NotAfter          *time.Time `json:"not_after,omitempty"`
	State             string     `json:"state"` // last terminal item state
	Reason            string     `json:"reason,omitempty"`
	HookExitCode      *int       `json:"hook_exit_code,omitempty"`
	LastItemID        string     `json:"last_item_id"`
	LastDeliveredAt   *time.Time `json:"last_delivered_at,omitempty"` // last installed/unchanged
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`        // lcm revoked the certificate installed
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Delivery item states (closed set).
const (
	DeliveryPending     = "pending"     // created, waiting for the agent
	DeliveryDelivered   = "delivered"   // CERTIFICATE command pushed at least once
	DeliveryFetched     = "fetched"     // material served to the agent
	DeliveryInstalled   = "installed"   // terminal: written (+ hook ok or no hook)
	DeliveryUnchanged   = "unchanged"   // terminal: same fingerprint already installed
	DeliveryFailed      = "failed"      // terminal (re-armable)
	DeliveryHookFailed  = "hook_failed" // terminal (re-armable): files installed, hook failed
	DeliveryUnsupported = "unsupported" // terminal: no agent / no cert.v1 / platform
	DeliverySuperseded  = "superseded"  // terminal: newer item for the same host+name
	DeliveryExpired     = "expired"     // terminal (re-armable)
	DeliveryCancelled   = "cancelled"   // terminal: user, revocation, host/agent removed
)

// DeliveryStates lists every delivery item state.
var DeliveryStates = []string{DeliveryPending, DeliveryDelivered, DeliveryFetched, DeliveryInstalled, DeliveryUnchanged,
	DeliveryFailed, DeliveryHookFailed, DeliveryUnsupported, DeliverySuperseded, DeliveryExpired, DeliveryCancelled}

// Delivery reason codes (closed set): reported by the agent ...
const (
	ReasonInvalidName         = "invalid_name"
	ReasonInvalidBundle       = "invalid_bundle"
	ReasonKeyMismatch         = "key_mismatch"
	ReasonCertificateNotValid = "certificate_not_valid"
	ReasonOwnerUnknown        = "owner_unknown"
	ReasonWriteFailed         = "write_failed"
	ReasonDiskFull            = "disk_full"
	ReasonHookFailed          = "hook_failed"
	ReasonHookTimeout         = "hook_timeout"
	ReasonHookRefused         = "hook_refused"
	ReasonDisabledLocally     = "disabled_locally"
	ReasonBusy                = "busy"
)

// ... or set by the server.
const (
	ReasonKeyUnavailable      = "key_unavailable"
	ReasonCertificateRevoked  = "certificate_revoked"
	ReasonCertificateExpired  = "certificate_expired"
	ReasonCertificateNotFound = "certificate_not_found"
	ReasonLCMUnavailable      = "lcm_unavailable"
	ReasonBundleTooLarge      = "bundle_too_large"
	ReasonFingerprintMismatch = "fingerprint_mismatch"
	ReasonNoAgent             = "no_agent"
	ReasonNoCapability        = "no_capability"
	ReasonPlatform            = "platform"
	ReasonHostRetired         = "host_retired"
	ReasonAgentRevoked        = "agent_revoked"
	ReasonHostDeleted         = "host_deleted"
	ReasonNoReport            = "no_report"
	ReasonExpired             = "expired"
	ReasonOlderThanInstalled  = "older_than_installed"
	ReasonCancelledByUser     = "cancelled_by_user"
	ReasonUnknownHost         = "unknown_host"
)

// AgentDeliveryReasons are the reason codes an agent may report.
var AgentDeliveryReasons = []string{ReasonInvalidName, ReasonInvalidBundle, ReasonKeyMismatch, ReasonCertificateNotValid,
	ReasonOwnerUnknown, ReasonWriteFailed, ReasonDiskFull, ReasonHookFailed, ReasonHookTimeout, ReasonHookRefused,
	ReasonDisabledLocally, ReasonBusy}

// Delivery key policies.
const (
	KeyPolicyRequire         = "require"
	KeyPolicyCertificateOnly = "certificate_only"
)

// Delivery triggers (from the deployer job).
const (
	TriggerManual     = "manual"
	TriggerAutoDeploy = "auto_deploy"
	TriggerRetry      = "retry"
)

// Certificate delivery bounds (research D17).
const (
	MaxDeliveryHosts    = 1000
	MaxHostTags         = 16
	MaxItemAttempts     = 5
	MaxItemFetches      = 5
	MaxReplayPerConnect = 50
	MaxDetailBytes      = 256
	MaxCommonNameBytes  = 256 // item and host certificate common_name
)

// --- audit ---

type AuditRow struct {
	ID          string
	TenantID    string
	At          time.Time
	ActorKind   string
	ActorID     string
	Action      string
	SubjectKind string
	SubjectID   string
	Outcome     string
	Reason      string
	Detail      map[string]any
}

// --- filters ---

// HostFilter constrains ListHosts. Empty fields match all.
type HostFilter struct {
	Hostname     string // case-insensitive substring, at most MaxSearchLen characters
	OSName       string
	Manufacturer string
	Status       string
	Tag          string // "key" or "key=value"
	LastSeenFrom *time.Time
	LastSeenTo   *time.Time
	Limit        int
	CursorID     string
}

// MaxSearchLen caps the free-text hostname filter (in characters): a longer
// value is rejected as invalid instead of being scanned against every row.
const MaxSearchLen = 200

// ReportCursor is the keyset position (report_changed_at, host id) of the
// last host row returned by a host report listing. A zero ChangedAt stands
// for a host whose report change time was never recorded (sorted first).
type ReportCursor struct {
	ChangedAt time.Time
	ID        string
}

// ReportRowFilter constrains ListHostReportRows. A zero ChangedSince lists
// every host of the tenant (including never-reported and retired ones);
// otherwise only hosts whose report changed strictly after it.
type ReportRowFilter struct {
	ChangedSince time.Time
	After        *ReportCursor
	Limit        int // <= 0: no limit
}
