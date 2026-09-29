package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// autoEnrollState holds feature 029 data (lazily created).
type autoEnrollState struct {
	settings map[string]store.AutoEnrollSettings // by tenant
	keys     map[string]store.AutoEnrollKey      // by id
	nonces   map[string]time.Time                // key_id + "\x00" + nonce
}

func (m *Mem) ae() *autoEnrollState {
	if m.aes == nil {
		m.aes = &autoEnrollState{settings: map[string]store.AutoEnrollSettings{}, keys: map[string]store.AutoEnrollKey{}, nonces: map[string]time.Time{}}
	}
	return m.aes
}

func copyKey(k store.AutoEnrollKey) store.AutoEnrollKey {
	k.AllowedCIDRs = append([]string(nil), k.AllowedCIDRs...)
	k.SecretSealed = append([]byte(nil), k.SecretSealed...)
	return k
}

func (m *Mem) GetAutoEnrollSettings(_ context.Context, tenantID string) (store.AutoEnrollSettings, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetAutoEnrollSettings"); err != nil {
		return store.AutoEnrollSettings{}, false, err
	}
	s, ok := m.ae().settings[tenantID]
	if !ok {
		return store.AutoEnrollSettings{TenantID: tenantID}, false, nil
	}
	return s, true, nil
}

func (m *Mem) PutAutoEnrollSettings(_ context.Context, s store.AutoEnrollSettings, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("PutAutoEnrollSettings"); err != nil {
		return err
	}
	m.ae().settings[s.TenantID] = s
	m.audit = append(m.audit, row)
	return nil
}

func (m *Mem) ListAutoEnrollKeys(_ context.Context, tenantID string) ([]store.AutoEnrollKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListAutoEnrollKeys"); err != nil {
		return nil, err
	}
	var out []store.AutoEnrollKey
	for _, k := range m.ae().keys {
		if k.TenantID == tenantID {
			out = append(out, copyKey(k))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) CreateAutoEnrollKey(_ context.Context, k store.AutoEnrollKey, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateAutoEnrollKey"); err != nil {
		return err
	}
	for _, cur := range m.ae().keys {
		if cur.KeyID == k.KeyID || (cur.TenantID == k.TenantID && cur.Name == k.Name) || cur.ID == k.ID {
			return repo.ErrConflict
		}
	}
	m.ae().keys[k.ID] = copyKey(k)
	m.audit = append(m.audit, row)
	return nil
}

func (m *Mem) UpdateAutoEnrollKey(_ context.Context, tenantID, id string, fn func(*store.AutoEnrollKey) (store.AuditRow, error)) (store.AutoEnrollKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateAutoEnrollKey"); err != nil {
		return store.AutoEnrollKey{}, err
	}
	cur, ok := m.ae().keys[id]
	if !ok || cur.TenantID != tenantID {
		return store.AutoEnrollKey{}, repo.ErrNotFound
	}
	next := copyKey(cur)
	row, err := fn(&next)
	if err != nil {
		return store.AutoEnrollKey{}, err
	}
	for _, other := range m.ae().keys {
		if other.ID != id && other.TenantID == tenantID && other.Name == next.Name {
			return store.AutoEnrollKey{}, repo.ErrConflict
		}
	}
	m.ae().keys[id] = copyKey(next)
	m.audit = append(m.audit, row)
	return copyKey(next), nil
}

func (m *Mem) DeleteAutoEnrollKey(_ context.Context, tenantID, id string, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteAutoEnrollKey"); err != nil {
		return err
	}
	cur, ok := m.ae().keys[id]
	if !ok || cur.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.ae().keys, id)
	m.audit = append(m.audit, row)
	return nil
}

func (m *Mem) LookupAutoEnrollKey(_ context.Context, keyID string) (store.AutoEnrollKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("LookupAutoEnrollKey"); err != nil {
		return store.AutoEnrollKey{}, false, err
	}
	for _, k := range m.ae().keys {
		if k.KeyID == keyID {
			return copyKey(k), m.ae().settings[k.TenantID].Enabled, nil
		}
	}
	return store.AutoEnrollKey{}, false, repo.ErrNotFound
}

func (m *Mem) EnrollWithAutoKey(_ context.Context, e store.AutoEnrollment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("EnrollWithAutoKey"); err != nil {
		return err
	}
	st := m.ae()
	for n, seen := range st.nonces {
		if seen.Before(e.PruneBefore) {
			delete(st.nonces, n)
		}
	}
	nk := e.KeyID + "\x00" + e.Nonce
	if _, used := st.nonces[nk]; used {
		return repo.ErrReplay
	}
	k, ok := st.keys[e.KeyUUID]
	if !ok || k.TenantID != e.TenantID || !k.Enabled || k.Expired(e.At) || k.Exhausted() || !st.settings[e.TenantID].Enabled {
		return repo.ErrConflict
	}
	if _, exists := m.agents[e.Agent.ID]; exists {
		return repo.ErrConflict
	}
	st.nonces[nk] = e.At
	k.Enrollments++
	at := e.At
	k.LastUsedAt, k.LastUsedIP = &at, e.IP
	st.keys[k.ID] = k
	m.agents[e.Agent.ID] = e.Agent
	m.audit = append(m.audit, e.Audit)
	return nil
}
