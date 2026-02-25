package collector

import "github.com/yusufpapurcu/wmi"

type win32Processor struct {
	Name                      string
	Manufacturer              string
	Family                    uint16
	Architecture              uint16
	NumberOfCores             uint32
	NumberOfLogicalProcessors uint32
	MaxClockSpeed             uint32
}

// collectCPUInfo queries Win32_Processor for CPU details.
func collectCPUInfo() ([]CPUInfo, error) {
	var procs []win32Processor
	q := "SELECT Name, Manufacturer, Family, Architecture, NumberOfCores, NumberOfLogicalProcessors, MaxClockSpeed FROM Win32_Processor"
	if err := wmi.Query(q, &procs); err != nil {
		return nil, err
	}

	result := make([]CPUInfo, len(procs))
	for i, p := range procs {
		result[i] = CPUInfo{
			Name:                      p.Name,
			Manufacturer:              p.Manufacturer,
			Family:                    p.Family,
			Architecture:              p.Architecture,
			NumberOfCores:             p.NumberOfCores,
			NumberOfLogicalProcessors: p.NumberOfLogicalProcessors,
			MaxClockSpeedMHz:          p.MaxClockSpeed,
		}
	}
	return result, nil
}
