package collector

import (
	"fmt"
	"os"
	"time"
)

// Collect gathers a full hardware inventory from the local Windows host.
// It attempts all collectors and returns partial results alongside any errors.
func Collect() (*Inventory, error) {
	hostname, _ := os.Hostname()

	inv := &Inventory{
		CollectedAt: time.Now().UTC(),
		Hostname:    hostname,
	}

	var errs []error

	sys, err := collectSystemInfo()
	if err != nil {
		errs = append(errs, fmt.Errorf("system: %w", err))
	}
	inv.System = sys

	cpu, err := collectCPUInfo()
	if err != nil {
		errs = append(errs, fmt.Errorf("cpu: %w", err))
	}
	inv.CPU = cpu

	mem, err := collectMemoryInfo()
	if err != nil {
		errs = append(errs, fmt.Errorf("memory: %w", err))
	}
	inv.Memory = mem

	mon, err := collectMonitorInfo()
	if err != nil {
		errs = append(errs, fmt.Errorf("monitors: %w", err))
	}
	inv.Monitors = mon

	if len(errs) > 0 {
		return inv, fmt.Errorf("collection errors: %v", errs)
	}
	return inv, nil
}
