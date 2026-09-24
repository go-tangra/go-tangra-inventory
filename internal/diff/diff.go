// Package diff is a pure inventory comparison engine. Given a previous and a
// next inventory payload it reports the per-component changes (added, removed,
// modified) between them. It is deterministic (stable component keys, sorted
// output) and side-effect free: it never touches a store, a clock, or a
// subject, so the change rows it returns carry only the comparison fields
// (Category, ChangeType, ComponentKey, Before/After JSON). Callers own the
// snapshot/host/tenant identifiers and detection timestamp.
package diff

import (
	"encoding/json"
	"reflect"
	"sort"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Component category names used for the Change.Category field. Singleton
// components use their category name as their ComponentKey too.
const (
	CatBIOS      = "bios"
	CatSystem    = "system"
	CatBaseboard = "baseboard"
	CatChassis   = "chassis"
	CatProcessor = "processor"
	CatMemory    = "memory"
	CatDisk      = "disk"
	CatNetwork   = "network"
	CatMonitor   = "monitor"
	CatSoftware  = "software"
	CatService   = "service"
	CatOS        = "os"
	CatUser      = "user"
	CatPatch     = "patch"
)

// nullSep joins the parts of a composite component key. A NUL byte cannot
// appear in the DMI/inventory string fields it separates, so keys stay unique.
const nullSep = "\x00"

// Diff compares prev against next and returns the component-level changes in a
// deterministic order: categories in a fixed sequence, and within each keyed
// category by ascending component key. The returned changes fill only the
// comparison fields; the caller stamps ids, tenant/host/snapshot and timestamp.
func Diff(prev, next store.Inventory) []store.Change {
	var out []store.Change
	add := func(c *store.Change) {
		if c != nil {
			out = append(out, *c)
		}
	}

	// Singleton components: one row per category, compared as a whole.
	add(diffSingle(CatBIOS, prev.BIOS, next.BIOS))
	add(diffSingle(CatSystem, prev.System, next.System))
	add(diffSingle(CatBaseboard, prev.Baseboard, next.Baseboard))
	add(diffSingle(CatChassis, prev.Chassis, next.Chassis))

	// Keyed collections.
	out = append(out, diffList(CatProcessor, prev.Processors, next.Processors,
		func(p store.Processor) string { return p.SocketDesignation })...)
	out = append(out, diffList(CatMemory, prev.Memory.Modules, next.Memory.Modules,
		func(m store.MemoryModule) string { return m.DeviceLocator + nullSep + m.SerialNumber })...)
	out = append(out, diffList(CatDisk, prev.Disks, next.Disks,
		func(d store.Disk) string { return d.Serial })...)
	out = append(out, diffList(CatNetwork, prev.Networks, next.Networks,
		func(n store.NetIface) string { return n.MAC })...)
	out = append(out, diffList(CatMonitor, prev.Monitors, next.Monitors,
		func(m store.Monitor) string { return m.SerialNumber })...)
	out = append(out, diffList(CatSoftware, prev.Programs, next.Programs,
		func(p store.Program) string { return p.Name + nullSep + p.Version })...)
	out = append(out, diffList(CatService, prev.Services, next.Services,
		func(s store.Service) string { return s.Name })...)

	// OS is a singleton but reported after the software collections.
	add(diffSingle(CatOS, prev.OS, next.OS))

	out = append(out, diffList(CatUser, prev.Users, next.Users,
		func(u store.UserAccount) string { return u.Name })...)
	out = append(out, diffList(CatPatch, prev.Patches, next.Patches,
		func(p store.Patch) string { return p.ID })...)

	return out
}

// diffSingle compares a singleton component. Equal values produce no change; a
// transition from the zero value is "added", a transition to the zero value is
// "removed", and any other difference is "modified".
func diffSingle(cat string, prev, next any) *store.Change {
	pj, nj := jsonOf(prev), jsonOf(next)
	if pj == nj {
		return nil
	}
	zj := zeroJSON(prev)
	c := store.Change{Category: cat, ComponentKey: cat}
	switch {
	case pj == zj:
		c.ChangeType = store.ChangeAdded
		c.After = nj
	case nj == zj:
		c.ChangeType = store.ChangeRemoved
		c.Before = pj
	default:
		c.ChangeType = store.ChangeModified
		c.Before = pj
		c.After = nj
	}
	return &c
}

// diffList compares two keyed collections. Items present only in next are
// "added", only in prev are "removed", and present in both but unequal are
// "modified". Output is sorted by component key for determinism.
func diffList[T any](cat string, prev, next []T, keyFn func(T) string) []store.Change {
	pm := make(map[string]T, len(prev))
	for _, it := range prev {
		pm[keyFn(it)] = it
	}
	nm := make(map[string]T, len(next))
	for _, it := range next {
		nm[keyFn(it)] = it
	}

	seen := map[string]bool{}
	keys := make([]string, 0, len(pm)+len(nm))
	for k := range pm {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range nm {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var out []store.Change
	for _, k := range keys {
		p, hasP := pm[k]
		n, hasN := nm[k]
		switch {
		case hasP && hasN:
			pj, nj := jsonOf(p), jsonOf(n)
			if pj == nj {
				continue
			}
			out = append(out, store.Change{
				Category: cat, ChangeType: store.ChangeModified, ComponentKey: k, Before: pj, After: nj,
			})
		case hasN:
			out = append(out, store.Change{
				Category: cat, ChangeType: store.ChangeAdded, ComponentKey: k, After: jsonOf(n),
			})
		default:
			out = append(out, store.Change{
				Category: cat, ChangeType: store.ChangeRemoved, ComponentKey: k, Before: jsonOf(p),
			})
		}
	}
	return out
}

// jsonOf renders v as canonical JSON. The inventory component types marshal
// without error, so the error is intentionally discarded.
func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// zeroJSON renders the zero value of v's concrete type as JSON, used to
// classify a singleton transition as added or removed.
func zeroJSON(v any) string {
	z := reflect.Zero(reflect.TypeOf(v)).Interface()
	return jsonOf(z)
}
