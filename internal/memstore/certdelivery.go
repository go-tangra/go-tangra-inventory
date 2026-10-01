package memstore

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 033: certificate deliveries, items and host certificates.

type certState struct {
	deliveries map[string]store.CertDelivery
	items      map[string]store.CertDeliveryItem
	hostCerts  map[[3]string]store.HostCertificate // tenant, host, name
}

func (m *Mem) cs() *certState {
	if m.cert == nil {
		m.cert = &certState{deliveries: map[string]store.CertDelivery{}, items: map[string]store.CertDeliveryItem{},
			hostCerts: map[[3]string]store.HostCertificate{}}
	}
	return m.cert
}

func cloneDelivery(d store.CertDelivery) store.CertDelivery {
	d.HostIDs = append([]string{}, d.HostIDs...)
	d.HostTags = append([]string{}, d.HostTags...)
	return d
}

// activeFor returns the active item of tenant, host and name other than exceptID.
func (c *certState) activeFor(tenantID, hostID, name, exceptID string) (store.CertDeliveryItem, bool) {
	for _, i := range c.items {
		if i.TenantID == tenantID && i.HostID == hostID && i.Name == name && i.ID != exceptID && i.Active() {
			return i, true
		}
	}
	return store.CertDeliveryItem{}, false
}

// CreateCertDelivery implements repo.CertDeliveryStore.
func (m *Mem) CreateCertDelivery(_ context.Context, n repo.NewCertDelivery) ([]store.CertDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateCertDelivery"); err != nil {
		return nil, err
	}
	c := m.cs()
	d := n.Delivery
	if _, ok := c.deliveries[d.ID]; ok {
		return nil, repo.ErrConflict
	}
	for _, x := range c.deliveries {
		if x.TenantID == d.TenantID && x.Source == d.Source && x.IdempotencyKey == d.IdempotencyKey {
			return nil, repo.ErrConflict
		}
	}
	hosts := map[string]bool{}
	for _, i := range n.Items {
		if _, ok := c.items[i.ID]; ok || hosts[i.HostID] {
			return nil, repo.ErrConflict
		}
		hosts[i.HostID] = true
	}
	// Apply on copies so a failure leaves nothing behind.
	items := map[string]store.CertDeliveryItem{}
	var superseded []store.CertDeliveryItem
	audit := append([]store.AuditRow{}, n.Audit...)
	for _, i := range n.Items {
		if i.Active() {
			if old, ok := c.activeFor(i.TenantID, i.HostID, i.Name, ""); ok {
				at := d.CreatedAt
				old.State, old.Reason, old.UpdatedAt, old.FinishedAt = store.DeliverySuperseded, "", at, &at
				items[old.ID] = old
				superseded = append(superseded, old)
				if n.SupersedeAudit != nil {
					audit = append(audit, n.SupersedeAudit(old, i.ID))
				}
			}
		}
		items[i.ID] = i
	}
	c.deliveries[d.ID] = cloneDelivery(d)
	for id, i := range items {
		c.items[id] = i
	}
	m.audit = append(m.audit, audit...)
	return superseded, nil
}

// GetCertDelivery implements repo.CertDeliveryStore.
func (m *Mem) GetCertDelivery(_ context.Context, tenantID, id string) (store.CertDelivery, []store.CertDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetCertDelivery"); err != nil {
		return store.CertDelivery{}, nil, err
	}
	d, ok := m.cs().deliveries[id]
	if !ok || d.TenantID != tenantID {
		return store.CertDelivery{}, nil, repo.ErrNotFound
	}
	var items []store.CertDeliveryItem
	for _, i := range m.cs().items {
		if i.DeliveryID == id {
			items = append(items, i)
		}
	}
	oldestFirst(items)
	return cloneDelivery(d), items, nil
}

// GetCertDeliveryByKey implements repo.CertDeliveryStore.
func (m *Mem) GetCertDeliveryByKey(_ context.Context, tenantID, source, key string) (store.CertDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetCertDeliveryByKey"); err != nil {
		return store.CertDelivery{}, err
	}
	for _, d := range m.cs().deliveries {
		if d.TenantID == tenantID && d.Source == source && d.IdempotencyKey == key {
			return cloneDelivery(d), nil
		}
	}
	return store.CertDelivery{}, repo.ErrNotFound
}

// GetCertItem implements repo.CertDeliveryStore.
func (m *Mem) GetCertItem(_ context.Context, tenantID, id string) (store.CertDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetCertItem"); err != nil {
		return store.CertDeliveryItem{}, err
	}
	i, ok := m.cs().items[id]
	if !ok || i.TenantID != tenantID {
		return store.CertDeliveryItem{}, repo.ErrNotFound
	}
	return i, nil
}

// UpdateCertItem implements repo.CertDeliveryStore.
func (m *Mem) UpdateCertItem(_ context.Context, tenantID, id string, fn func(*store.CertDeliveryItem) (repo.CertItemChange, error)) (store.CertDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateCertItem"); err != nil {
		return store.CertDeliveryItem{}, err
	}
	c := m.cs()
	cur, ok := c.items[id]
	if !ok || cur.TenantID != tenantID {
		return store.CertDeliveryItem{}, repo.ErrNotFound
	}
	next := cur
	ch, err := fn(&next)
	if err != nil {
		return cur, err
	}
	if next.Active() {
		if _, dup := c.activeFor(tenantID, next.HostID, next.Name, id); dup {
			return cur, repo.ErrConflict
		}
	}
	if err := m.fail("UpdateCertItem.commit"); err != nil { // tests: the transaction fails after fn
		return cur, err
	}
	next.ID, next.TenantID = cur.ID, cur.TenantID
	c.items[id] = next
	if h := ch.HostCert; h != nil {
		hc := *h
		hc.TenantID = tenantID
		c.hostCerts[[3]string{tenantID, hc.HostID, hc.Name}] = hc
	}
	m.audit = append(m.audit, ch.Audit...)
	return next, nil
}

// oldestFirst orders items by creation time, then id.
func oldestFirst(list []store.CertDeliveryItem) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
}

// ListActiveCertItemsForAgent implements repo.CertDeliveryStore.
func (m *Mem) ListActiveCertItemsForAgent(_ context.Context, tenantID, agentID string, limit int) ([]store.CertDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListActiveCertItemsForAgent"); err != nil {
		return nil, err
	}
	var out []store.CertDeliveryItem
	for _, i := range m.cs().items {
		if i.TenantID == tenantID && i.AgentID == agentID && i.Active() {
			out = append(out, i)
		}
	}
	oldestFirst(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func certItemKey(i store.CertDeliveryItem, field string) any {
	switch field {
	case "updated_at":
		return i.UpdatedAt
	case "state":
		return i.State
	case "name":
		return i.Name
	default:
		return i.CreatedAt
	}
}

// ListCertItemsPage implements repo.CertDeliveryStore.
func (m *Mem) ListCertItemsPage(_ context.Context, tenantID string, f repo.CertItemFilter, req listquery.Request) ([]store.CertDeliveryItem, int, listquery.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req = store.ListRequest(req, store.CertItemList)
	if err := m.fail("ListCertItemsPage"); err != nil {
		return nil, 0, req, err
	}
	match := func(want, got string) bool { return want == "" || want == got }
	var all []store.CertDeliveryItem
	for _, i := range m.cs().items {
		if i.TenantID == tenantID && match(f.HostID, i.HostID) && match(f.State, i.State) && match(f.Name, i.Name) &&
			match(f.CertificateID, i.CertificateID) && match(f.DeliveryID, i.DeliveryID) {
			all = append(all, i)
		}
	}
	listquery.SortSlice(all, req, certItemKey, func(i store.CertDeliveryItem) string { return i.ID })
	page, total, applied := listquery.Window(all, req)
	return append([]store.CertDeliveryItem(nil), page...), total, applied, nil
}

// errEmptyScope refuses a CancelCertItems call that would select nothing
// specific (a programming error).
var errEmptyScope = errors.New("memstore: cancel scope is empty")

// CancelCertItems implements repo.CertDeliveryStore.
func (m *Mem) CancelCertItems(_ context.Context, tenantID string, scope repo.CertCancelScope, reason string, row func(store.CertDeliveryItem) store.AuditRow) ([]store.CertDeliveryItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CancelCertItems"); err != nil {
		return nil, err
	}
	if scope.HostID == "" && scope.AgentID == "" && scope.CertificateID == "" {
		return nil, errEmptyScope
	}
	now := m.Now()
	var out []store.CertDeliveryItem
	for id, i := range m.cs().items {
		if i.TenantID != tenantID || !i.Active() ||
			(scope.HostID != "" && i.HostID != scope.HostID) ||
			(scope.AgentID != "" && i.AgentID != scope.AgentID) ||
			(scope.CertificateID != "" && i.CertificateID != scope.CertificateID) {
			continue
		}
		at := now
		i.State, i.Reason, i.UpdatedAt, i.FinishedAt = store.DeliveryCancelled, reason, at, &at
		m.cs().items[id] = i
		out = append(out, i)
	}
	oldestFirst(out)
	for _, i := range out {
		m.audit = append(m.audit, row(i))
	}
	return out, nil
}

// GetHostCertificate implements repo.CertDeliveryStore.
func (m *Mem) GetHostCertificate(_ context.Context, tenantID, hostID, name string) (store.HostCertificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetHostCertificate"); err != nil {
		return store.HostCertificate{}, err
	}
	hc, ok := m.cs().hostCerts[[3]string{tenantID, hostID, name}]
	if !ok {
		return store.HostCertificate{}, repo.ErrNotFound
	}
	return hc, nil
}

// ListHostCertificates implements repo.CertDeliveryStore.
func (m *Mem) ListHostCertificates(_ context.Context, tenantID, hostID string) ([]store.HostCertificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListHostCertificates"); err != nil {
		return nil, err
	}
	var out []store.HostCertificate
	for k, hc := range m.cs().hostCerts {
		if k[0] == tenantID && k[1] == hostID {
			out = append(out, hc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PurgeCertItems implements repo.CertDeliveryStore.
func (m *Mem) PurgeCertItems(_ context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("PurgeCertItems"); err != nil {
		return 0, err
	}
	c := m.cs()
	var n int64
	for id, i := range c.items {
		if !i.Active() && i.UpdatedAt.Before(olderThan) {
			delete(c.items, id)
			n++
		}
	}
	for id, d := range c.deliveries {
		if !d.CreatedAt.Before(olderThan) {
			continue
		}
		empty := true
		for _, i := range c.items {
			if i.DeliveryID == id {
				empty = false
				break
			}
		}
		if empty {
			delete(c.deliveries, id)
		}
	}
	return n, nil
}
