package invpb

import (
	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Hardware mappers (BIOS, system, baseboard, chassis, processors, memory,
// disks, filesystems, availability) shared by the agent sender, the ingest
// edge, the mesh API and the host report projection (feature 023 extended
// them with the DSP0134 fields, every memory slot and physical disks).
// Singletons always map to a message, as agents always send them; empty
// lists map to nil.

// BIOSToPB maps the BIOS.
func BIOSToPB(b store.BIOSInfo) *invv1.BIOSInfo {
	return &invv1.BIOSInfo{Vendor: b.Vendor, Version: b.Version, ReleaseDate: b.ReleaseDate}
}

// BIOSFromPB maps the wire BIOS.
func BIOSFromPB(b *invv1.BIOSInfo) store.BIOSInfo {
	return store.BIOSInfo{Vendor: b.GetVendor(), Version: b.GetVersion(), ReleaseDate: b.GetReleaseDate()}
}

// SystemToPB maps the system information.
func SystemToPB(s store.SystemInfo) *invv1.SystemInfo {
	return &invv1.SystemInfo{
		Manufacturer: s.Manufacturer, ProductName: s.ProductName, Version: s.Version, SerialNumber: s.SerialNumber,
		Uuid: s.UUID, WakeUpType: s.WakeUpType, SkuNumber: s.SKUNumber, Family: s.Family,
	}
}

// SystemFromPB maps the wire system information.
func SystemFromPB(s *invv1.SystemInfo) store.SystemInfo {
	return store.SystemInfo{
		Manufacturer: s.GetManufacturer(), ProductName: s.GetProductName(), Version: s.GetVersion(),
		SerialNumber: s.GetSerialNumber(), UUID: s.GetUuid(), WakeUpType: s.GetWakeUpType(),
		SKUNumber: s.GetSkuNumber(), Family: s.GetFamily(),
	}
}

// BaseboardToPB maps the baseboard.
func BaseboardToPB(b store.BaseboardInfo) *invv1.BaseboardInfo {
	return &invv1.BaseboardInfo{
		Manufacturer: b.Manufacturer, Product: b.Product, Version: b.Version, SerialNumber: b.SerialNumber,
		AssetTag: b.AssetTag, LocationInChassis: b.LocationInChassis, BoardType: b.BoardType,
	}
}

// BaseboardFromPB maps the wire baseboard.
func BaseboardFromPB(b *invv1.BaseboardInfo) store.BaseboardInfo {
	return store.BaseboardInfo{
		Manufacturer: b.GetManufacturer(), Product: b.GetProduct(), Version: b.GetVersion(),
		SerialNumber: b.GetSerialNumber(), AssetTag: b.GetAssetTag(),
		LocationInChassis: b.GetLocationInChassis(), BoardType: b.GetBoardType(),
	}
}

// ChassisToPB maps the chassis.
func ChassisToPB(c store.ChassisInfo) *invv1.ChassisInfo {
	return &invv1.ChassisInfo{
		Manufacturer: c.Manufacturer, Version: c.Version, SerialNumber: c.SerialNumber, AssetTag: c.AssetTag,
		SkuNumber: c.SKUNumber, Type: c.Type, BootupState: c.BootupState,
	}
}

// ChassisFromPB maps the wire chassis.
func ChassisFromPB(c *invv1.ChassisInfo) store.ChassisInfo {
	return store.ChassisInfo{
		Manufacturer: c.GetManufacturer(), Version: c.GetVersion(), SerialNumber: c.GetSerialNumber(),
		AssetTag: c.GetAssetTag(), SKUNumber: c.GetSkuNumber(), Type: c.GetType(), BootupState: c.GetBootupState(),
	}
}

// ProcessorsToPB maps processors.
func ProcessorsToPB(in []store.Processor) []*invv1.Processor {
	if len(in) == 0 {
		return nil
	}
	out := make([]*invv1.Processor, 0, len(in))
	for _, p := range in {
		out = append(out, &invv1.Processor{
			SocketDesignation: p.SocketDesignation, Manufacturer: p.Manufacturer, Version: p.Version,
			MaxSpeedMhz: p.MaxSpeedMHz, CurrentSpeedMhz: p.CurrentSpeedMHz, CoreCount: p.CoreCount,
			CoreEnabled: p.CoreEnabled, ThreadCount: p.ThreadCount, PartNumber: p.PartNumber,
			SerialNumber: p.SerialNumber, SocketPopulated: p.SocketPopulated,
			Family: p.Family, Type: p.Type, Upgrade: p.Upgrade,
		})
	}
	return out
}

// ProcessorsFromPB maps wire processors.
func ProcessorsFromPB(in []*invv1.Processor) []store.Processor {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.Processor, 0, len(in))
	for _, p := range in {
		out = append(out, store.Processor{
			SocketDesignation: p.GetSocketDesignation(), Manufacturer: p.GetManufacturer(), Version: p.GetVersion(),
			MaxSpeedMHz: p.GetMaxSpeedMhz(), CurrentSpeedMHz: p.GetCurrentSpeedMhz(), CoreCount: p.GetCoreCount(),
			CoreEnabled: p.GetCoreEnabled(), ThreadCount: p.GetThreadCount(), PartNumber: p.GetPartNumber(),
			SerialNumber: p.GetSerialNumber(), SocketPopulated: p.GetSocketPopulated(),
			Family: p.GetFamily(), Type: p.GetType(), Upgrade: p.GetUpgrade(),
		})
	}
	return out
}

// MemoryArrayToPB maps one memory array.
func MemoryArrayToPB(a store.MemoryArray) *invv1.MemoryArray {
	return &invv1.MemoryArray{
		Location: a.Location, Use: a.Use, ErrorCorrection: a.ErrorCorrection,
		MaximumCapacity: a.MaximumCapacity, NumberOfDevices: a.NumberOfDevices, Handle: a.Handle,
	}
}

// MemoryArrayFromPB maps one wire memory array.
func MemoryArrayFromPB(a *invv1.MemoryArray) store.MemoryArray {
	return store.MemoryArray{
		Location: a.GetLocation(), Use: a.GetUse(), ErrorCorrection: a.GetErrorCorrection(),
		MaximumCapacity: a.GetMaximumCapacity(), NumberOfDevices: a.GetNumberOfDevices(), Handle: a.GetHandle(),
	}
}

// MemoryToPB maps the memory subsystem (every slot incl. empty ones).
func MemoryToPB(m store.MemoryInfo) *invv1.MemoryInfo {
	pb := &invv1.MemoryInfo{
		TotalPhysicalBytes: m.TotalPhysicalBytes, Array: MemoryArrayToPB(m.Array),
		SlotsTotal: m.SlotsTotal, SlotsPopulated: m.SlotsPopulated,
	}
	for _, a := range m.Arrays {
		pb.Arrays = append(pb.Arrays, MemoryArrayToPB(a))
	}
	for _, mod := range m.Modules {
		pb.Modules = append(pb.Modules, &invv1.MemoryModule{
			DeviceLocator: mod.DeviceLocator, BankLocator: mod.BankLocator, CapacityBytes: mod.CapacityBytes,
			FormFactor: mod.FormFactor, MemoryType: mod.MemoryType, SpeedMtS: mod.SpeedMTs,
			ConfiguredSpeedMtS: mod.ConfiguredSpeedMTs, Manufacturer: mod.Manufacturer,
			SerialNumber: mod.SerialNumber, PartNumber: mod.PartNumber,
			Populated: mod.Populated, TypeDetail: mod.TypeDetail, ArrayHandle: mod.ArrayHandle,
			AssetTag: mod.AssetTag, RankCount: mod.RankCount,
		})
	}
	return pb
}

// MemoryFromPB maps the wire memory subsystem.
func MemoryFromPB(pb *invv1.MemoryInfo) store.MemoryInfo {
	m := store.MemoryInfo{
		TotalPhysicalBytes: pb.GetTotalPhysicalBytes(), Array: MemoryArrayFromPB(pb.GetArray()),
		SlotsTotal: pb.GetSlotsTotal(), SlotsPopulated: pb.GetSlotsPopulated(),
	}
	for _, a := range pb.GetArrays() {
		m.Arrays = append(m.Arrays, MemoryArrayFromPB(a))
	}
	for _, mod := range pb.GetModules() {
		m.Modules = append(m.Modules, store.MemoryModule{
			DeviceLocator: mod.GetDeviceLocator(), BankLocator: mod.GetBankLocator(), CapacityBytes: mod.GetCapacityBytes(),
			FormFactor: mod.GetFormFactor(), MemoryType: mod.GetMemoryType(), SpeedMTs: mod.GetSpeedMtS(),
			ConfiguredSpeedMTs: mod.GetConfiguredSpeedMtS(), Manufacturer: mod.GetManufacturer(),
			SerialNumber: mod.GetSerialNumber(), PartNumber: mod.GetPartNumber(),
			Populated: mod.GetPopulated(), TypeDetail: mod.GetTypeDetail(), ArrayHandle: mod.GetArrayHandle(),
			AssetTag: mod.GetAssetTag(), RankCount: mod.GetRankCount(),
		})
	}
	return m
}

// DisksToPB maps disks including the legacy partition groups.
func DisksToPB(in []store.Disk) []*invv1.Disk {
	if len(in) == 0 {
		return nil
	}
	out := make([]*invv1.Disk, 0, len(in))
	for _, d := range in {
		pd := &invv1.Disk{
			Model: d.Model, Serial: d.Serial, SizeBytes: d.SizeBytes, MediaType: d.MediaType, Interface: d.Interface,
			Name: d.Name, Removable: d.Removable, Vendor: d.Vendor,
		}
		for _, p := range d.Partitions {
			pd.Partitions = append(pd.Partitions, &invv1.Partition{Mount: p.Mount, Fs: p.FS, SizeBytes: p.SizeBytes, FreeBytes: p.FreeBytes})
		}
		out = append(out, pd)
	}
	return out
}

// DisksFromPB maps wire disks.
func DisksFromPB(in []*invv1.Disk) []store.Disk {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.Disk, 0, len(in))
	for _, d := range in {
		disk := store.Disk{
			Model: d.GetModel(), Serial: d.GetSerial(), SizeBytes: d.GetSizeBytes(), MediaType: d.GetMediaType(),
			Interface: d.GetInterface(), Name: d.GetName(), Removable: d.GetRemovable(), Vendor: d.GetVendor(),
		}
		for _, p := range d.GetPartitions() {
			disk.Partitions = append(disk.Partitions, store.Partition{Mount: p.GetMount(), FS: p.GetFs(), SizeBytes: p.GetSizeBytes(), FreeBytes: p.GetFreeBytes()})
		}
		out = append(out, disk)
	}
	return out
}

// FilesystemsToPB maps filesystems.
func FilesystemsToPB(in []store.Filesystem) []*invv1.Filesystem {
	if len(in) == 0 {
		return nil
	}
	out := make([]*invv1.Filesystem, 0, len(in))
	for _, f := range in {
		out = append(out, &invv1.Filesystem{Mount: f.Mount, Fs: f.FS, Device: f.Device, SizeBytes: f.SizeBytes, FreeBytes: f.FreeBytes, Disks: f.Disks})
	}
	return out
}

// FilesystemsFromPB maps wire filesystems.
func FilesystemsFromPB(in []*invv1.Filesystem) []store.Filesystem {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.Filesystem, 0, len(in))
	for _, f := range in {
		out = append(out, store.Filesystem{Mount: f.GetMount(), FS: f.GetFs(), Device: f.GetDevice(),
			SizeBytes: f.GetSizeBytes(), FreeBytes: f.GetFreeBytes(), Disks: f.GetDisks()})
	}
	return out
}

// AvailabilityToPB maps the collection availability; the zero value maps to nil.
func AvailabilityToPB(a store.HardwareAvailability) *invv1.HardwareAvailability {
	if a == (store.HardwareAvailability{}) {
		return nil
	}
	return &invv1.HardwareAvailability{Smbios: a.SMBIOS, Disks: a.Disks}
}

// AvailabilityFromPB maps the wire collection availability.
func AvailabilityFromPB(a *invv1.HardwareAvailability) store.HardwareAvailability {
	return store.HardwareAvailability{SMBIOS: a.GetSmbios(), Disks: a.GetDisks()}
}
