package collector

import "time"

// Inventory holds the complete hardware inventory of a Windows host.
type Inventory struct {
	CollectedAt time.Time     `json:"collected_at"`
	Hostname    string        `json:"hostname"`
	System      SystemInfo    `json:"system"`
	CPU         []CPUInfo     `json:"cpu"`
	Memory      MemoryInfo    `json:"memory"`
	Monitors    []MonitorInfo `json:"monitors"`
}

// SystemInfo holds computer manufacturer, model, and serial number.
type SystemInfo struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
}

// CPUInfo holds processor details.
type CPUInfo struct {
	Name                      string `json:"name"`
	Manufacturer              string `json:"manufacturer"`
	Family                    uint16 `json:"family"`
	Architecture              uint16 `json:"architecture"`
	NumberOfCores             uint32 `json:"cores"`
	NumberOfLogicalProcessors uint32 `json:"logical_processors"`
	MaxClockSpeedMHz          uint32 `json:"max_clock_speed_mhz"`
}

// MemoryInfo holds total physical memory and per-module details.
type MemoryInfo struct {
	TotalPhysicalBytes uint64         `json:"total_physical_bytes"`
	TotalPhysicalGB    float64        `json:"total_physical_gb"`
	Modules            []MemoryModule `json:"modules,omitempty"`
}

// MemoryModule holds details for a single physical memory DIMM.
type MemoryModule struct {
	CapacityBytes uint64 `json:"capacity_bytes"`
	SpeedMHz      uint32 `json:"speed_mhz"`
	Manufacturer  string `json:"manufacturer"`
	PartNumber    string `json:"part_number"`
	SerialNumber  string `json:"serial_number"`
	DeviceLocator string `json:"device_locator"`
}

// MonitorInfo holds connected display details.
type MonitorInfo struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
}
