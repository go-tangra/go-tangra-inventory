// Package memstore is an in-memory repo.Store for the inventory service, used by
// tests and local dev. It filters by tenant (mirroring RLS), implements the full
// inventory surface (identity resolution, snapshots + component-derived stats,
// changes, agents, single-use enrollment tokens, audit), and offers per-method
// error injection via FailNext.
package memstore

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// injected is the error FailNext arms for a given method.
type injectedErr struct{ method string }

func (e injectedErr) Error() string { return "memstore: injected failure in " + e.method }

// Mem is an in-memory store. It is safe for concurrent use.
type Mem struct {
	mu       sync.Mutex
	hosts    map[string]store.Host
	snaps    map[string]store.Snapshot
	changes  []store.Change
	agents   map[string]store.Agent
	tokens   map[string]store.EnrollmentToken // keyed by id
	audit    []store.AuditRow
	failNext map[string]bool
	Now      func() time.Time
}

// New builds an empty store.
func New() *Mem {
	return &Mem{
		hosts:    map[string]store.Host{},
		snaps:    map[string]store.Snapshot{},
		agents:   map[string]store.Agent{},
		tokens:   map[string]store.EnrollmentToken{},
		failNext: map[string]bool{},
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

// FailNext arms the next call to the named method to return an injected error.
func (m *Mem) FailNext(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext[method] = true
}

// fail reports (and disarms) an injected failure for method, if armed.
func (m *Mem) fail(method string) error {
	if m.failNext[method] {
		delete(m.failNext, method)
		return injectedErr{method}
	}
	return nil
}

// Close is a no-op for the in-memory store.
func (m *Mem) Close() {}

// ---- identity resolution

// identityKey chooses the resolving key per precedence: hardware_uuid, then
// machine_id, then hostname.
func identityKey(hwUUID, machineID, hostname string) (key, val string) {
	switch {
	case hwUUID != "":
		return store.IdentityHardwareUUID, hwUUID
	case machineID != "":
		return store.IdentityMachineID, machineID
	default:
		return store.IdentityHostname, hostname
	}
}

// lookupHost finds a host in tenant matching the identity precedence of the
// supplied fields, mirroring the partial-unique-index semantics.
func (m *Mem) lookupHost(tenantID, hwUUID, machineID, hostname string) (store.Host, bool) {
	key, _ := identityKey(hwUUID, machineID, hostname)
	for _, h := range m.hosts {
		if h.TenantID != tenantID {
			continue
		}
		switch key {
		case store.IdentityHardwareUUID:
			if h.HardwareUUID == hwUUID {
				return h, true
			}
		case store.IdentityMachineID:
			if h.HardwareUUID == "" && h.MachineID == machineID {
				return h, true
			}
		case store.IdentityHostname:
			if h.HardwareUUID == "" && h.MachineID == "" && h.Hostname == hostname {
				return h, true
			}
		}
	}
	return store.Host{}, false
}

// ---- hosts

func (m *Mem) ResolveHost(_ context.Context, tenantID string, h store.Host) (store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ResolveHost"); err != nil {
		return store.Host{}, err
	}
	now := m.Now()
	key, _ := identityKey(h.HardwareUUID, h.MachineID, h.Hostname)
	if existing, ok := m.lookupHost(tenantID, h.HardwareUUID, h.MachineID, h.Hostname); ok {
		merged := mergeHost(existing, h)
		merged.IdentityKey = key
		if !h.LastSeen.IsZero() {
			if h.LastSeen.After(merged.LastSeen) {
				merged.LastSeen = h.LastSeen
			}
		} else {
			merged.LastSeen = now
		}
		merged.UpdatedAt = now
		m.hosts[merged.ID] = merged
		return merged, nil
	}
	// insert new
	if h.ID == "" {
		h.ID = store.NewID()
	}
	h.TenantID = tenantID
	h.IdentityKey = key
	if h.Status == "" {
		h.Status = store.HostActive
	}
	if h.FirstSeen.IsZero() {
		h.FirstSeen = now
	}
	if h.LastSeen.IsZero() {
		h.LastSeen = now
	}
	if h.Tags == nil {
		h.Tags = map[string]string{}
	}
	h.CreatedAt = now
	h.UpdatedAt = now
	m.hosts[h.ID] = h
	return h, nil
}

// mergeHost overlays the non-empty summary fields of src onto dst.
func mergeHost(dst, src store.Host) store.Host {
	if src.Hostname != "" {
		dst.Hostname = src.Hostname
	}
	if src.MachineID != "" {
		dst.MachineID = src.MachineID
	}
	if src.HardwareUUID != "" {
		dst.HardwareUUID = src.HardwareUUID
	}
	if src.SystemSerial != "" {
		dst.SystemSerial = src.SystemSerial
	}
	if src.Manufacturer != "" {
		dst.Manufacturer = src.Manufacturer
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.OSName != "" {
		dst.OSName = src.OSName
	}
	if src.OSVersion != "" {
		dst.OSVersion = src.OSVersion
	}
	if src.OSArch != "" {
		dst.OSArch = src.OSArch
	}
	if src.AgentVersion != "" {
		dst.AgentVersion = src.AgentVersion
	}
	if src.AssignedUser != "" {
		dst.AssignedUser = src.AssignedUser
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.Tags != nil {
		dst.Tags = src.Tags
	}
	if src.LastSnapshotID != "" {
		dst.LastSnapshotID = src.LastSnapshotID
	}
	return dst
}

func (m *Mem) GetHost(_ context.Context, tenantID, id string) (store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[id]
	if !ok || h.TenantID != tenantID {
		return store.Host{}, repo.ErrNotFound
	}
	return h, nil
}

func (m *Mem) GetHostByIdentity(_ context.Context, tenantID string, id store.Identity) (store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.lookupHost(tenantID, id.HardwareUUID, id.MachineID, id.Hostname)
	if !ok {
		return store.Host{}, repo.ErrNotFound
	}
	return h, nil
}

func (m *Mem) ListHosts(_ context.Context, tenantID string, f store.HostFilter) ([]store.Host, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tagKey, tagVal, tagHasVal := parseTag(f.Tag)
	var out []store.Host
	for _, h := range m.hosts {
		if h.TenantID != tenantID {
			continue
		}
		if f.Hostname != "" && !strings.Contains(strings.ToLower(h.Hostname), strings.ToLower(f.Hostname)) {
			continue
		}
		if f.OSName != "" && h.OSName != f.OSName {
			continue
		}
		if f.Manufacturer != "" && h.Manufacturer != f.Manufacturer {
			continue
		}
		if f.Status != "" && h.Status != f.Status {
			continue
		}
		if f.Tag != "" {
			v, ok := h.Tags[tagKey]
			if !ok {
				continue
			}
			if tagHasVal && v != tagVal {
				continue
			}
		}
		if f.LastSeenFrom != nil && h.LastSeen.Before(*f.LastSeenFrom) {
			continue
		}
		if f.LastSeenTo != nil && h.LastSeen.After(*f.LastSeenTo) {
			continue
		}
		out = append(out, h)
	}
	// Newest first via id-desc keyset (uuid v7 ids are time-ordered); cursorID is
	// the last id returned by the previous page.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if f.CursorID != "" {
		filtered := out[:0]
		for _, h := range out {
			if h.ID < f.CursorID {
				filtered = append(filtered, h)
			}
		}
		out = filtered
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func parseTag(tag string) (key, val string, hasVal bool) {
	if tag == "" {
		return "", "", false
	}
	if i := strings.IndexByte(tag, '='); i >= 0 {
		return tag[:i], tag[i+1:], true
	}
	return tag, "", false
}

func (m *Mem) SetHostTags(_ context.Context, tenantID, id string, tags map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("SetHostTags"); err != nil {
		return err
	}
	h, ok := m.hosts[id]
	if !ok || h.TenantID != tenantID {
		return repo.ErrNotFound
	}
	if tags == nil {
		tags = map[string]string{}
	}
	h.Tags = tags
	h.UpdatedAt = m.Now()
	m.hosts[id] = h
	return nil
}

func (m *Mem) RetireHost(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("RetireHost"); err != nil {
		return err
	}
	h, ok := m.hosts[id]
	if !ok || h.TenantID != tenantID {
		return repo.ErrNotFound
	}
	h.Status = store.HostRetired
	h.UpdatedAt = m.Now()
	m.hosts[id] = h
	return nil
}

func (m *Mem) DeleteHost(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteHost"); err != nil {
		return err
	}
	h, ok := m.hosts[id]
	if !ok || h.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.hosts, id)
	// Cascade: drop this host's snapshots, changes and unbind agents.
	for sid, s := range m.snaps {
		if s.HostID == id {
			delete(m.snaps, sid)
		}
	}
	kept := m.changes[:0]
	for _, c := range m.changes {
		if c.HostID != id {
			kept = append(kept, c)
		}
	}
	m.changes = kept
	for aid, a := range m.agents {
		if a.HostID == id {
			a.HostID = ""
			m.agents[aid] = a
		}
	}
	return nil
}

func (m *Mem) MarkStaleHosts(_ context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, h := range m.hosts {
		if h.Status == store.HostActive && h.LastSeen.Before(olderThan) {
			h.Status = store.HostStale
			h.UpdatedAt = m.Now()
			m.hosts[id] = h
			n++
		}
	}
	return n, nil
}

// ---- snapshots

func (m *Mem) InsertSnapshot(_ context.Context, s store.Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("InsertSnapshot"); err != nil {
		return err
	}
	if s.ID == "" {
		s.ID = store.NewID()
	}
	if s.ReceivedAt.IsZero() {
		s.ReceivedAt = m.Now()
	}
	if s.Source == "" {
		s.Source = store.SourceAgent
	}
	m.snaps[s.ID] = s
	// Advance host pointer/summary.
	if h, ok := m.hosts[s.HostID]; ok && h.TenantID == s.TenantID {
		h.LastSnapshotID = s.ID
		if s.CollectedAt.After(h.LastSeen) {
			h.LastSeen = s.CollectedAt
		}
		if s.OSName != "" {
			h.OSName = s.OSName
		}
		if s.OSVersion != "" {
			h.OSVersion = s.OSVersion
		}
		if s.Manufacturer != "" {
			h.Manufacturer = s.Manufacturer
		}
		if s.Model != "" {
			h.Model = s.Model
		}
		h.UpdatedAt = m.Now()
		m.hosts[h.ID] = h
	}
	return nil
}

func (m *Mem) GetSnapshot(_ context.Context, tenantID, id string) (store.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snaps[id]
	if !ok || s.TenantID != tenantID {
		return store.Snapshot{}, repo.ErrNotFound
	}
	return s, nil
}

func (m *Mem) ListSnapshotsForHost(_ context.Context, tenantID, hostID string, limit int, cursorID string) ([]store.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Snapshot
	for _, s := range m.snaps {
		if s.TenantID == tenantID && s.HostID == hostID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CollectedAt.Equal(out[j].CollectedAt) {
			return out[i].CollectedAt.After(out[j].CollectedAt)
		}
		return out[i].ID > out[j].ID
	})
	if cursorID != "" {
		filtered := out[:0]
		for _, s := range out {
			if s.ID < cursorID {
				filtered = append(filtered, s)
			}
		}
		out = filtered
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Mem) GetLatestForHost(_ context.Context, tenantID, hostID string) (store.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest store.Snapshot
	found := false
	for _, s := range m.snaps {
		if s.TenantID != tenantID || s.HostID != hostID {
			continue
		}
		if !found || s.CollectedAt.After(latest.CollectedAt) {
			latest = s
			found = true
		}
	}
	if !found {
		return store.Snapshot{}, repo.ErrNotFound
	}
	return latest, nil
}

func (m *Mem) DeleteSnapshot(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("DeleteSnapshot"); err != nil {
		return err
	}
	s, ok := m.snaps[id]
	if !ok || s.TenantID != tenantID {
		return repo.ErrNotFound
	}
	delete(m.snaps, id)
	return nil
}

func (m *Mem) PurgeSnapshots(_ context.Context, olderThan time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Determine the latest snapshot id per host, which is always kept.
	latest := map[string]string{} // hostID -> snapshot id
	latestAt := map[string]time.Time{}
	for _, s := range m.snaps {
		if t, ok := latestAt[s.HostID]; !ok || s.CollectedAt.After(t) {
			latestAt[s.HostID] = s.CollectedAt
			latest[s.HostID] = s.ID
		}
	}
	var n int64
	for id, s := range m.snaps {
		if latest[s.HostID] == id {
			continue // keep each host's latest
		}
		if s.CollectedAt.Before(olderThan) {
			delete(m.snaps, id)
			n++
		}
	}
	return n, nil
}

// ---- changes

func (m *Mem) InsertChanges(_ context.Context, changes []store.Change) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("InsertChanges"); err != nil {
		return err
	}
	for _, c := range changes {
		if c.ID == "" {
			c.ID = store.NewID()
		}
		if c.DetectedAt.IsZero() {
			c.DetectedAt = m.Now()
		}
		m.changes = append(m.changes, c)
	}
	return nil
}

func (m *Mem) ListChangesForHost(_ context.Context, tenantID, hostID string, limit int) ([]store.Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Change
	for _, c := range m.changes {
		if c.TenantID == tenantID && c.HostID == hostID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectedAt.After(out[j].DetectedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Mem) ListChangesForSnapshot(_ context.Context, tenantID, snapshotID string) ([]store.Change, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Change
	for _, c := range m.changes {
		if c.TenantID == tenantID && c.SnapshotID == snapshotID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DetectedAt.After(out[j].DetectedAt) })
	return out, nil
}

// ---- agents

func (m *Mem) CreateAgent(_ context.Context, a store.Agent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateAgent"); err != nil {
		return err
	}
	if _, exists := m.agents[a.ID]; exists {
		return repo.ErrConflict
	}
	if a.EnrolledAt.IsZero() {
		a.EnrolledAt = m.Now()
	}
	if a.LastSeen.IsZero() {
		a.LastSeen = a.EnrolledAt
	}
	m.agents[a.ID] = a
	return nil
}

func (m *Mem) GetAgent(_ context.Context, tenantID, id string) (store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok || a.TenantID != tenantID {
		return store.Agent{}, repo.ErrNotFound
	}
	return a, nil
}

func (m *Mem) GetAgentByID(_ context.Context, id string) (store.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return store.Agent{}, repo.ErrNotFound
	}
	return a, nil
}

func (m *Mem) TouchAgent(_ context.Context, id, version, hostID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("TouchAgent"); err != nil {
		return err
	}
	a, ok := m.agents[id]
	if !ok {
		return repo.ErrNotFound
	}
	if at.IsZero() {
		at = m.Now()
	}
	a.LastSeen = at
	if version != "" {
		a.AgentVersion = version
	}
	if hostID != "" {
		a.HostID = hostID
	}
	m.agents[id] = a
	return nil
}

func (m *Mem) RevokeAgent(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("RevokeAgent"); err != nil {
		return err
	}
	a, ok := m.agents[id]
	if !ok || a.TenantID != tenantID {
		return repo.ErrNotFound
	}
	a.Revoked = true
	m.agents[id] = a
	return nil
}

// ---- enrollment tokens

func (m *Mem) CreateEnrollmentToken(_ context.Context, t store.EnrollmentToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("CreateEnrollmentToken"); err != nil {
		return err
	}
	for _, ex := range m.tokens {
		if ex.TokenHash == t.TokenHash {
			return repo.ErrConflict
		}
	}
	if t.ID == "" {
		t.ID = store.NewID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = m.Now()
	}
	m.tokens[t.ID] = t
	return nil
}

func (m *Mem) ConsumeEnrollmentToken(_ context.Context, tokenHash string, now time.Time) (store.EnrollmentToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ConsumeEnrollmentToken"); err != nil {
		return store.EnrollmentToken{}, err
	}
	for id, t := range m.tokens {
		if t.TokenHash != tokenHash {
			continue
		}
		if t.UsedAt != nil || t.Revoked || !now.Before(t.ExpiresAt) {
			return store.EnrollmentToken{}, repo.ErrConflict
		}
		used := now
		t.UsedAt = &used
		m.tokens[id] = t
		return t, nil
	}
	return store.EnrollmentToken{}, repo.ErrNotFound
}

func (m *Mem) RevokeEnrollmentToken(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("RevokeEnrollmentToken"); err != nil {
		return err
	}
	t, ok := m.tokens[id]
	if !ok || t.TenantID != tenantID {
		return repo.ErrNotFound
	}
	t.Revoked = true
	m.tokens[id] = t
	return nil
}

// ---- statistics

func (m *Mem) TenantStats(_ context.Context, tenantID string, staleBefore time.Time) (repo.Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := repo.Stats{
		HostsByStatus:       map[string]int64{},
		HostsByOS:           map[string]int64{},
		HostsByManufacturer: map[string]int64{},
		TopPrograms:         map[string]int64{},
		OSVersions:          map[string]int64{},
	}
	var hosts []store.Host
	for _, h := range m.hosts {
		if h.TenantID != tenantID {
			continue
		}
		hosts = append(hosts, h)
		st.HostsTotal++
		st.HostsByStatus[h.Status]++
		if h.OSName != "" {
			st.HostsByOS[h.OSName]++
		}
		if h.Manufacturer != "" {
			st.HostsByManufacturer[h.Manufacturer]++
		}
		if h.OSVersion != "" {
			st.OSVersions[h.OSVersion]++
		}
		if h.LastSeen.Before(staleBefore) {
			st.StaleHosts++
		}
	}
	// Latest snapshot per host for hardware/software rollups.
	latest := map[string]store.Snapshot{}
	for _, s := range m.snaps {
		if s.TenantID != tenantID {
			continue
		}
		st.SnapshotsTotal++
		if cur, ok := latest[s.HostID]; !ok || s.CollectedAt.After(cur.CollectedAt) {
			latest[s.HostID] = s
		}
	}
	programs := map[string]int64{}
	for _, s := range latest {
		p := s.Payload
		mem := p.Memory.TotalPhysicalBytes
		if mem == 0 {
			for _, mod := range p.Memory.Modules {
				mem += mod.CapacityBytes
			}
		}
		st.TotalMemoryBytes += mem
		for _, proc := range p.Processors {
			st.TotalCPUCores += int64(proc.CoreCount)
		}
		for _, d := range p.Disks {
			st.TotalDiskBytes += d.SizeBytes
		}
		for _, prog := range p.Programs {
			if prog.Name != "" {
				programs[prog.Name]++
			}
		}
	}
	st.TopPrograms = topN(programs, 20)
	return st, nil
}

// topN keeps the highest-count entries (up to n) from counts.
func topN(counts map[string]int64, n int) map[string]int64 {
	type kv struct {
		k string
		v int64
	}
	items := make([]kv, 0, len(counts))
	for k, v := range counts {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	out := map[string]int64{}
	for i, it := range items {
		if n > 0 && i >= n {
			break
		}
		out[it.k] = it.v
	}
	return out
}

func (m *Mem) TenantIDs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := map[string]bool{}
	for _, h := range m.hosts {
		set[h.TenantID] = true
	}
	for _, s := range m.snaps {
		set[s.TenantID] = true
	}
	for _, a := range m.agents {
		set[a.TenantID] = true
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// ---- audit

func (m *Mem) AppendAudit(_ context.Context, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("AppendAudit"); err != nil {
		return err
	}
	if row.ID == "" {
		row.ID = store.NewID()
	}
	if row.At.IsZero() {
		row.At = m.Now()
	}
	m.audit = append(m.audit, row)
	return nil
}

var _ repo.Store = (*Mem)(nil)
