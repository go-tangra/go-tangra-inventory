package inventoryclient

import (
	"time"

	invv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
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
	Type        string
	Up          bool
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
			UptimeSec: os.GetUptimeSec(),
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
	for _, n := range pb.GetNetworkInterfaces() {
		inv.Networks = append(inv.Networks, NetworkInterface{
			Name: n.GetName(), MAC: n.GetMac(), IPAddresses: n.GetIpAddresses(), Subnet: n.GetSubnet(),
			Gateway: n.GetGateway(), DNS: n.GetDns(), DHCP: n.GetDhcp(), SpeedBps: n.GetSpeedBps(),
			Type: n.GetType(), Up: n.GetUp(),
		})
	}
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
