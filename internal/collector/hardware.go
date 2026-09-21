package collector

import (
	"strings"

	"github.com/go-freya/freya/services/inventory/internal/store"
	"github.com/siderolabs/go-smbios/smbios"
)

// hardware holds the SMBIOS-derived pieces of an inventory before they are
// applied onto a store.Inventory.
type hardware struct {
	BIOS         store.BIOSInfo
	System       store.SystemInfo
	Baseboard    store.BaseboardInfo
	Chassis      store.ChassisInfo
	Processors   []store.Processor
	Cache        []store.CacheInfo
	Memory       store.MemoryInfo
	Ports        []string
	Slots        []string
	OEMStrings   []string
	BIOSLanguage string
}

// collectHardware opens the local SMBIOS tables and maps them. It reports ok
// false (with an empty hardware) when SMBIOS is unavailable, e.g. insufficient
// privileges or an unsupported platform.
func collectHardware() (hardware, bool) {
	s, err := smbios.New()
	if err != nil || s == nil {
		return hardware{}, false
	}
	return mapHardware(s), true
}

// mapHardware is the pure SMBIOS -> store mapper. It is exercised by tests via a
// decoded fixture and is free of any platform I/O.
func mapHardware(s *smbios.SMBIOS) hardware {
	hw := hardware{
		BIOS: store.BIOSInfo{
			Vendor:      s.BIOSInformation.Vendor,
			Version:     s.BIOSInformation.Version,
			ReleaseDate: s.BIOSInformation.ReleaseDate,
		},
		System: store.SystemInfo{
			Manufacturer: s.SystemInformation.Manufacturer,
			ProductName:  s.SystemInformation.ProductName,
			Version:      s.SystemInformation.Version,
			SerialNumber: s.SystemInformation.SerialNumber,
			UUID:         s.SystemInformation.UUID,
			WakeUpType:   s.SystemInformation.WakeUpType.String(),
			SKUNumber:    s.SystemInformation.SKUNumber,
			Family:       s.SystemInformation.Family,
		},
		Baseboard: store.BaseboardInfo{
			Manufacturer:      s.BaseboardInformation.Manufacturer,
			Product:           s.BaseboardInformation.Product,
			Version:           s.BaseboardInformation.Version,
			SerialNumber:      s.BaseboardInformation.SerialNumber,
			AssetTag:          s.BaseboardInformation.AssetTag,
			LocationInChassis: s.BaseboardInformation.LocationInChassis,
			BoardType:         s.BaseboardInformation.BoardType.String(),
		},
		Chassis: store.ChassisInfo{
			Manufacturer: s.SystemEnclosure.Manufacturer,
			Version:      s.SystemEnclosure.Version,
			SerialNumber: s.SystemEnclosure.SerialNumber,
			AssetTag:     s.SystemEnclosure.AssetTagNumber,
			SKUNumber:    s.SystemEnclosure.SKUNumber,
		},
		Processors:   mapProcessors(s),
		Memory:       mapMemory(s),
		OEMStrings:   s.OEMStrings.Strings,
		BIOSLanguage: s.BIOSLanguageInformation.CurrentLanguage,
	}

	for _, c := range s.CacheInformation {
		hw.Cache = append(hw.Cache, store.CacheInfo{SocketDesignation: c.SocketDesignation})
	}
	for _, p := range s.PortConnectorInformation {
		if d := portLabel(p); d != "" {
			hw.Ports = append(hw.Ports, d)
		}
	}
	for _, sl := range s.SystemSlots {
		if d := strings.TrimSpace(sl.SlotDesignation); d != "" {
			hw.Slots = append(hw.Slots, d)
		}
	}
	return hw
}

// portLabel builds a human-readable designation for a port connector, preferring
// the external reference designator and falling back to the internal one.
func portLabel(p smbios.PortConnectorInformation) string {
	ext := strings.TrimSpace(p.ExternalReferenceDesignator)
	in := strings.TrimSpace(p.InternalReferenceDesignator)
	switch {
	case ext != "" && in != "":
		return in + " / " + ext
	case ext != "":
		return ext
	default:
		return in
	}
}

func mapProcessors(s *smbios.SMBIOS) []store.Processor {
	var out []store.Processor
	for _, p := range s.ProcessorInformation {
		out = append(out, store.Processor{
			SocketDesignation: p.SocketDesignation,
			Manufacturer:      p.ProcessorManufacturer,
			Version:           p.ProcessorVersion,
			MaxSpeedMHz:       uint32(p.MaxSpeed),
			CurrentSpeedMHz:   uint32(p.CurrentSpeed),
			CoreCount:         uint32(p.CoreCount),
			CoreEnabled:       uint32(p.CoreEnabled),
			ThreadCount:       uint32(p.ThreadCount),
			PartNumber:        strings.TrimSpace(p.PartNumber),
			SerialNumber:      strings.TrimSpace(p.SerialNumber),
			SocketPopulated:   p.Status.SocketPopulated(),
		})
	}
	return out
}

func mapMemory(s *smbios.SMBIOS) store.MemoryInfo {
	pma := s.PhysicalMemoryArray
	info := store.MemoryInfo{
		Array: store.MemoryArray{
			Location:        pma.Location.String(),
			Use:             pma.Use.String(),
			ErrorCorrection: pma.MemoryErrorCorrection.String(),
			MaximumCapacity: maxCapacityBytes(pma),
			NumberOfDevices: uint32(pma.NumberOfMemoryDevices),
		},
	}

	var total uint64
	for _, d := range s.MemoryDevices {
		capBytes := moduleCapacityBytes(d)
		if capBytes == 0 {
			continue // empty slot
		}
		total += capBytes
		info.Modules = append(info.Modules, store.MemoryModule{
			DeviceLocator:      d.DeviceLocator,
			BankLocator:        d.BankLocator,
			CapacityBytes:      capBytes,
			FormFactor:         d.FormFactor.String(),
			MemoryType:         d.MemoryType.String(),
			SpeedMTs:           uint32(d.Speed),
			ConfiguredSpeedMTs: uint32(d.ConfiguredMemorySpeed),
			Manufacturer:       strings.TrimSpace(d.Manufacturer),
			SerialNumber:       strings.TrimSpace(d.SerialNumber),
			PartNumber:         strings.TrimSpace(d.PartNumber),
		})
	}
	info.TotalPhysicalBytes = total
	return info
}

// moduleCapacityBytes returns a memory device's capacity in bytes, honoring the
// SMBIOS extended-size escape (0x7FFF) for DIMMs of 32 GB-1 MB or larger.
func moduleCapacityBytes(d smbios.MemoryDevice) uint64 {
	sizeMB := d.Size.Megabytes()
	if sizeMB == 0 {
		return 0
	}
	if uint16(d.Size) == 0x7FFF {
		return uint64(d.ExtendedSize) * 1024 * 1024
	}
	return uint64(sizeMB) * 1024 * 1024
}

// maxCapacityBytes returns the array maximum capacity in bytes, preferring the
// 64-bit extended field and otherwise converting the 32-bit kilobyte field.
func maxCapacityBytes(pma smbios.PhysicalMemoryArray) uint64 {
	if pma.ExtendedMaximumCapacity != 0 {
		return uint64(pma.ExtendedMaximumCapacity)
	}
	return uint64(pma.MaximumCapacity) * 1024
}
