package grpcapi

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/events"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/sealed"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/stats"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

const tenant = "11111111-1111-1111-1111-111111111111"

type kit struct {
	hosts     *HostServer
	snapshots *SnapshotServer
	stats     *StatisticsServer
	agents    *AgentServer
	mem       *memstore.Mem
	snapSvc   *snapshots.Service
}

func newKit(t *testing.T) kit {
	t.Helper()
	mem := memstore.New()
	hostsSvc := hosts.New(mem)
	snapSvc := snapshots.New(mem, hostsSvc, events.HubPublisher{})
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	reg := registry.NewMemory()
	return kit{
		hosts:     &HostServer{Hosts: hostsSvc},
		snapshots: &SnapshotServer{Snapshots: snapSvc},
		stats:     &StatisticsServer{Stats: stats.New(mem)},
		agents:    &AgentServer{Enroll: enroll.New(mem, env), Registry: reg},
		mem:       mem,
		snapSvc:   snapSvc,
	}
}

// withFakeCaller overrides the SPIFFE resolver for the test and restores it.
func withFakeCaller(t *testing.T, id string, ok bool) {
	t.Helper()
	prev := callerFunc
	callerFunc = func(context.Context) (string, bool) { return id, ok }
	t.Cleanup(func() { callerFunc = prev })
}

func (k kit) seed(t *testing.T, hostname string, inv store.Inventory) string {
	t.Helper()
	inv.Identity.Hostname = hostname
	if inv.CollectedAt.IsZero() {
		inv.CollectedAt = time.Now().UTC()
	}
	snap, err := k.snapSvc.Ingest(context.Background(), tenant, inv, store.SourceAgent)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	return snap.HostID
}

func TestHostRPCs(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()
	hostID := k.seed(t, "host-a", store.Inventory{
		Identity: store.Identity{HardwareUUID: "hw-1", MachineID: "m-1"},
		System:   store.SystemInfo{Manufacturer: "Dell"}, OS: store.OSInfo{Name: "Windows"},
	})

	list, err := k.hosts.ListHosts(ctx, &invv1.ListHostsRequest{TenantId: tenant})
	if err != nil || len(list.GetHosts()) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if list.GetHosts()[0].GetStatus() != invv1.HostStatus_HOST_STATUS_ACTIVE {
		t.Fatalf("status: %v", list.GetHosts()[0].GetStatus())
	}

	got, err := k.hosts.GetHost(ctx, &invv1.GetHostRequest{TenantId: tenant, Id: hostID})
	if err != nil || got.GetHostname() != "host-a" {
		t.Fatalf("get: %v %+v", err, got)
	}

	byID, err := k.hosts.GetHostByIdentity(ctx, &invv1.GetHostByIdentityRequest{
		TenantId: tenant, Identity: &invv1.Identity{HardwareUuid: "hw-1"},
	})
	if err != nil || byID.GetId() != hostID {
		t.Fatalf("byidentity: %v %+v", err, byID)
	}

	tagged, err := k.hosts.TagHost(ctx, &invv1.TagHostRequest{TenantId: tenant, Id: hostID, Tags: map[string]string{"env": "prod"}})
	if err != nil || tagged.GetTags()["env"] != "prod" {
		t.Fatalf("tag: %v %+v", err, tagged)
	}

	ret, err := k.hosts.RetireHost(ctx, &invv1.RetireHostRequest{TenantId: tenant, Id: hostID})
	if err != nil || ret.GetStatus() != invv1.HostStatus_HOST_STATUS_RETIRED {
		t.Fatalf("retire: %v %+v", err, ret)
	}

	if _, err := k.hosts.DeleteHost(ctx, &invv1.DeleteHostRequest{TenantId: tenant, Id: hostID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := k.hosts.GetHost(ctx, &invv1.GetHostRequest{TenantId: tenant, Id: hostID}); status.Code(err) != codes.NotFound {
		t.Fatalf("get after delete: want NotFound, got %v", err)
	}
}

func TestSnapshotRPCs(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()
	id := store.Identity{HardwareUUID: "hw-2"}
	hostID := k.seed(t, "host-b", store.Inventory{Identity: id, Programs: []store.Program{{Name: "curl", Version: "7"}}})
	k.seed(t, "host-b", store.Inventory{Identity: id, Programs: []store.Program{{Name: "curl", Version: "8"}}})

	latest, err := k.snapshots.GetLatestByHost(ctx, &invv1.GetLatestByHostRequest{TenantId: tenant, HostId: hostID})
	if err != nil || latest.GetPayload() == nil {
		t.Fatalf("latest: %v payload=%v", err, latest.GetPayload())
	}

	hist, err := k.snapshots.ListSnapshots(ctx, &invv1.ListSnapshotsRequest{TenantId: tenant, HostId: hostID})
	if err != nil || len(hist.GetSnapshots()) != 2 {
		t.Fatalf("history: %v n=%d", err, len(hist.GetSnapshots()))
	}
	a := hist.GetSnapshots()[0].GetId()
	b := hist.GetSnapshots()[1].GetId()

	diff, err := k.snapshots.DiffSnapshots(ctx, &invv1.DiffSnapshotsRequest{TenantId: tenant, Id: a, Other: b})
	if err != nil || len(diff.GetChanges()) == 0 {
		t.Fatalf("diff: %v n=%d", err, len(diff.GetChanges()))
	}

	changes, err := k.snapshots.ListChanges(ctx, &invv1.ListChangesRequest{TenantId: tenant, HostId: hostID})
	if err != nil || len(changes.GetChanges()) == 0 {
		t.Fatalf("changes: %v", err)
	}

	if _, err := k.snapshots.GetSnapshot(ctx, &invv1.GetSnapshotRequest{TenantId: tenant, Id: a}); err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	if _, err := k.snapshots.DeleteSnapshot(ctx, &invv1.DeleteSnapshotRequest{TenantId: tenant, Id: a}); err != nil {
		t.Fatalf("delete snapshot: %v", err)
	}
}

func TestStatisticsRPC(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()
	k.seed(t, "host-c", store.Inventory{Identity: store.Identity{HardwareUUID: "hw-3"}, OS: store.OSInfo{Name: "macOS"}})

	st, err := k.stats.GetStatistics(ctx, &invv1.GetStatisticsRequest{TenantId: tenant})
	if err != nil || st.GetHostsTotal() != 1 {
		t.Fatalf("stats: %v %+v", err, st)
	}
}

func TestAgentRPCs(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()

	minted, err := k.agents.MintEnrollmentToken(ctx, &invv1.MintEnrollmentTokenRequest{TenantId: tenant, Label: "fleet", TtlSeconds: 3600})
	if err != nil || minted.GetToken() == "" {
		t.Fatalf("mint: %v %+v", err, minted)
	}
	if minted.GetId() == "" || minted.GetLabel() != "fleet" {
		t.Fatalf("mint fields: %+v", minted)
	}

	// Refresh with no connected agent: delivered=false, but a command id is set.
	ref, err := k.agents.RefreshInventory(ctx, &invv1.RefreshInventoryRequest{TenantId: tenant, HostId: "unknown"})
	if err != nil || ref.GetDelivered() || ref.GetCommandId() == "" {
		t.Fatalf("refresh: %v %+v", err, ref)
	}

	// Enroll an agent using the minted secret, then revoke it.
	agentID, _, err := k.agents.Enroll.Enroll(ctx, minted.GetToken(), store.Identity{Hostname: "h"}, "1.0")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if _, err := k.agents.RevokeAgent(ctx, &invv1.RevokeAgentRequest{TenantId: tenant, Id: agentID}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	list, err := k.agents.ListConnectedAgents(ctx, &invv1.ListConnectedAgentsRequest{TenantId: tenant})
	if err != nil || len(list.GetAgents()) != 0 {
		t.Fatalf("list connected: %v %+v", err, list)
	}
}

func TestUnauthenticated(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "", false)
	_, err := k.hosts.GetHost(context.Background(), &invv1.GetHostRequest{TenantId: tenant, Id: "x"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestInvalidArgument(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	_, err := k.hosts.GetHost(context.Background(), &invv1.GetHostRequest{TenantId: "not-a-uuid", Id: "x"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}
