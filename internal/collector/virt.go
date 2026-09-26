package collector

import (
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// collectVirtualization detects the virtualization role from the SMBIOS data
// already collected (both platforms) plus the platform's file facts.
func collectVirtualization(inv *store.Inventory, smbiosReadable bool) {
	f := agentfacts.VirtFacts{
		SMBIOSReadable:  smbiosReadable,
		SysManufacturer: inv.System.Manufacturer,
		SysProduct:      inv.System.ProductName,
		BIOSVendor:      inv.BIOS.Vendor,
	}
	platformVirtFacts(&f)
	inv.Virtualization = agentfacts.DetectVirtualization(f)
}
