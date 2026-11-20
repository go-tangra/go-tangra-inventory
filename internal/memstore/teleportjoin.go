package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 038: Teleport join items.

func (m *Mem) ts() map[string]store.TeleportJoinItem {
	if m.teleport == nil {
		m.teleport = map[string]store.TeleportJoinItem{}
	}
	return m.teleport
}

// activeTeleport returns the active item of tenant, host and cluster other
// than exceptID.
func (m *Mem) activeTeleport(tenantID, hostID, clusterID, exceptID string) (store.TeleportJoinItem, bool) {
	for _, i := range m.ts() {
		if i.TenantID == tenantID && i.HostID == hostID && i.ClusterID == clusterID && i.ID != exceptID && i.Active() {
			return i, true
		}
	}
	return store.TeleportJoinItem{}, false
}

// CreateTeleportItem implements repo.TeleportJoinStore.
func (m *Mem) CreateTeleportItem(_ context.Context, n repo.NewTeleportItem) ([]store.TeleportJoinItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateTeleportItem"); err != nil {
		return nil, err
	}
	it := n.Item
	if _, ok := m.ts()[it.ID]; ok {
		return nil, repo.ErrConflict
	}
	for _, x := range m.ts() {
		if x.TenantID == it.TenantID && x.Source == it.Source && x.EnrollmentID == it.EnrollmentID {
			return nil, repo.ErrConflict
		}
	}
	rows := append([]store.AuditRow{}, n.Audit...)
	var superseded []store.TeleportJoinItem
	if it.Active() {
		if old, ok := m.activeTeleport(it.TenantID, it.HostID, it.ClusterID, ""); ok {
			old.State, old.Reason, old.UpdatedAt = store.DeliverySuperseded, "", it.CreatedAt
			m.ts()[old.ID] = old
			superseded = append(superseded, old)
			if n.SupersedeAudit != nil {
				rows = append(rows, n.SupersedeAudit(old, it.ID))
			}
		}
	}
	m.ts()[it.ID] = it
	m.audit = append(m.audit, rows...)
	return superseded, nil
}

// GetTeleportItem implements repo.TeleportJoinStore.
func (m *Mem) GetTeleportItem(_ context.Context, tenantID, id string) (store.TeleportJoinItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetTeleportItem"); err != nil {
		return store.TeleportJoinItem{}, err
	}
	i, ok := m.ts()[id]
	if !ok || i.TenantID != tenantID {
		return store.TeleportJoinItem{}, repo.ErrNotFound
	}
	return i, nil
}

// GetTeleportItemByEnrollment implements repo.TeleportJoinStore.
func (m *Mem) GetTeleportItemByEnrollment(_ context.Context, tenantID, source, enrollmentID string) (store.TeleportJoinItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetTeleportItemByEnrollment"); err != nil {
		return store.TeleportJoinItem{}, err
	}
	for _, i := range m.ts() {
		if i.TenantID == tenantID && i.Source == source && i.EnrollmentID == enrollmentID {
			return i, nil
		}
	}
	return store.TeleportJoinItem{}, repo.ErrNotFound
}

// UpdateTeleportItem implements repo.TeleportJoinStore.
func (m *Mem) UpdateTeleportItem(_ context.Context, tenantID, id string, fn func(*store.TeleportJoinItem) ([]store.AuditRow, error)) (store.TeleportJoinItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateTeleportItem"); err != nil {
		return store.TeleportJoinItem{}, err
	}
	cur, ok := m.ts()[id]
	if !ok || cur.TenantID != tenantID {
		return store.TeleportJoinItem{}, repo.ErrNotFound
	}
	next := cur
	rows, err := fn(&next)
	if err != nil {
		return cur, err
	}
	if next.Active() {
		if _, dup := m.activeTeleport(tenantID, next.HostID, next.ClusterID, id); dup {
			return cur, repo.ErrConflict
		}
	}
	next.ID, next.TenantID = cur.ID, cur.TenantID
	m.ts()[id] = next
	m.audit = append(m.audit, rows...)
	return next, nil
}

// teleportOldestFirst orders items by creation time, then id, and cuts the
// list to limit (<= 0: no limit).
func teleportOldestFirst(list []store.TeleportJoinItem, limit int) []store.TeleportJoinItem {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
	if limit > 0 && len(list) > limit {
		return list[:limit]
	}
	return list
}

// ListActiveTeleportItemsForAgent implements repo.TeleportJoinStore.
func (m *Mem) ListActiveTeleportItemsForAgent(_ context.Context, tenantID, agentID string, limit int) ([]store.TeleportJoinItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListActiveTeleportItemsForAgent"); err != nil {
		return nil, err
	}
	var out []store.TeleportJoinItem
	for _, i := range m.ts() {
		if i.TenantID == tenantID && i.AgentID == agentID && i.Active() {
			out = append(out, i)
		}
	}
	return teleportOldestFirst(out, limit), nil
}

// ListStaleTeleportItems implements repo.TeleportJoinStore.
func (m *Mem) ListStaleTeleportItems(_ context.Context, now, reportBefore time.Time, limit int) ([]store.TeleportJoinItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListStaleTeleportItems"); err != nil {
		return nil, err
	}
	var out []store.TeleportJoinItem
	for _, i := range m.ts() {
		expired := store.CertDeliveryActive(i.State) && i.ExpiresAt.Before(now)
		silent := (i.State == store.DeliveryFetched || i.State == store.TeleportInstalling) && i.UpdatedAt.Before(reportBefore)
		if expired || silent {
			out = append(out, i)
		}
	}
	return teleportOldestFirst(out, limit), nil
}

// PurgeTeleportItems implements repo.TeleportJoinStore.
func (m *Mem) PurgeTeleportItems(_ context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("PurgeTeleportItems"); err != nil {
		return 0, err
	}
	var n int64
	for id, i := range m.ts() {
		if !i.Active() && i.UpdatedAt.Before(olderThan) {
			delete(m.teleport, id)
			n++
		}
	}
	return n, nil
}
