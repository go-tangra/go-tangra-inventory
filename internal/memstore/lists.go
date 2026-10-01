package memstore

import (
	"context"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Paged lists (list contract, go-tangra specs/032-server-side-tables): the
// same public sort fields as the SQL Specs, sorted and windowed in Go.

// hostKey is the value of a store.HostList sort field (same semantics as SQL).
func hostKey(h store.Host, field string) any {
	switch field {
	case "os_name":
		return h.OSName
	case "manufacturer":
		return h.Manufacturer
	case "status":
		return h.Status
	case "last_seen":
		return h.LastSeen
	case "created_at":
		return h.CreatedAt
	default:
		return h.Hostname
	}
}

// ListHostsPage implements repo.Store.
func (m *Mem) ListHostsPage(_ context.Context, tenantID string, f store.HostFilter, req listquery.Request) ([]store.Host, int, listquery.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req = store.ListRequest(req, store.HostList)
	if err := m.fail("ListHostsPage"); err != nil {
		return nil, 0, req, err
	}
	all := m.filterHosts(tenantID, f)
	listquery.SortSlice(all, req, hostKey, func(h store.Host) string { return h.ID })
	page, total, applied := listquery.Window(all, req)
	return append([]store.Host(nil), page...), total, applied, nil
}

// ListSnapshotsPage implements repo.Store (payload-free summaries).
func (m *Mem) ListSnapshotsPage(_ context.Context, tenantID, hostID string, req listquery.Request) ([]store.Snapshot, int, listquery.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req = store.ListRequest(req, store.SnapshotList)
	if err := m.fail("ListSnapshotsPage"); err != nil {
		return nil, 0, req, err
	}
	var all []store.Snapshot
	for _, s := range m.snaps {
		if s.TenantID == tenantID && s.HostID == hostID {
			s.Payload = store.Inventory{}
			all = append(all, s)
		}
	}
	listquery.SortSlice(all, req, func(s store.Snapshot, _ string) any { return s.CollectedAt }, func(s store.Snapshot) string { return s.ID })
	page, total, applied := listquery.Window(all, req)
	return append([]store.Snapshot(nil), page...), total, applied, nil
}

// ListChangesPage implements repo.Store.
func (m *Mem) ListChangesPage(_ context.Context, tenantID, hostID string, req listquery.Request) ([]store.Change, int, listquery.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req = store.ListRequest(req, store.ChangeList)
	if err := m.fail("ListChangesPage"); err != nil {
		return nil, 0, req, err
	}
	var all []store.Change
	for _, c := range m.changes {
		if c.TenantID == tenantID && c.HostID == hostID {
			all = append(all, c)
		}
	}
	key := func(c store.Change, field string) any {
		if field == "kind" {
			return c.ChangeType
		}
		return c.DetectedAt
	}
	listquery.SortSlice(all, req, key, func(c store.Change) string { return c.ID })
	page, total, applied := listquery.Window(all, req)
	return append([]store.Change(nil), page...), total, applied, nil
}
