package memstore

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 023: agent releases, platforms, upgrade requests and policies.

type artifactKey struct{ version, os, arch, installType string }

func keyOf(a store.AgentArtifact) artifactKey {
	return artifactKey{a.Version, a.OS, a.Arch, a.InstallType}
}

type upgradeState struct {
	releases map[string]store.AgentRelease
	chunks   map[artifactKey][][]byte
	upgrades map[string]store.AgentUpgrade
	policies map[string]store.AgentUpgradePolicy
	locks    map[string]bool
	lockMu   sync.Mutex
}

func (m *Mem) up() *upgradeState {
	if m.upg == nil {
		m.upg = &upgradeState{releases: map[string]store.AgentRelease{}, chunks: map[artifactKey][][]byte{},
			upgrades: map[string]store.AgentUpgrade{}, policies: map[string]store.AgentUpgradePolicy{}, locks: map[string]bool{}}
	}
	return m.upg
}

// ---- releases

func (m *Mem) ImportAgentRelease(_ context.Context, rel store.AgentRelease, open repo.ArtifactOpener, chunkBytes int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ImportAgentRelease"); err != nil {
		return false, err
	}
	u := m.up()
	if cur, ok := u.releases[rel.Version]; ok {
		if cur.ManifestSHA256 != rel.ManifestSHA256 {
			return false, repo.ErrConflict
		}
		complete := true
		for _, a := range cur.Artifacts {
			complete = complete && a.Complete
		}
		if complete {
			return false, nil
		}
	}
	if chunkBytes <= 0 {
		chunkBytes = 1 << 20
	}
	chunks := map[artifactKey][][]byte{}
	stored := rel
	stored.Artifacts = nil
	for _, a := range rel.Artifacts {
		a.Version = rel.Version
		rc, err := open(a)
		if err != nil {
			return false, err
		}
		var parts [][]byte
		for {
			buf := make([]byte, chunkBytes)
			n, rerr := io.ReadFull(rc, buf)
			if n > 0 {
				parts = append(parts, buf[:n])
			}
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			if rerr != nil {
				_ = rc.Close()
				return false, rerr
			}
		}
		if err := rc.Close(); err != nil {
			return false, err
		}
		a.Complete = true
		chunks[keyOf(a)] = parts
		stored.Artifacts = append(stored.Artifacts, a)
	}
	if stored.ImportedAt.IsZero() {
		stored.ImportedAt = m.Now()
	}
	u.releases[rel.Version] = stored
	for k, v := range chunks {
		u.chunks[k] = v
	}
	return true, nil
}

func (m *Mem) ListAgentReleases(context.Context) ([]store.AgentRelease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListAgentReleases"); err != nil {
		return nil, err
	}
	out := make([]store.AgentRelease, 0, len(m.up().releases))
	for _, r := range m.up().releases {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (m *Mem) GetAgentRelease(_ context.Context, version string) (store.AgentRelease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetAgentRelease"); err != nil {
		return store.AgentRelease{}, err
	}
	r, ok := m.up().releases[version]
	if !ok {
		return store.AgentRelease{}, repo.ErrNotFound
	}
	return r, nil
}

func (m *Mem) ReadArtifactChunk(_ context.Context, a store.AgentArtifact, seq int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ReadArtifactChunk"); err != nil {
		return nil, err
	}
	parts, ok := m.up().chunks[keyOf(a)]
	if !ok || seq < 0 || seq >= len(parts) {
		return nil, repo.ErrNotFound
	}
	return append([]byte(nil), parts[seq]...), nil
}

// CorruptArtifactChunk flips a byte of a stored chunk (tests: a tampered
// artifact in the database).
func (m *Mem) CorruptArtifactChunk(a store.AgentArtifact, seq int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if parts := m.up().chunks[keyOf(a)]; seq < len(parts) && len(parts[seq]) > 0 {
		parts[seq][0] ^= 0xFF
	}
}

func (m *Mem) DeleteAgentRelease(_ context.Context, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteAgentRelease"); err != nil {
		return err
	}
	u := m.up()
	if _, ok := u.releases[version]; !ok {
		return repo.ErrNotFound
	}
	delete(u.releases, version)
	for k := range u.chunks {
		if k.version == version {
			delete(u.chunks, k)
		}
	}
	return nil
}

func (m *Mem) ProtectedAgentVersions(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ProtectedAgentVersions"); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range m.up().policies {
		if p.TargetVersion != "" {
			seen[p.TargetVersion] = true
		}
	}
	for _, u := range m.up().upgrades {
		if u.Active() {
			seen[u.TargetVersion] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

// ---- agents

func (m *Mem) ListAgents(_ context.Context, tenantID string) ([]store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListAgents"); err != nil {
		return nil, err
	}
	var out []store.Agent
	for _, a := range m.agents {
		if a.TenantID == tenantID && !a.Revoked {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Mem) SetAgentPlatform(_ context.Context, agentID, os, arch, installType string, caps []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetAgentPlatform"); err != nil {
		return err
	}
	a, ok := m.agents[agentID]
	if !ok {
		return repo.ErrNotFound
	}
	a.OS, a.Arch, a.InstallType, a.Capabilities, a.PlatformSeenAt = os, arch, installType, append([]string(nil), caps...), at
	m.agents[agentID] = a
	return nil
}

// ---- upgrade requests

func (m *Mem) CreateAgentUpgrade(_ context.Context, u store.AgentUpgrade, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateAgentUpgrade"); err != nil {
		return err
	}
	for _, cur := range m.up().upgrades {
		if cur.TenantID == u.TenantID && cur.AgentID == u.AgentID && cur.Active() {
			return repo.ErrConflict
		}
	}
	m.up().upgrades[u.ID] = u
	m.audit = append(m.audit, row)
	return nil
}

func (m *Mem) UpdateAgentUpgrade(_ context.Context, tenantID, id string, fn func(*store.AgentUpgrade) ([]store.AuditRow, error)) (store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateAgentUpgrade"); err != nil {
		return store.AgentUpgrade{}, err
	}
	cur, ok := m.up().upgrades[id]
	if !ok || cur.TenantID != tenantID {
		return store.AgentUpgrade{}, repo.ErrNotFound
	}
	next := cur
	rows, err := fn(&next)
	if err != nil {
		return cur, err
	}
	if err := m.fail("UpdateAgentUpgrade.commit"); err != nil { // tests: the transaction fails after fn
		return cur, err
	}
	m.up().upgrades[id] = next
	m.audit = append(m.audit, rows...)
	return next, nil
}

func (m *Mem) GetAgentUpgrade(_ context.Context, tenantID, id string) (store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetAgentUpgrade"); err != nil {
		return store.AgentUpgrade{}, err
	}
	u, ok := m.up().upgrades[id]
	if !ok || u.TenantID != tenantID {
		return store.AgentUpgrade{}, repo.ErrNotFound
	}
	return u, nil
}

// newestFirst orders requests by creation time, then id, descending.
func newestFirst(list []store.AgentUpgrade) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		}
		return list[i].ID > list[j].ID
	})
}

func (m *Mem) ListAgentUpgrades(_ context.Context, tenantID string, f repo.UpgradeFilter) ([]store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListAgentUpgrades"); err != nil {
		return nil, err
	}
	var all []store.AgentUpgrade
	for _, u := range m.up().upgrades {
		if u.TenantID == tenantID && (f.State == "" || u.State == f.State) && (f.AgentID == "" || u.AgentID == f.AgentID) {
			all = append(all, u)
		}
	}
	newestFirst(all)
	if f.CursorID != "" {
		for i, u := range all {
			if u.ID == f.CursorID {
				all = all[i+1:]
				break
			}
		}
	}
	if f.Limit > 0 && len(all) > f.Limit {
		all = all[:f.Limit]
	}
	return all, nil
}

func (m *Mem) LatestAgentUpgrades(_ context.Context, tenantID string) (map[string]store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("LatestAgentUpgrades"); err != nil {
		return nil, err
	}
	var all []store.AgentUpgrade
	for _, u := range m.up().upgrades {
		if u.TenantID == tenantID {
			all = append(all, u)
		}
	}
	newestFirst(all)
	out := map[string]store.AgentUpgrade{}
	for _, u := range all {
		if _, ok := out[u.AgentID]; !ok {
			out[u.AgentID] = u
		}
	}
	return out, nil
}

func (m *Mem) ListStaleUpgrades(_ context.Context, now, progressBefore time.Time, limit int) ([]store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListStaleUpgrades"); err != nil {
		return nil, err
	}
	var out []store.AgentUpgrade
	for _, u := range m.up().upgrades {
		switch {
		case (u.State == store.UpgradePending || u.State == store.UpgradeDelivered) && u.ExpiresAt.Before(now):
		case (u.State == store.UpgradeDownloading || u.State == store.UpgradeInstalling) && u.UpdatedAt.Before(progressBefore):
		default:
			continue
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---- policies

func (m *Mem) GetUpgradePolicy(_ context.Context, tenantID string) (store.AgentUpgradePolicy, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("GetUpgradePolicy"); err != nil {
		return store.AgentUpgradePolicy{}, false, err
	}
	p, ok := m.up().policies[tenantID]
	if !ok {
		return store.DefaultUpgradePolicy(tenantID), false, nil
	}
	return p, true, nil
}

func (m *Mem) UpdateUpgradePolicy(_ context.Context, tenantID string, fn func(*store.AgentUpgradePolicy) ([]store.AuditRow, error)) (store.AgentUpgradePolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("UpdateUpgradePolicy"); err != nil {
		return store.AgentUpgradePolicy{}, err
	}
	cur, ok := m.up().policies[tenantID]
	if !ok {
		cur = store.DefaultUpgradePolicy(tenantID)
	}
	next := cur
	rows, err := fn(&next)
	if err != nil {
		return cur, err
	}
	next.TenantID = tenantID
	m.up().policies[tenantID] = next
	m.audit = append(m.audit, rows...)
	return next, nil
}

func (m *Mem) ListEnabledUpgradePolicies(context.Context) ([]store.AgentUpgradePolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ListEnabledUpgradePolicies"); err != nil {
		return nil, err
	}
	var out []store.AgentUpgradePolicy
	for _, p := range m.up().policies {
		if p.Enabled {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TenantID < out[j].TenantID })
	return out, nil
}

func (m *Mem) TryTenantLock(ctx context.Context, key string, fn func(context.Context) error) (bool, error) {
	m.mu.Lock()
	u := m.up()
	err := m.fail("TryTenantLock")
	m.mu.Unlock()
	if err != nil {
		return false, err
	}
	u.lockMu.Lock()
	if u.locks[key] {
		u.lockMu.Unlock()
		return false, nil
	}
	u.locks[key] = true
	u.lockMu.Unlock()
	defer func() {
		u.lockMu.Lock()
		delete(u.locks, key)
		u.lockMu.Unlock()
	}()
	return true, fn(ctx)
}

// AuditRows returns a copy of every audit row written (tests).
func (m *Mem) AuditRows() []store.AuditRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AuditRow(nil), m.audit...)
}
