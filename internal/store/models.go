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
	Type         string `json:"type,omitempty"`
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
}

type CacheInfo struct {
	SocketDesignation string `json:"socket_designation,omitempty"`
}

type MemoryInfo struct {
	TotalPhysicalBytes uint64         `json:"total_physical_bytes,omitempty"`
	Array              MemoryArray    `json:"array"`
	Modules            []MemoryModule `json:"modules,omitempty"`
}

type MemoryArray struct {
	Location        string `json:"location,omitempty"`
	Use             string `json:"use,omitempty"`
	ErrorCorrection string `json:"error_correction,omitempty"`
	MaximumCapacity uint64 `json:"maximum_capacity,omitempty"`
	NumberOfDevices uint32 `json:"number_of_devices,omitempty"`
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
}

// Collection bounds shared by the agent, the ingest edge and the projection.
const (
	MaxInterfaces     = 256
	MaxIfaceAddresses = 64
	MaxGuests         = 1000
	MaxGuestMACs      = 32
	MaxPendingUpdates = 5000
	MaxBmcPorts       = 8
)

type Disk struct {
	Model      string      `json:"model,omitempty"`
	Serial     string      `json:"serial,omitempty"`
	SizeBytes  uint64      `json:"size_bytes,omitempty"`
	MediaType  string      `json:"media_type,omitempty"`
	Interface  string      `json:"interface,omitempty"`
	Partitions []Partition `json:"partitions,omitempty"`
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
	Hostname     string
	OSName       string
	Manufacturer string
	Status       string
	Tag          string // "key" or "key=value"
	LastSeenFrom *time.Time
	LastSeenTo   *time.Time
	Limit        int
	CursorID     string
}

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
