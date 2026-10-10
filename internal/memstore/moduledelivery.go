package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 037: module delivery items.

func (m *Mem) ms() map[string]store.ModuleDeliveryItem {
	if m.modules == nil {
		m.modules = map[string]store.ModuleDeliveryItem{}
	}
	return m.modules
}

// activeModule returns the active item of tenant, host and module other
// than exceptID.
func (m *Mem) activeModule(tenantID, hostID, module, exceptID string) (store.ModuleDeliveryItem, bool) {
	for _, i := range m.ms() {
		if i.TenantID == tenantID && i.HostID == hostID && i.Module == module && i.ID != exceptID && i.Active() {
			return i, true
		}
	}
	return store.ModuleDeliveryItem{}, false
}

// CreateModuleItem implements repo.ModuleDeliveryStore.
func (m *Mem) CreateModuleItem(_ context.Context, n repo.NewModuleItem) ([]store.ModuleDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateModuleItem"); err != nil {
		return nil, err
	}
	it := n.Item
	if _, ok := m.ms()[it.ID]; ok {
		return nil, repo.ErrConflict
	}
	for _, x := range m.ms() {
		if x.TenantID == it.TenantID && x.Source == it.Source && x.DeliveryID == it.DeliveryID {
			return nil, repo.ErrConflict
		}
	}
	rows := append([]store.AuditRow{}, n.Audit...)
	var superseded []store.ModuleDeliveryItem
	if it.Active() {
		if old, ok := m.activeModule(it.TenantID, it.HostID, it.Module, ""); ok {
			at := it.CreatedAt
			old.State, old.Reason, old.UpdatedAt = store.DeliverySuperseded, "", at
			m.ms()[old.ID] = old
			superseded = append(superseded, old)
			if n.SupersedeAudit != nil {
				rows = append(rows, n.SupersedeAudit(old, it.ID))
			}
		}
	}
	m.ms()[it.ID] = it
	m.audit = append(m.audit, rows...)
	return superseded, nil
}

// GetModuleItem implements repo.ModuleDeliveryStore.
func (m *Mem) GetModuleItem(_ context.Context, tenantID, id string) (store.ModuleDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetModuleItem"); err != nil {
		return store.ModuleDeliveryItem{}, err
	}
	i, ok := m.ms()[id]
	if !ok || i.TenantID != tenantID {
		return store.ModuleDeliveryItem{}, repo.ErrNotFound
	}
	return i, nil
}

// GetModuleItemByDelivery implements repo.ModuleDeliveryStore.
func (m *Mem) GetModuleItemByDelivery(_ context.Context, tenantID, source, deliveryID string) (store.ModuleDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetModuleItemByDelivery"); err != nil {
		return store.ModuleDeliveryItem{}, err
	}
	for _, i := range m.ms() {
		if i.TenantID == tenantID && i.Source == source && i.DeliveryID == deliveryID {
			return i, nil
		}
	}
	return store.ModuleDeliveryItem{}, repo.ErrNotFound
}

// UpdateModuleItem implements repo.ModuleDeliveryStore.
func (m *Mem) UpdateModuleItem(_ context.Context, tenantID, id string, fn func(*store.ModuleDeliveryItem) ([]store.AuditRow, error)) (store.ModuleDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateModuleItem"); err != nil {
		return store.ModuleDeliveryItem{}, err
	}
	cur, ok := m.ms()[id]
	if !ok || cur.TenantID != tenantID {
		return store.ModuleDeliveryItem{}, repo.ErrNotFound
	}
	next := cur
	rows, err := fn(&next)
	if err != nil {
		return cur, err
	}
	if next.Active() {
		if _, dup := m.activeModule(tenantID, next.HostID, next.Module, id); dup {
			return cur, repo.ErrConflict
		}
	}
	next.ID, next.TenantID = cur.ID, cur.TenantID
	m.ms()[id] = next
	m.audit = append(m.audit, rows...)
	return next, nil
}

// moduleOldestFirst orders items by creation time, then id.
func moduleOldestFirst(list []store.ModuleDeliveryItem) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
}

// limited cuts list to limit (<= 0: no limit).
func limited(list []store.ModuleDeliveryItem, limit int) []store.ModuleDeliveryItem {
	if limit > 0 && len(list) > limit {
		return list[:limit]
	}
	return list
}

// ListActiveModuleItemsForAgent implements repo.ModuleDeliveryStore.
func (m *Mem) ListActiveModuleItemsForAgent(_ context.Context, tenantID, agentID string, limit int) ([]store.ModuleDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListActiveModuleItemsForAgent"); err != nil {
		return nil, err
	}
	var out []store.ModuleDeliveryItem
	for _, i := range m.ms() {
		if i.TenantID == tenantID && i.AgentID == agentID && i.Active() {
			out = append(out, i)
		}
	}
	moduleOldestFirst(out)
	return limited(out, limit), nil
}

// ListStaleModuleItems implements repo.ModuleDeliveryStore.
func (m *Mem) ListStaleModuleItems(_ context.Context, now, reportBefore time.Time, limit int) ([]store.ModuleDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListStaleModuleItems"); err != nil {
		return nil, err
	}
	var out []store.ModuleDeliveryItem
	for _, i := range m.ms() {
		if i.Active() && (i.ExpiresAt.Before(now) || (i.State == store.DeliveryFetched && i.UpdatedAt.Before(reportBefore))) {
			out = append(out, i)
		}
	}
	moduleOldestFirst(out)
	return limited(out, limit), nil
}

// PurgeModuleItems implements repo.ModuleDeliveryStore.
func (m *Mem) PurgeModuleItems(_ context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("PurgeModuleItems"); err != nil {
		return 0, err
	}
	var n int64
	for id, i := range m.ms() {
		if !i.Active() && i.UpdatedAt.Before(olderThan) {
			delete(m.modules, id)
			n++
		}
	}
	return n, nil
}
