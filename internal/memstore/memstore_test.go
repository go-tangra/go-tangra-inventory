package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

const tenant = "t1"

func ctx() context.Context { return context.Background() }

func TestInjectedErrorString(t *testing.T) {
	e := injectedErr{"Foo"}
	if e.Error() != "memstore: injected failure in Foo" {
		t.Fatalf("unexpected: %q", e.Error())
	}
}

func TestClose(t *testing.T) {
	New().Close() // no-op, just for coverage
}

// TestFailNextAllMethods arms every method that consults fail() and asserts the
// injected error surfaces and is then disarmed.
func TestFailNextAllMethods(t *testing.T) {
	type call struct {
		method string
		run    func(m *Mem) error
	}
	calls := []call{
		{"ResolveHost", func(m *Mem) error { _, e := m.ResolveHost(ctx(), tenant, store.Host{Hostname: "h"}); return e }},
		{"SetHostTags", func(m *Mem) error { return m.SetHostTags(ctx(), tenant, "x", nil) }},
		{"RetireHost", func(m *Mem) error { return m.RetireHost(ctx(), tenant, "x") }},
		{"DeleteHost", func(m *Mem) error { return m.DeleteHost(ctx(), tenant, "x") }},
		{"InsertSnapshot", func(m *Mem) error { return m.InsertSnapshot(ctx(), store.Snapshot{TenantID: tenant, HostID: "h"}) }},
		{"DeleteSnapshot", func(m *Mem) error { return m.DeleteSnapshot(ctx(), tenant, "x") }},
		{"InsertChanges", func(m *Mem) error { return m.InsertChanges(ctx(), []store.Change{{HostID: "h"}}) }},
		{"CreateAgent", func(m *Mem) error { return m.CreateAgent(ctx(), store.Agent{ID: "a", TenantID: tenant}) }},
		{"TouchAgent", func(m *Mem) error { return m.TouchAgent(ctx(), "a", "", "", time.Time{}) }},
		{"RevokeAgent", func(m *Mem) error { return m.RevokeAgent(ctx(), tenant, "a") }},
		{"CreateEnrollmentToken", func(m *Mem) error {
			return m.CreateEnrollmentToken(ctx(), store.EnrollmentToken{ID: "e", TenantID: tenant})
		}},
		{"ConsumeEnrollmentToken", func(m *Mem) error { _, e := m.ConsumeEnrollmentToken(ctx(), "h", time.Now()); return e }},
		{"RevokeEnrollmentToken", func(m *Mem) error { return m.RevokeEnrollmentToken(ctx(), tenant, "e") }},
		{"AppendAudit", func(m *Mem) error { return m.AppendAudit(ctx(), store.AuditRow{TenantID: tenant}) }},
	}
	for _, c := range calls {
		t.Run(c.method, func(t *testing.T) {
			m := New()
			m.FailNext(c.method)
			var ie injectedErr
			if err := c.run(m); err == nil || !errors.As(err, &ie) {
				t.Fatalf("%s: want injected error, got %v", c.method, err)
			}
			// disarmed: a second call should not return the injected error
			if err := c.run(m); errors.As(err, &ie) {
				t.Fatalf("%s: injected error not disarmed", c.method)
			}
		})
	}
}

func seedHost(t *testing.T, m *Mem, h store.Host) store.Host {
	t.Helper()
	h.TenantID = tenant
	got, err := m.ResolveHost(ctx(), tenant, h)
	if err != nil {
		t.Fatalf("seed host: %v", err)
	}
	return got
}

func TestListHostsFilters(t *testing.T) {
	m := New()
	now := time.Now().UTC()
	m.Now = func() time.Time { return now }

	h1 := seedHost(t, m, store.Host{HardwareUUID: "uuid-1", Hostname: "alpha", OSName: "Windows", Manufacturer: "Dell", Status: store.HostActive})
	h2 := seedHost(t, m, store.Host{HardwareUUID: "uuid-2", Hostname: "beta", OSName: "Linux", Manufacturer: "HP", Status: store.HostActive})
	// tag them
	if err := m.SetHostTags(ctx(), tenant, h1.ID, map[string]string{"env": "prod", "role": "db"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetHostTags(ctx(), tenant, h2.ID, map[string]string{"env": "dev"}); err != nil {
		t.Fatal(err)
	}

	// hostname substring
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Hostname: "alph"}); len(got) != 1 || got[0].ID != h1.ID {
		t.Fatalf("hostname filter: %+v", got)
	}
	// OS
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{OSName: "Linux"}); len(got) != 1 || got[0].ID != h2.ID {
		t.Fatalf("os filter: %+v", got)
	}
	// manufacturer
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Manufacturer: "Dell"}); len(got) != 1 {
		t.Fatalf("mfr filter: %+v", got)
	}
	// status
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Status: store.HostActive}); len(got) != 2 {
		t.Fatalf("status filter: %+v", got)
	}
	// tag key only
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Tag: "role"}); len(got) != 1 || got[0].ID != h1.ID {
		t.Fatalf("tag key filter: %+v", got)
	}
	// tag key=value match
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Tag: "env=prod"}); len(got) != 1 || got[0].ID != h1.ID {
		t.Fatalf("tag key=value filter: %+v", got)
	}
	// tag key=value no match
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Tag: "env=staging"}); len(got) != 0 {
		t.Fatalf("tag no-match filter: %+v", got)
	}
	// tag key absent
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Tag: "missing"}); len(got) != 0 {
		t.Fatalf("tag absent filter: %+v", got)
	}
	// last-seen range
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{LastSeenFrom: &from, LastSeenTo: &to}); len(got) != 2 {
		t.Fatalf("last-seen range: %+v", got)
	}
	future := now.Add(2 * time.Hour)
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{LastSeenFrom: &future}); len(got) != 0 {
		t.Fatalf("last-seen from future: %+v", got)
	}
	past := now.Add(-2 * time.Hour)
	if got, _ := m.ListHosts(ctx(), tenant, store.HostFilter{LastSeenTo: &past}); len(got) != 0 {
		t.Fatalf("last-seen to past: %+v", got)
	}
	// limit + cursor: id-desc ordering
	page1, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Limit: 1})
	if len(page1) != 1 {
		t.Fatalf("limit page1: %+v", page1)
	}
	page2, _ := m.ListHosts(ctx(), tenant, store.HostFilter{Limit: 1, CursorID: page1[0].ID})
	if len(page2) != 1 || page2[0].ID >= page1[0].ID {
		t.Fatalf("cursor paging: page1=%v page2=%v", page1, page2)
	}
	// other tenant sees nothing
	if got, _ := m.ListHosts(ctx(), "other", store.HostFilter{}); len(got) != 0 {
		t.Fatalf("tenant isolation: %+v", got)
	}
}

func TestConsumeEnrollmentTokenStates(t *testing.T) {
	m := New()
	now := time.Now().UTC()

	// valid
	if err := m.CreateEnrollmentToken(ctx(), store.EnrollmentToken{ID: "valid", TenantID: tenant, TokenHash: "hv", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConsumeEnrollmentToken(ctx(), "hv", now); err != nil {
		t.Fatalf("valid consume: %v", err)
	}
	// used (second consume of same hash -> conflict)
	if _, err := m.ConsumeEnrollmentToken(ctx(), "hv", now); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("used token: want conflict, got %v", err)
	}
	// expired
	if err := m.CreateEnrollmentToken(ctx(), store.EnrollmentToken{ID: "exp", TenantID: tenant, TokenHash: "he", ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConsumeEnrollmentToken(ctx(), "he", now); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("expired token: want conflict, got %v", err)
	}
	// revoked
	if err := m.CreateEnrollmentToken(ctx(), store.EnrollmentToken{ID: "rev", TenantID: tenant, TokenHash: "hr", ExpiresAt: now.Add(time.Hour), Revoked: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ConsumeEnrollmentToken(ctx(), "hr", now); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("revoked token: want conflict, got %v", err)
	}
	// unknown hash
	if _, err := m.ConsumeEnrollmentToken(ctx(), "nope", now); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("unknown token: want not-found, got %v", err)
	}
	// duplicate hash -> conflict on create
	if err := m.CreateEnrollmentToken(ctx(), store.EnrollmentToken{ID: "dup", TenantID: tenant, TokenHash: "he"}); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("dup token hash: want conflict, got %v", err)
	}
	// revoke by id
	if err := m.RevokeEnrollmentToken(ctx(), tenant, "exp"); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if err := m.RevokeEnrollmentToken(ctx(), tenant, "missing"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("revoke missing token: %v", err)
	}
	if err := m.RevokeEnrollmentToken(ctx(), "other", "exp"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("revoke token wrong tenant: %v", err)
	}
}

func TestMarkStaleHosts(t *testing.T) {
	m := New()
	base := time.Now().UTC()
	old := seedHost(t, m, store.Host{HardwareUUID: "old", Hostname: "old", LastSeen: base.Add(-48 * time.Hour)})
	fresh := seedHost(t, m, store.Host{HardwareUUID: "new", Hostname: "new", LastSeen: base})

	n, err := m.MarkStaleHosts(ctx(), base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 marked stale, got %d", n)
	}
	go1, _ := m.GetHost(ctx(), tenant, old.ID)
	if go1.Status != store.HostStale {
		t.Fatalf("old host not stale: %s", go1.Status)
	}
	go2, _ := m.GetHost(ctx(), tenant, fresh.ID)
	if go2.Status != store.HostActive {
		t.Fatalf("fresh host should stay active: %s", go2.Status)
	}
}

func TestPurgeSnapshotsKeepsLatest(t *testing.T) {
	m := New()
	h := seedHost(t, m, store.Host{HardwareUUID: "h", Hostname: "h"})
	base := time.Now().UTC()
	mustSnap := func(id string, at time.Time) {
		if err := m.InsertSnapshot(ctx(), store.Snapshot{ID: id, TenantID: tenant, HostID: h.ID, CollectedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	mustSnap("s-old1", base.Add(-72*time.Hour))
	mustSnap("s-old2", base.Add(-48*time.Hour))
	mustSnap("s-latest", base) // newest, must be kept

	n, err := m.PurgeSnapshots(ctx(), base.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 purged, got %d", n)
	}
	if _, err := m.GetSnapshot(ctx(), tenant, "s-latest"); err != nil {
		t.Fatalf("latest snapshot must be kept: %v", err)
	}
	if _, err := m.GetSnapshot(ctx(), tenant, "s-old1"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("old snapshot should be gone: %v", err)
	}
}

func TestTenantStatsAggregation(t *testing.T) {
	m := New()
	staleBefore := time.Now().UTC().Add(-24 * time.Hour)
	h := seedHost(t, m, store.Host{
		HardwareUUID: "h1", Hostname: "h1", OSName: "Windows", OSVersion: "11",
		Manufacturer: "Dell", Status: store.HostActive, LastSeen: time.Now().UTC(),
	})
	// a stale host (last seen long ago)
	seedHost(t, m, store.Host{HardwareUUID: "h2", Hostname: "h2", OSName: "Linux", Manufacturer: "HP", LastSeen: staleBefore.Add(-time.Hour)})

	inv := store.Inventory{
		Memory:     store.MemoryInfo{TotalPhysicalBytes: 16 << 30},
		Processors: []store.Processor{{CoreCount: 8}},
		Disks:      []store.Disk{{SizeBytes: 512 << 30}},
		Programs:   []store.Program{{Name: "Chrome"}, {Name: "Chrome"}, {Name: "vim"}},
	}
	// Older snapshot with modules-based memory (exercises the module fallback path).
	if err := m.InsertSnapshot(ctx(), store.Snapshot{TenantID: tenant, HostID: h.ID, CollectedAt: time.Now().Add(-time.Hour),
		Payload: store.Inventory{Memory: store.MemoryInfo{Modules: []store.MemoryModule{{CapacityBytes: 8 << 30}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.InsertSnapshot(ctx(), store.Snapshot{TenantID: tenant, HostID: h.ID, CollectedAt: time.Now(), Payload: inv}); err != nil {
		t.Fatal(err)
	}

	st, err := m.TenantStats(ctx(), tenant, staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	if st.HostsTotal != 2 {
		t.Fatalf("hosts total: %d", st.HostsTotal)
	}
	if st.StaleHosts != 1 {
		t.Fatalf("stale hosts: %d", st.StaleHosts)
	}
	if st.HostsByOS["Windows"] != 1 || st.HostsByOS["Linux"] != 1 {
		t.Fatalf("hosts by os: %+v", st.HostsByOS)
	}
	if st.TotalCPUCores != 8 {
		t.Fatalf("cpu cores: %d", st.TotalCPUCores)
	}
	if st.TotalMemoryBytes != 16<<30 {
		t.Fatalf("memory (should use latest snapshot): %d", st.TotalMemoryBytes)
	}
	if st.TotalDiskBytes != 512<<30 {
		t.Fatalf("disk: %d", st.TotalDiskBytes)
	}
	if st.TopPrograms["Chrome"] != 2 || st.TopPrograms["vim"] != 1 {
		t.Fatalf("top programs: %+v", st.TopPrograms)
	}

	// TenantIDs reflects seeded data.
	ids, err := m.TenantIDs(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != tenant {
		t.Fatalf("tenant ids: %+v", ids)
	}
}

func TestTopNTruncates(t *testing.T) {
	counts := map[string]int64{"a": 5, "b": 4, "c": 3, "d": 2}
	got := topN(counts, 2)
	if len(got) != 2 || got["a"] != 5 || got["b"] != 4 {
		t.Fatalf("topN(2): %+v", got)
	}
}

func TestAgentLifecycleAndGetByID(t *testing.T) {
	m := New()
	if err := m.CreateAgent(ctx(), store.Agent{ID: "a1", TenantID: tenant}); err != nil {
		t.Fatal(err)
	}
	// duplicate -> conflict
	if err := m.CreateAgent(ctx(), store.Agent{ID: "a1", TenantID: tenant}); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("dup agent: %v", err)
	}
	// GetAgentByID (no tenant scope)
	if a, err := m.GetAgentByID(ctx(), "a1"); err != nil || a.ID != "a1" {
		t.Fatalf("GetAgentByID: %v %+v", err, a)
	}
	if _, err := m.GetAgentByID(ctx(), "missing"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("GetAgentByID missing: %v", err)
	}
	// GetAgent tenant-scoped
	if _, err := m.GetAgent(ctx(), "other", "a1"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("GetAgent wrong tenant: %v", err)
	}
	// Touch with version + hostID
	when := time.Now().UTC()
	if err := m.TouchAgent(ctx(), "a1", "v2", "host-9", when); err != nil {
		t.Fatal(err)
	}
	a, _ := m.GetAgent(ctx(), tenant, "a1")
	if a.AgentVersion != "v2" || a.HostID != "host-9" || !a.LastSeen.Equal(when) {
		t.Fatalf("touch not applied: %+v", a)
	}
	if err := m.TouchAgent(ctx(), "missing", "", "", time.Time{}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("touch missing: %v", err)
	}
	// Revoke
	if err := m.RevokeAgent(ctx(), tenant, "a1"); err != nil {
		t.Fatal(err)
	}
	if a, _ := m.GetAgent(ctx(), tenant, "a1"); !a.Revoked {
		t.Fatal("agent should be revoked")
	}
	if err := m.RevokeAgent(ctx(), tenant, "missing"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("revoke missing agent: %v", err)
	}
}

func TestDeleteHostCascade(t *testing.T) {
	m := New()
	h := seedHost(t, m, store.Host{HardwareUUID: "h", Hostname: "h"})
	if err := m.InsertSnapshot(ctx(), store.Snapshot{ID: "s1", TenantID: tenant, HostID: h.ID, CollectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := m.InsertChanges(ctx(), []store.Change{{TenantID: tenant, HostID: h.ID, SnapshotID: "s1", ChangeType: store.ChangeAdded}}); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateAgent(ctx(), store.Agent{ID: "ag", TenantID: tenant, HostID: h.ID}); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteHost(ctx(), tenant, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSnapshot(ctx(), tenant, "s1"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("snapshot should cascade delete: %v", err)
	}
	ch, _ := m.ListChangesForHost(ctx(), tenant, h.ID, 0)
	if len(ch) != 0 {
		t.Fatalf("changes should cascade delete: %+v", ch)
	}
	ag, _ := m.GetAgentByID(ctx(), "ag")
	if ag.HostID != "" {
		t.Fatalf("agent host binding should be cleared: %+v", ag)
	}
	// deleting missing host
	if err := m.DeleteHost(ctx(), tenant, "gone"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("delete missing host: %v", err)
	}
}

func TestChangesQueries(t *testing.T) {
	m := New()
	h := seedHost(t, m, store.Host{HardwareUUID: "h", Hostname: "h"})
	base := time.Now().UTC()
	changes := []store.Change{
		{TenantID: tenant, HostID: h.ID, SnapshotID: "s1", DetectedAt: base.Add(-time.Hour), ChangeType: store.ChangeAdded},
		{TenantID: tenant, HostID: h.ID, SnapshotID: "s1", DetectedAt: base, ChangeType: store.ChangeModified},
		{TenantID: tenant, HostID: h.ID, SnapshotID: "s2", DetectedAt: base, ChangeType: store.ChangeRemoved},
	}
	if err := m.InsertChanges(ctx(), changes); err != nil {
		t.Fatal(err)
	}
	byHost, _ := m.ListChangesForHost(ctx(), tenant, h.ID, 0)
	if len(byHost) != 3 {
		t.Fatalf("changes for host: %d", len(byHost))
	}
	// limited + newest first
	limited, _ := m.ListChangesForHost(ctx(), tenant, h.ID, 1)
	if len(limited) != 1 {
		t.Fatalf("limited changes: %d", len(limited))
	}
	bySnap, _ := m.ListChangesForSnapshot(ctx(), tenant, "s1")
	if len(bySnap) != 2 {
		t.Fatalf("changes for snapshot s1: %d", len(bySnap))
	}
	if none, _ := m.ListChangesForSnapshot(ctx(), "other", "s1"); len(none) != 0 {
		t.Fatalf("changes tenant isolation: %+v", none)
	}
}

func TestSnapshotQueriesAndPaging(t *testing.T) {
	m := New()
	h := seedHost(t, m, store.Host{HardwareUUID: "h", Hostname: "h"})
	base := time.Now().UTC()
	for i, id := range []string{"sa", "sb", "sc"} {
		if err := m.InsertSnapshot(ctx(), store.Snapshot{ID: id, TenantID: tenant, HostID: h.ID, CollectedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	page1, _ := m.ListSnapshotsForHost(ctx(), tenant, h.ID, 2, "")
	if len(page1) != 2 {
		t.Fatalf("snapshot page1: %d", len(page1))
	}
	page2, _ := m.ListSnapshotsForHost(ctx(), tenant, h.ID, 2, page1[len(page1)-1].ID)
	if len(page2) != 1 {
		t.Fatalf("snapshot page2: %d", len(page2))
	}
	latest, err := m.GetLatestForHost(ctx(), tenant, h.ID)
	if err != nil || latest.ID != "sc" {
		t.Fatalf("latest snapshot: %v %+v", err, latest)
	}
	if _, err := m.GetLatestForHost(ctx(), tenant, "no-host"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("latest for unknown host: %v", err)
	}
	if err := m.DeleteSnapshot(ctx(), tenant, "sa"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSnapshot(ctx(), tenant, "missing"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("delete missing snapshot: %v", err)
	}
}

func TestResolveHostMergeAndIdentity(t *testing.T) {
	m := New()
	// Insert with hostname-only identity.
	h1 := seedHost(t, m, store.Host{Hostname: "box", OSName: "Linux"})
	if h1.IdentityKey != store.IdentityHostname {
		t.Fatalf("identity key: %s", h1.IdentityKey)
	}
	// Resolve again with a hardware uuid; hostname-only match should NOT merge
	// (different identity precedence -> new host).
	h2 := seedHost(t, m, store.Host{HardwareUUID: "uuid-x", Hostname: "box", OSName: "Linux", Model: "T480"})
	if h2.ID == h1.ID {
		t.Fatal("hw-uuid host should be distinct from hostname-only host")
	}
	// Re-resolve same hw uuid merges and overlays fields.
	h3 := seedHost(t, m, store.Host{HardwareUUID: "uuid-x", AssignedUser: "alice", LastSeen: time.Now().Add(time.Hour)})
	if h3.ID != h2.ID {
		t.Fatal("same hw uuid should merge")
	}
	if h3.AssignedUser != "alice" || h3.Model != "T480" {
		t.Fatalf("merge overlay wrong: %+v", h3)
	}
	// GetHostByIdentity
	if got, err := m.GetHostByIdentity(ctx(), tenant, store.Identity{HardwareUUID: "uuid-x"}); err != nil || got.ID != h2.ID {
		t.Fatalf("GetHostByIdentity: %v %+v", err, got)
	}
	if _, err := m.GetHostByIdentity(ctx(), tenant, store.Identity{HardwareUUID: "none"}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("GetHostByIdentity unknown: %v", err)
	}
	// GetHost wrong tenant
	if _, err := m.GetHost(ctx(), "other", h1.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("GetHost wrong tenant: %v", err)
	}
	// SetHostTags/Retire on wrong tenant
	if err := m.SetHostTags(ctx(), "other", h1.ID, nil); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("SetHostTags wrong tenant: %v", err)
	}
	if err := m.RetireHost(ctx(), "other", h1.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("RetireHost wrong tenant: %v", err)
	}
	if err := m.RetireHost(ctx(), tenant, h1.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetHost(ctx(), tenant, h1.ID); got.Status != store.HostRetired {
		t.Fatalf("retire: %+v", got)
	}
}

func TestAppendAuditDefaults(t *testing.T) {
	m := New()
	if err := m.AppendAudit(ctx(), store.AuditRow{TenantID: tenant, Action: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(m.audit) != 1 || m.audit[0].ID == "" || m.audit[0].At.IsZero() {
		t.Fatalf("audit defaults not filled: %+v", m.audit)
	}
}
