package collector

import (
	"os"

	"github.com/siderolabs/go-smbios/smbios"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Linux exposes the raw SMBIOS table and its entry point in sysfs.
const (
	dmiEntryPoint = "/sys/firmware/dmi/tables/smbios_entry_point"
	dmiTable      = "/sys/firmware/dmi/tables/DMI"
	// maxDMITable bounds the table read (real tables are a few KiB).
	maxDMITable = 1 << 20
)

// tableSource returns a split SMBIOS table (the go-smbios firmware table
// path used where sysfs has none, i.e. Windows).
type tableSource func() (agentfacts.SMBIOSVersion, []agentfacts.SMBIOSStructure, error)

// goSMBIOSSource finds the table through go-smbios (GetSystemFirmwareTable on
// Windows); a panic of its typed decoding on a malformed table becomes an
// error. Only the raw structures are used.
func goSMBIOSSource() (agentfacts.SMBIOSVersion, []agentfacts.SMBIOSStructure, error) {
	s, err := agentfacts.OpenGoSMBIOS(smbios.New)
	if err != nil {
		return agentfacts.SMBIOSVersion{}, nil, err
	}
	v, structs := agentfacts.FromGoSMBIOS(s)
	return v, structs, nil
}

// collectHardware reads and decodes the local SMBIOS table (feature 023:
// DSP0134 names from the raw structures, every memory slot, arrays, chassis
// type, processor family and socket). ok is false when no table is readable
// (insufficient privileges, unsupported platform); collection goes on.
func collectHardware() (agentfacts.SMBIOSHardware, bool) {
	return readHardware(dmiEntryPoint, dmiTable, goSMBIOSSource)
}

// readHardware decodes the sysfs table at tablePath (version from the entry
// point at epPath, SMBIOS 3.0 when unreadable) and otherwise the fallback
// source.
func readHardware(epPath, tablePath string, fallback tableSource) (agentfacts.SMBIOSHardware, bool) {
	if table, err := readBounded(tablePath, maxDMITable); err == nil && len(table) > 0 {
		v := agentfacts.SMBIOSVersion{Major: 3}
		if ep, eerr := readBounded(epPath, 64); eerr == nil {
			if pv, ok := agentfacts.ParseEntryPoint(ep); ok {
				v = pv
			}
		}
		if hw, derr := agentfacts.DecodeSMBIOSTable(table, v); derr == nil {
			return hw, true
		}
	}
	if fallback == nil {
		return agentfacts.SMBIOSHardware{}, false
	}
	v, structs, err := fallback()
	if err != nil || len(structs) == 0 {
		return agentfacts.SMBIOSHardware{}, false
	}
	return agentfacts.DecodeSMBIOS(v, structs), true
}

// readBounded reads at most n bytes of path.
func readBounded(path string, n int64) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- fixed sysfs paths (tests: fixtures)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	total := 0
	for total < len(buf) {
		m, rerr := f.Read(buf[total:])
		total += m
		if rerr != nil {
			break
		}
	}
	return buf[:total], nil
}

// applyHardware copies the decoded hardware onto the inventory and records
// the SMBIOS availability and truncation counters.
func applyHardware(inv *store.Inventory, hw agentfacts.SMBIOSHardware) {
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
	inv.Truncated.Processors += hw.Truncated.Processors
	inv.Truncated.MemorySlots += hw.Truncated.MemorySlots
	inv.Truncated.MemoryArrays += hw.Truncated.MemoryArrays
	inv.Availability.SMBIOS = hw.Availability
}
