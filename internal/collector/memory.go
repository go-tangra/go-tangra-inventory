package collector

import (
	"strings"

	"github.com/yusufpapurcu/wmi"
)

type win32ComputerSystemMem struct {
	TotalPhysicalMemory uint64
}

type win32PhysicalMemory struct {
	Capacity      uint64
	Speed         uint32
	Manufacturer  string
	PartNumber    string
	SerialNumber  string
	DeviceLocator string
}

// collectMemoryInfo queries Win32_ComputerSystem for total RAM and
// Win32_PhysicalMemory for per-DIMM details.
func collectMemoryInfo() (MemoryInfo, error) {
	var cs []win32ComputerSystemMem
	if err := wmi.Query("SELECT TotalPhysicalMemory FROM Win32_ComputerSystem", &cs); err != nil {
		return MemoryInfo{}, err
	}

	var pm []win32PhysicalMemory
	if err := wmi.Query("SELECT Capacity, Speed, Manufacturer, PartNumber, SerialNumber, DeviceLocator FROM Win32_PhysicalMemory", &pm); err != nil {
		return MemoryInfo{}, err
	}

	info := MemoryInfo{}
	if len(cs) > 0 {
		info.TotalPhysicalBytes = cs[0].TotalPhysicalMemory
		info.TotalPhysicalGB = float64(cs[0].TotalPhysicalMemory) / (1024 * 1024 * 1024)
	}

	info.Modules = make([]MemoryModule, len(pm))
	for i, m := range pm {
		info.Modules[i] = MemoryModule{
			CapacityBytes: m.Capacity,
			SpeedMHz:      m.Speed,
			Manufacturer:  strings.TrimSpace(m.Manufacturer),
			PartNumber:    strings.TrimSpace(m.PartNumber),
			SerialNumber:  strings.TrimSpace(m.SerialNumber),
			DeviceLocator: m.DeviceLocator,
		}
	}
	return info, nil
}
