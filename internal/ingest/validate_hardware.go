package ingest

import (
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// maxTypeDetail bounds the DSP0134 type-detail names of one memory module
// (the WORD has 15 defined bits).
const maxTypeDetail = 16

var (
	mediaTypes = set(store.MediaSSD, store.MediaHDD, store.MediaNVMeSSD, store.MediaUnknown)
	diskIfaces = set(store.IfNVMe, store.IfSATA, store.IfSAS, store.IfSCSI, store.IfUSB, store.IfVirtio,
		store.IfHyperV, store.IfXen, store.IfMMC, store.IfOther)
	availabilities = set(store.AvailOK, store.AvailPartial, store.AvailUnavailable, store.AvailUnsupported, store.AvailUnknown)
)

// validateHardware bounds and sanitises the hardware part of an agent payload
// (feature 023, research D5). Lists above their bound keep the first N
// entries and count the excess in inv.Truncated (dropped filesystem->disk
// references count as disks); every string is made valid UTF-8, stripped of
// control characters and clipped to store.MaxHWString bytes; media, interface
// and availability values outside their closed sets become "unknown",
// "other" and "unknown". Empty values from older agents stay empty. The
// hardware schema is clamped to the highest known one.
func validateHardware(inv *store.Inventory) {
	lim := &inv.Truncated
	if inv.HardwareSchema > store.HardwareSchemaCurrent {
		inv.HardwareSchema = store.HardwareSchemaCurrent
	}

	b := &inv.BIOS
	cleanAll(&b.Vendor, &b.Version, &b.ReleaseDate)
	sy := &inv.System
	cleanAll(&sy.Manufacturer, &sy.ProductName, &sy.Version, &sy.SerialNumber, &sy.UUID, &sy.WakeUpType, &sy.SKUNumber, &sy.Family)
	bb := &inv.Baseboard
	cleanAll(&bb.Manufacturer, &bb.Product, &bb.Version, &bb.SerialNumber, &bb.AssetTag, &bb.LocationInChassis, &bb.BoardType)
	ch := &inv.Chassis
	cleanAll(&ch.Manufacturer, &ch.Version, &ch.SerialNumber, &ch.AssetTag, &ch.SKUNumber, &ch.Type, &ch.BootupState)

	inv.Processors = bound(inv.Processors, store.MaxProcessors, &lim.Processors)
	for i := range inv.Processors {
		p := &inv.Processors[i]
		cleanAll(&p.SocketDesignation, &p.Manufacturer, &p.Version, &p.PartNumber, &p.SerialNumber, &p.Family, &p.Type, &p.Upgrade)
	}

	m := &inv.Memory
	cleanArray(&m.Array)
	m.Arrays = bound(m.Arrays, store.MaxMemoryArrays, &lim.MemoryArrays)
	for i := range m.Arrays {
		cleanArray(&m.Arrays[i])
	}
	m.Modules = bound(m.Modules, store.MaxMemorySlots, &lim.MemorySlots)
	for i := range m.Modules {
		mod := &m.Modules[i]
		cleanAll(&mod.DeviceLocator, &mod.BankLocator, &mod.FormFactor, &mod.MemoryType, &mod.Manufacturer,
			&mod.SerialNumber, &mod.PartNumber, &mod.AssetTag)
		mod.TypeDetail = cleanList(mod.TypeDetail, maxTypeDetail, nil)
	}

	inv.Disks = bound(inv.Disks, store.MaxDisks, &lim.Disks)
	for i := range inv.Disks {
		d := &inv.Disks[i]
		cleanAll(&d.Name, &d.Model, &d.Serial, &d.Vendor)
		d.MediaType = enum(d.MediaType, mediaTypes, store.MediaUnknown)
		d.Interface = enum(d.Interface, diskIfaces, store.IfOther)
	}

	inv.Filesystems = bound(inv.Filesystems, store.MaxFilesystems, &lim.Filesystems)
	for i := range inv.Filesystems {
		f := &inv.Filesystems[i]
		cleanAll(&f.Mount, &f.FS, &f.Device)
		f.Disks = cleanList(f.Disks, store.MaxFSDisks, &lim.Disks)
	}

	a := &inv.Availability
	a.SMBIOS = enum(a.SMBIOS, availabilities, store.AvailUnknown)
	a.Disks = enum(a.Disks, availabilities, store.AvailUnknown)
}

func cleanArray(a *store.MemoryArray) {
	cleanAll(&a.Location, &a.Use, &a.ErrorCorrection)
}

// bound keeps the first n entries of in and adds the excess to *count.
func bound[T any](in []T, n int, count *uint32) []T {
	if len(in) <= n {
		return in
	}
	*count = addSat(*count, uint32(len(in)-n)) // #nosec G115 -- len(in)-n > 0 and bounded by the message size
	return in[:n]
}

// cleanList cleans every entry, drops empty ones and keeps at most n; the
// excess is added to *count when count is set. A nil or all-empty input
// yields nil.
func cleanList(in []string, n int, count *uint32) []string {
	var out []string
	dropped := uint32(0)
	for _, s := range in {
		s = cleanHW(s)
		if s == "" {
			continue
		}
		if len(out) == n {
			dropped++
			continue
		}
		out = append(out, s)
	}
	if count != nil {
		*count = addSat(*count, dropped)
	}
	return out
}

func cleanAll(ps ...*string) {
	for _, p := range ps {
		*p = cleanHW(*p)
	}
}

// cleanHW returns s as valid UTF-8 without control characters, surrounding
// whitespace or more than store.MaxHWString bytes.
func cleanHW(s string) string {
	if s == "" {
		return s
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if hasControl(s) {
		s = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				return -1
			}
			return r
		}, s)
	}
	return clip(strings.TrimSpace(s), store.MaxHWString)
}
