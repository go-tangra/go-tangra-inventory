package inventoryclient

import (
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// Inventory is a snapshot's full collected payload as plain Go values.
type Inventory struct {
	Identity     Identity
	CollectedAt  time.Time
	AgentVersion string
	OS           OSInfo
	BIOS         BIOSInfo
	System       SystemInfo
	Baseboard    BaseboardInfo
	Chassis      ChassisInfo
	Processors   []Processor
	Cache        []CacheInfo
	Memory       MemoryInfo
	Ports        []string
	Slots        []string
	OEMStrings   []string
	BIOSLanguage string
	Monitors     []Monitor
	Programs     []Program
	Services     []ServiceInfo
	Users        []UserAccount
	Patches      []Patch
	Environment  Environment
	Networks     []NetworkInterface
	Disks        []Disk
	// 020: host report fields (zero values from older agents).
	PrimaryIPv4    string
	PrimaryIPv6    string
	Virtualization Virtualization
	BMC            *BMC // nil = no BMC or not readable
	Guests         []HypervisorGuest
	Updates        UpdateState
	Truncated      CollectionLimits
}

type Identity struct {
	HardwareUUID string
	MachineID    string
	Hostname     string
}

type OSInfo struct {
	Name        string
	Version     string
	Build       string
	Arch        string
	Kernel      string
	InstallDate time.Time
	LastBoot    time.Time
	UptimeSec   uint64
	Family      string // "linux" | "windows" ("" = old agent)
}

type BIOSInfo struct {
	Vendor      string
	Version     string
	ReleaseDate string
}

type SystemInfo struct {
	Manufacturer string
	ProductName  string
	Version      string
	SerialNumber string
	UUID         string
	WakeUpType   string
	SKUNumber    string
	Family       string
}

type BaseboardInfo struct {
	Manufacturer      string
	Product           string
	Version           string
	SerialNumber      string
	AssetTag          string
	LocationInChassis string
	BoardType         string
}

type ChassisInfo struct {
	Manufacturer string
	Version      string
	SerialNumber string
	AssetTag     string
	SKUNumber    string
	Type         string
}

type Processor struct {
	SocketDesignation string
	Manufacturer      string
	Version           string
	MaxSpeedMHz       uint32
	CurrentSpeedMHz   uint32
	CoreCount         uint32
	CoreEnabled       uint32
	ThreadCount       uint32
	PartNumber        string
	SerialNumber      string
	SocketPopulated   bool
}

type CacheInfo struct {
	SocketDesignation string
}

type MemoryInfo struct {
	TotalPhysicalBytes uint64
	Array              MemoryArray
	Modules            []MemoryModule
}

type MemoryArray struct {
	Location        string
	Use             string
	ErrorCorrection string
	MaximumCapacity uint64
	NumberOfDevices uint32
}

type MemoryModule struct {
	DeviceLocator      string
	BankLocator        string
	CapacityBytes      uint64
	FormFactor         string
	MemoryType         string
	SpeedMTs           uint32
	ConfiguredSpeedMTs uint32
	Manufacturer       string
	SerialNumber       string
	PartNumber         string
}

type Monitor struct {
	Manufacturer string
	Model        string
	SerialNumber string
}

type Program struct {
	Name            string
	Version         string
	Publisher       string
	InstallDate     string
	InstallLocation string
	SizeBytes       uint64
	// AvailableVersion is a newer version available from the package
	// manager ("" = none/unknown); SecurityUpdate marks it as a security update.
	AvailableVersion string
	SecurityUpdate   bool
}

type ServiceInfo struct {
	Name        string
	DisplayName string
	State       string
	StartMode   string
	Account     string
}

type UserAccount struct {
	Name      string
	IsAdmin   bool
	LastLogon time.Time
}

type Patch struct {
	ID          string
	InstalledOn string
}

type Environment struct {
	Domain    string
	Workgroup string
	Timezone  string
	Locale    string
}

type NetworkInterface struct {
	Name        string
	MAC         string
	IPAddresses []string
	Subnet      string
	Gateway     string
	DNS         []string
	DHCP        bool
	SpeedBps    uint64
	Type        string // kind: ethernet|wireless|bond|bridge|vlan|virtual|loopback|other
	Up          bool
	Addresses   []InterfaceAddress
	// DefaultRoute is true when the interface carries a default route.
	DefaultRoute bool
	Master       string // bond or bridge this interface is enslaved to
	VLANID       uint32
}

// InterfaceAddress is one address of an interface with its prefix and flags.
type InterfaceAddress struct {
	Address      string // canonical text, no prefix
	PrefixLength uint32
	Family       string // "ipv4" | "ipv6"
	DHCP         bool
	Temporary    bool
	Deprecated   bool
	Scope        string // "global" | "site" | "link" | "host"
}

// Virtualization is the detected virtualization role and kind.
type Virtualization struct {
	Role   string // physical|vm|container|unknown ("" = old agent)
	Kind   string
	Source string
}

// BMC is the out-of-band controller's LAN configuration. It deliberately has
// no credential field.
type BMC struct {
	Address      string
	PrefixLength uint32
	Gateway      string
	IPSource     string // static|dhcp|bios|other|""
	VLANID       uint32
	Ports        []BMCPort
}

// BMCPort is one BMC LAN channel.
type BMCPort struct {
	Channel uint32
	MAC     string
	Address string
}

// HypervisorGuest is a guest defined on a hypervisor host.
type HypervisorGuest struct {
	ID       string
	Name     string
	Kind     string // vm|container
	Platform string // proxmox
	MACs     []string
}

// UpdateState is a host's package update state.
type UpdateState struct {
	PackageManager     string
	Status             string // unknown|up_to_date|updates_available|unsupported|error ("" = old agent)
	RebootRequired     string // unknown|true|false
	AutomaticUpdates   string // unknown|true|false
	SecurityClassified bool
	CheckedAt          time.Time
	PendingCount       uint32
	SecurityCount      uint32
}

// CollectionLimits counts entries dropped because a bound was reached.
type CollectionLimits struct {
	Interfaces uint32
	Addresses  uint32
	Guests     uint32
	Packages   uint32
	BMCPorts   uint32
}

type Disk struct {
	Model      string
	Serial     string
	SizeBytes  uint64
	MediaType  string
	Interface  string
	Partitions []Partition
}

type Partition struct {
	Mount     string
	FS        string
	SizeBytes uint64
	FreeBytes uint64
}

// toInventory maps the wire Inventory message to plain Go values.
func toInventory(pb *invv1.Inventory) *Inventory {
	inv := &Inventory{
		CollectedAt:  unixTime(pb.GetCollectedAt()),
		AgentVersion: pb.GetAgentVersion(),
		Ports:        pb.GetPorts(),
		Slots:        pb.GetSlots(),
		OEMStrings:   pb.GetOemStrings(),
		BIOSLanguage: pb.GetBiosLanguage(),
	}
	if id := pb.GetIdentity(); id != nil {
		inv.Identity = Identity{HardwareUUID: id.GetHardwareUuid(), MachineID: id.GetMachineId(), Hostname: id.GetHostname()}
	}
	if os := pb.GetOs(); os != nil {
		inv.OS = OSInfo{
			Name: os.GetName(), Version: os.GetVersion(), Build: os.GetBuild(), Arch: os.GetArch(),
			Kernel: os.GetKernel(), InstallDate: unixTime(os.GetInstallDate()), LastBoot: unixTime(os.GetLastBoot()),
			UptimeSec: os.GetUptimeSec(), Family: os.GetFamily(),
		}
	}
	if b := pb.GetBios(); b != nil {
		inv.BIOS = BIOSInfo{Vendor: b.GetVendor(), Version: b.GetVersion(), ReleaseDate: b.GetReleaseDate()}
	}
	if sy := pb.GetSystem(); sy != nil {
		inv.System = SystemInfo{
			Manufacturer: sy.GetManufacturer(), ProductName: sy.GetProductName(), Version: sy.GetVersion(),
			SerialNumber: sy.GetSerialNumber(), UUID: sy.GetUuid(), WakeUpType: sy.GetWakeUpType(),
			SKUNumber: sy.GetSkuNumber(), Family: sy.GetFamily(),
		}
	}
	if bb := pb.GetBaseboard(); bb != nil {
		inv.Baseboard = BaseboardInfo{
			Manufacturer: bb.GetManufacturer(), Product: bb.GetProduct(), Version: bb.GetVersion(),
			SerialNumber: bb.GetSerialNumber(), AssetTag: bb.GetAssetTag(),
			LocationInChassis: bb.GetLocationInChassis(), BoardType: bb.GetBoardType(),
		}
	}
	if ch := pb.GetChassis(); ch != nil {
		inv.Chassis = ChassisInfo{
			Manufacturer: ch.GetManufacturer(), Version: ch.GetVersion(), SerialNumber: ch.GetSerialNumber(),
			AssetTag: ch.GetAssetTag(), SKUNumber: ch.GetSkuNumber(), Type: ch.GetType(),
		}
	}
	if m := pb.GetMemory(); m != nil {
		inv.Memory = MemoryInfo{TotalPhysicalBytes: m.GetTotalPhysicalBytes()}
		if a := m.GetArray(); a != nil {
			inv.Memory.Array = MemoryArray{
				Location: a.GetLocation(), Use: a.GetUse(), ErrorCorrection: a.GetErrorCorrection(),
				MaximumCapacity: a.GetMaximumCapacity(), NumberOfDevices: a.GetNumberOfDevices(),
			}
		}
		for _, mod := range m.GetModules() {
			inv.Memory.Modules = append(inv.Memory.Modules, MemoryModule{
				DeviceLocator: mod.GetDeviceLocator(), BankLocator: mod.GetBankLocator(), CapacityBytes: mod.GetCapacityBytes(),
				FormFactor: mod.GetFormFactor(), MemoryType: mod.GetMemoryType(), SpeedMTs: mod.GetSpeedMtS(),
				ConfiguredSpeedMTs: mod.GetConfiguredSpeedMtS(), Manufacturer: mod.GetManufacturer(),
				SerialNumber: mod.GetSerialNumber(), PartNumber: mod.GetPartNumber(),
			})
		}
	}
	if e := pb.GetEnvironment(); e != nil {
		inv.Environment = Environment{Domain: e.GetDomain(), Workgroup: e.GetWorkgroup(), Timezone: e.GetTimezone(), Locale: e.GetLocale()}
	}
	for _, p := range pb.GetProcessors() {
		inv.Processors = append(inv.Processors, Processor{
			SocketDesignation: p.GetSocketDesignation(), Manufacturer: p.GetManufacturer(), Version: p.GetVersion(),
			MaxSpeedMHz: p.GetMaxSpeedMhz(), CurrentSpeedMHz: p.GetCurrentSpeedMhz(), CoreCount: p.GetCoreCount(),
			CoreEnabled: p.GetCoreEnabled(), ThreadCount: p.GetThreadCount(), PartNumber: p.GetPartNumber(),
			SerialNumber: p.GetSerialNumber(), SocketPopulated: p.GetSocketPopulated(),
		})
	}
	for _, c := range pb.GetCache() {
		inv.Cache = append(inv.Cache, CacheInfo{SocketDesignation: c.GetSocketDesignation()})
	}
	for _, m := range pb.GetMonitors() {
		inv.Monitors = append(inv.Monitors, Monitor{Manufacturer: m.GetManufacturer(), Model: m.GetModel(), SerialNumber: m.GetSerialNumber()})
	}
	for _, p := range pb.GetInstalledPrograms() {
		inv.Programs = append(inv.Programs, Program{
			Name: p.GetName(), Version: p.GetVersion(), Publisher: p.GetPublisher(), InstallDate: p.GetInstallDate(),
			InstallLocation: p.GetInstallLocation(), SizeBytes: p.GetSizeBytes(),
			AvailableVersion: p.GetAvailableVersion(), SecurityUpdate: p.GetSecurityUpdate(),
		})
	}
	for _, sv := range pb.GetServices() {
		inv.Services = append(inv.Services, ServiceInfo{
			Name: sv.GetName(), DisplayName: sv.GetDisplayName(), State: sv.GetState(),
			StartMode: sv.GetStartMode(), Account: sv.GetAccount(),
		})
	}
	for _, u := range pb.GetUsers() {
		inv.Users = append(inv.Users, UserAccount{Name: u.GetName(), IsAdmin: u.GetIsAdmin(), LastLogon: unixTime(u.GetLastLogon())})
	}
	for _, p := range pb.GetPatches() {
		inv.Patches = append(inv.Patches, Patch{ID: p.GetId(), InstalledOn: p.GetInstalledOn()})
	}
	inv.Networks = toInterfaces(pb.GetNetworkInterfaces())
	inv.PrimaryIPv4 = pb.GetPrimaryIpv4()
	inv.PrimaryIPv6 = pb.GetPrimaryIpv6()
	inv.Virtualization = toVirtualization(pb.GetVirtualization())
	inv.BMC = toBMC(pb.GetBmc())
	inv.Guests = toGuests(pb.GetHypervisorGuests())
	inv.Updates = toUpdateState(pb.GetUpdateState())
	inv.Truncated = toLimits(pb.GetTruncated())
	for _, d := range pb.GetDisks() {
		disk := Disk{
			Model: d.GetModel(), Serial: d.GetSerial(), SizeBytes: d.GetSizeBytes(),
			MediaType: d.GetMediaType(), Interface: d.GetInterface(),
		}
		for _, part := range d.GetPartitions() {
			disk.Partitions = append(disk.Partitions, Partition{
				Mount: part.GetMount(), FS: part.GetFs(), SizeBytes: part.GetSizeBytes(), FreeBytes: part.GetFreeBytes(),
			})
		}
		inv.Disks = append(inv.Disks, disk)
	}
	return inv
}

func toInterfaces(in []*invv1.NetworkInterface) []NetworkInterface {
	var out []NetworkInterface
	for _, n := range in {
		ni := NetworkInterface{
			Name: n.GetName(), MAC: n.GetMac(), IPAddresses: n.GetIpAddresses(), Subnet: n.GetSubnet(),
			Gateway: n.GetGateway(), DNS: n.GetDns(), DHCP: n.GetDhcp(), SpeedBps: n.GetSpeedBps(),
			Type: n.GetType(), Up: n.GetUp(), DefaultRoute: n.GetDefaultRoute(), Master: n.GetMaster(),
			VLANID: n.GetVlanId(),
		}
		for _, a := range n.GetAddresses() {
			ni.Addresses = append(ni.Addresses, InterfaceAddress{
				Address: a.GetAddress(), PrefixLength: a.GetPrefixLength(), Family: a.GetFamily(),
				DHCP: a.GetDhcp(), Temporary: a.GetTemporary(), Deprecated: a.GetDeprecated(), Scope: a.GetScope(),
			})
		}
		out = append(out, ni)
	}
	return out
}

func toVirtualization(v *invv1.Virtualization) Virtualization {
	return Virtualization{Role: v.GetRole(), Kind: v.GetKind(), Source: v.GetSource()}
}

func toBMC(b *invv1.Bmc) *BMC {
	if b == nil {
		return nil
	}
	out := &BMC{Address: b.GetAddress(), PrefixLength: b.GetPrefixLength(), Gateway: b.GetGateway(),
		IPSource: b.GetIpSource(), VLANID: b.GetVlanId()}
	for _, p := range b.GetPorts() {
		out.Ports = append(out.Ports, BMCPort{Channel: p.GetChannel(), MAC: p.GetMac(), Address: p.GetAddress()})
	}
	return out
}

func toGuests(in []*invv1.HypervisorGuest) []HypervisorGuest {
	var out []HypervisorGuest
	for _, g := range in {
		out = append(out, HypervisorGuest{ID: g.GetId(), Name: g.GetName(), Kind: g.GetKind(),
			Platform: g.GetPlatform(), MACs: g.GetMacs()})
	}
	return out
}

func toUpdateState(u *invv1.UpdateState) UpdateState {
	return UpdateState{
		PackageManager: u.GetPackageManager(), Status: u.GetStatus(), RebootRequired: u.GetRebootRequired(),
		AutomaticUpdates: u.GetAutomaticUpdates(), SecurityClassified: u.GetSecurityClassified(),
		CheckedAt: unixTime(u.GetCheckedAt()), PendingCount: u.GetPendingCount(), SecurityCount: u.GetSecurityCount(),
	}
}

func toLimits(l *invv1.CollectionLimits) CollectionLimits {
	return CollectionLimits{Interfaces: l.GetInterfaces(), Addresses: l.GetAddresses(), Guests: l.GetGuests(),
		Packages: l.GetPackages(), BMCPorts: l.GetBmcPorts()}
}
