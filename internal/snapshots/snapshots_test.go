package snapshots_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

// recPub records published events for assertions.
type recPub struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	tenant    string
	eventType string
}

func (p *recPub) Publish(_ context.Context, tenantID, eventType string, _ any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, recEvent{tenant: tenantID, eventType: eventType})
}

func (p *recPub) count(eventType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.eventType == eventType {
			n++
		}
	}
	return n
}

type fixture struct {
	svc  *snapshots.Service
	mem  *memstore.Mem
	pub  *recPub
	subj authz.Subjects
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	mem := memstore.New()
	pub := &recPub{}
	hostsSvc := hosts.New(mem)
	svc := snapshots.New(mem, hostsSvc, pub)
	tenant := store.NewID()
	subj := authz.Subjects{TenantID: tenant, UserID: "u1", Roles: []string{"admin"}, ActorKind: authz.ActorUser}
	return fixture{svc: svc, mem: mem, pub: pub, subj: subj}
}

// baseInv builds a synthetic inventory for a single host.
func baseInv(hostname, hwUUID string, collectedAt time.Time) store.Inventory {
	return store.Inventory{
		Identity:     store.Identity{Hostname: hostname, HardwareUUID: hwUUID},
		CollectedAt:  collectedAt,
		AgentVersion: "1.2.3",
		OS:           store.OSInfo{Name: "Ubuntu", Version: "24.04", Arch: "amd64"},
		System:       store.SystemInfo{Manufacturer: "Dell", ProductName: "OptiPlex", SerialNumber: "SN123"},
		BIOS:         store.BIOSInfo{Vendor: "Dell", Version: "1.0"},
		Disks:        []store.Disk{{Serial: "DISK1", SizeBytes: 500}},
		Programs:     []store.Program{{Name: "vim", Version: "9.0"}},
	}
}

func TestIngest_CreatesOneHost(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	inv := baseInv("web01", "uuid-web01", at)
	snap, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, store.SourceAgent)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if snap.HostID == "" {
		t.Fatalf("snapshot has no host id")
	}

	// A second snapshot for the same identity must reuse the same host.
	inv2 := baseInv("web01", "uuid-web01", at.Add(time.Hour))
	snap2, err := f.svc.Ingest(ctx, f.subj.TenantID, inv2, store.SourceAgent)
	if err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}
	if snap2.HostID != snap.HostID {
		t.Errorf("second ingest created a new host: %s vs %s", snap.HostID, snap2.HostID)
	}

	list, err := f.mem.ListHosts(ctx, f.subj.TenantID, store.HostFilter{})
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("host count = %d, want 1", len(list))
	}

	// Host summary lifted from the payload.
	h, _ := f.mem.GetHost(ctx, f.subj.TenantID, snap.HostID)
	if h.Manufacturer != "Dell" || h.Model != "OptiPlex" || h.OSName != "Ubuntu" {
		t.Errorf("host summary not lifted: %+v", h)
	}
}

func TestIngest_FirstSnapshotHasNoChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	snap, err := f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	changes, _ := f.mem.ListChangesForHost(ctx, f.subj.TenantID, snap.HostID, 0)
	if len(changes) != 0 {
		t.Errorf("first snapshot recorded %d changes, want 0", len(changes))
	}
	if f.pub.count("inventory.snapshot.received") != 1 {
		t.Errorf("SnapshotReceived events = %d, want 1", f.pub.count("inventory.snapshot.received"))
	}
	if f.pub.count("inventory.host.changed") != 0 {
		t.Errorf("HostChanged events = %d, want 0", f.pub.count("inventory.host.changed"))
	}
}

func TestIngest_SecondDifferingSnapshotRecordsChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	_, err := f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)
	if err != nil {
		t.Fatalf("Ingest 1: %v", err)
	}

	inv2 := baseInv("h", "uuid-h", at.Add(time.Hour))
	inv2.BIOS.Version = "2.0"                                                         // modified bios
	inv2.Programs = append(inv2.Programs, store.Program{Name: "git", Version: "2.4"}) // added program
	snap2, err := f.svc.Ingest(ctx, f.subj.TenantID, inv2, store.SourceAgent)
	if err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}

	changes, _ := f.mem.ListChangesForHost(ctx, f.subj.TenantID, snap2.HostID, 0)
	if len(changes) < 2 {
		t.Fatalf("expected >=2 changes, got %d: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.SnapshotID != snap2.ID {
			t.Errorf("change snapshot_id = %q, want %q", c.SnapshotID, snap2.ID)
		}
		if c.HostID != snap2.HostID {
			t.Errorf("change host_id = %q, want %q", c.HostID, snap2.HostID)
		}
		if c.PrevSnapshotID == "" {
			t.Errorf("change missing prev_snapshot_id")
		}
	}
	if f.pub.count("inventory.host.changed") != 1 {
		t.Errorf("HostChanged events = %d, want 1", f.pub.count("inventory.host.changed"))
	}
}

func TestGet_IncludesPayload_ListOmitsIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	snap, err := f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Detail: payload present.
	got, err := f.svc.Get(ctx, f.subj, snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Payload == nil || got.Payload.OS.Name != "Ubuntu" {
		t.Errorf("detail view missing payload: %+v", got)
	}

	// Summary list: payload omitted from JSON.
	list, err := f.svc.ListForHost(ctx, f.subj, snap.HostID, 0, "")
	if err != nil {
		t.Fatalf("ListForHost: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0].Payload != nil {
		t.Errorf("summary view leaked payload: %+v", list[0].Payload)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "\"payload\"") {
		t.Errorf("summary JSON contains payload key: %s", raw)
	}
}

func TestGetLatestForHost(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	_, _ = f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)
	inv2 := baseInv("h", "uuid-h", at.Add(2*time.Hour))
	inv2.OS.Version = "24.10"
	snap2, _ := f.svc.Ingest(ctx, f.subj.TenantID, inv2, store.SourceAgent)

	got, err := f.svc.GetLatestForHost(ctx, f.subj, snap2.HostID)
	if err != nil {
		t.Fatalf("GetLatestForHost: %v", err)
	}
	if got.ID != snap2.ID {
		t.Errorf("latest = %q, want %q", got.ID, snap2.ID)
	}
	if got.Payload == nil || got.Payload.OS.Version != "24.10" {
		t.Errorf("latest payload wrong: %+v", got.Payload)
	}
}

func TestDiffAPI(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	snapA, _ := f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)
	inv2 := baseInv("h", "uuid-h", at.Add(time.Hour))
	inv2.BIOS.Version = "9.9"
	snapB, _ := f.svc.Ingest(ctx, f.subj.TenantID, inv2, store.SourceAgent)

	changes, err := f.svc.Diff(ctx, f.subj, snapA.ID, snapB.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	found := false
	for _, c := range changes {
		if c.Category == "bios" && c.ChangeType == store.ChangeModified {
			found = true
		}
	}
	if !found {
		t.Errorf("Diff did not report bios modification: %+v", changes)
	}
}

func TestDiff_NotFound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Diff(ctx, f.subj, "nope", "nada"); err == nil {
		t.Errorf("expected error for missing snapshots")
	}
}

func TestListChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()

	_, _ = f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)
	inv2 := baseInv("h", "uuid-h", at.Add(time.Hour))
	inv2.BIOS.Version = "3.0"
	snap2, _ := f.svc.Ingest(ctx, f.subj.TenantID, inv2, store.SourceAgent)

	changes, err := f.svc.ListChanges(ctx, f.subj, snap2.HostID)
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(changes) == 0 {
		t.Errorf("ListChanges returned none")
	}
}

func TestDelete_RemovesSnapshot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 0).UTC()
	snap, _ := f.svc.Ingest(ctx, f.subj.TenantID, baseInv("h", "uuid-h", at), store.SourceAgent)

	if err := f.svc.Delete(ctx, f.subj, snap.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.subj, snap.ID); err == nil {
		t.Errorf("snapshot still present after delete")
	}
}

func TestIngest_TenantRequired(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Ingest(context.Background(), "", baseInv("h", "uuid-h", time.Now()), ""); err == nil {
		t.Errorf("expected error for empty tenant")
	}
}

func TestIngest_DefaultsSourceAndCollectedAt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.svc.SetClock(func() time.Time { return time.Unix(1_700_000_500, 0).UTC() })

	inv := baseInv("h", "uuid-h", time.Time{}) // zero collectedAt
	snap, err := f.svc.Ingest(ctx, f.subj.TenantID, inv, "")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if snap.Source != store.SourceAgent {
		t.Errorf("source = %q, want %q", snap.Source, store.SourceAgent)
	}
	if snap.CollectedAt.IsZero() {
		t.Errorf("collected_at not defaulted from clock")
	}
}
