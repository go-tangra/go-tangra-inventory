// Package collector gathers a full local endpoint inventory (hardware via
// SMBIOS, OS/network/disk via gopsutil, and best-effort software/users) and
// returns it as a store.Inventory. Every sub-collector degrades to empty (never
// an error) when the data it reads is unavailable on the running platform, so a
// single privileged or platform-specific gap never fails the whole collect.
package collector

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Version is the agent version stamped into every collected inventory. It is
// overridden from the agent entrypoint (via ldflags) at build time.
var Version = "dev"

// Collect composes every sub-collector into a single store.Inventory. It never
// returns a nil-meaning error for a missing platform capability; err is only
// non-nil for a context cancellation observed before any work is done.
func Collect(ctx context.Context) (store.Inventory, error) {
	if err := ctx.Err(); err != nil {
		return store.Inventory{}, err
	}

	inv := store.Inventory{
		CollectedAt:  time.Now().UTC(),
		AgentVersion: Version,
	}

	// Identity: hostname + machine id (hardware uuid is filled from SMBIOS below).
	inv.Identity = collectIdentity()

	// Hardware via SMBIOS. Empty (not error) when SMBIOS is unavailable.
	if hw, ok := collectHardware(); ok {
		applyHardware(&inv, hw)
		if inv.Identity.HardwareUUID == "" {
			inv.Identity.HardwareUUID = inv.System.UUID
		}
	}

	// OS / network / disks via gopsutil (cross-platform).
	collectOS(ctx, &inv)
	collectNetworks(ctx, &inv)
	collectDisks(ctx, &inv)

	// Software: installed programs, services, local users (build-tagged,
	// best-effort; empty where not implemented for the platform).
	inv.Programs = collectPrograms()
	inv.Services = collectServices()
	inv.Users = collectUsers()

	// Platform extras: monitor EDID and the current interactive user.
	inv.Monitors = collectMonitors()
	if u, ok := collectCurrentUser(); ok {
		inv.Users = mergeUser(inv.Users, u)
	}

	return inv, nil
}

// applyHardware copies the SMBIOS-derived hardware pieces onto the inventory.
func applyHardware(inv *store.Inventory, hw hardware) {
	inv.BIOS = hw.BIOS
	inv.System = hw.System
	inv.Baseboard = hw.Baseboard
	inv.Chassis = hw.Chassis
	inv.Processors = hw.Processors
	inv.Cache = hw.Cache
	inv.Memory = hw.Memory
	inv.Ports = hw.Ports
	inv.Slots = hw.Slots
	inv.OEMStrings = hw.OEMStrings
	inv.BIOSLanguage = hw.BIOSLanguage
}

// mergeUser appends u unless a user with the same name is already present.
func mergeUser(users []store.UserAccount, u store.UserAccount) []store.UserAccount {
	if u.Name == "" {
		return users
	}
	for _, e := range users {
		if e.Name == u.Name {
			return users
		}
	}
	return append(users, u)
}

// collectIdentity resolves the stable host identity keys available without
// SMBIOS. HardwareUUID is filled by the hardware collector when present.
func collectIdentity() store.Identity {
	host, _ := os.Hostname()
	return store.Identity{
		Hostname:  host,
		MachineID: machineID(),
	}
}

// hostArch returns the OS architecture, falling back to the compiled GOARCH.
func hostArch(kernelArch string) string {
	if kernelArch != "" {
		return kernelArch
	}
	return runtime.GOARCH
}
