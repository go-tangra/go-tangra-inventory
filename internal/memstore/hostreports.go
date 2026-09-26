package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// SetReportDigest records digest/changedAt when the digest differs.
func (m *Mem) SetReportDigest(_ context.Context, tenantID, hostID, digest string, changedAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetReportDigest"); err != nil {
		return false, err
	}
	h, ok := m.hosts[hostID]
	if !ok || h.TenantID != tenantID {
		return false, repo.ErrNotFound
	}
	if h.ReportDigest == digest {
		return false, nil
	}
	h.ReportDigest = digest
	h.ReportChangedAt = changedAt.UTC()
	m.hosts[hostID] = h
	return true, nil
}

// ListReportTenants lists tenants with a report change after since (system scope).
func (m *Mem) ListReportTenants(_ context.Context, since time.Time, limit int) ([]string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListReportTenants"); err != nil {
		return nil, time.Time{}, err
	}
	latest := map[string]time.Time{}
	for _, h := range m.hosts {
		if !since.IsZero() && !h.ReportChangedAt.After(since) {
			continue
		}
		if cur, ok := latest[h.TenantID]; !ok || h.ReportChangedAt.After(cur) {
			latest[h.TenantID] = h.ReportChangedAt
		}
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := latest[ids[i]], latest[ids[j]]
		if !a.Equal(b) {
			return a.Before(b)
		}
		return ids[i] < ids[j]
	})
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	maxAt := since
	for _, id := range ids {
		if latest[id].After(maxAt) {
			maxAt = latest[id]
		}
	}
	return ids, maxAt, nil
}

// ListHostReportRows lists a tenant's hosts by (report_changed_at, id).
func (m *Mem) ListHostReportRows(_ context.Context, tenantID string, f store.ReportRowFilter) ([]store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListHostReportRows"); err != nil {
		return nil, err
	}
	var out []store.Host
	for _, h := range m.hosts {
		if h.TenantID != tenantID {
			continue
		}
		if !f.ChangedSince.IsZero() && !h.ReportChangedAt.After(f.ChangedSince) {
			continue
		}
		if f.After != nil && !cursorLess(*f.After, h) {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		return cursorLess(store.ReportCursor{ChangedAt: out[i].ReportChangedAt, ID: out[i].ID}, out[j])
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// cursorLess reports whether c sorts strictly before host h.
func cursorLess(c store.ReportCursor, h store.Host) bool {
	if !c.ChangedAt.Equal(h.ReportChangedAt) {
		return c.ChangedAt.Before(h.ReportChangedAt)
	}
	return c.ID < h.ID
}
