package inventoryclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	invv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/pkg/inventoryclient"
)

// ---- stub servers

type stubHosts struct {
	invv1.UnimplementedInventoryHostServiceServer
	lastList *invv1.ListHostsRequest
	failGet  bool
}

func (s *stubHosts) ListHosts(_ context.Context, req *invv1.ListHostsRequest) (*invv1.ListHostsResponse, error) {
	s.lastList = req
	return &invv1.ListHostsResponse{Hosts: []*invv1.Host{
		{Id: "h-1", Hostname: "host-a", Status: invv1.HostStatus_HOST_STATUS_ACTIVE, LastSeen: time.Now().Unix(), Tags: map[string]string{"env": "prod"}},
	}}, nil
}

func (s *stubHosts) GetHost(_ context.Context, req *invv1.GetHostRequest) (*invv1.Host, error) {
	if s.failGet {
		return nil, status.Error(codes.NotFound, "not_found")
	}
	return &invv1.Host{Id: req.GetId(), Hostname: "host-a", Status: invv1.HostStatus_HOST_STATUS_STALE}, nil
}

type stubSnapshots struct {
	invv1.UnimplementedInventorySnapshotServiceServer
}

func (s *stubSnapshots) GetLatestByHost(_ context.Context, req *invv1.GetLatestByHostRequest) (*invv1.Snapshot, error) {
	return &invv1.Snapshot{
		Summary: &invv1.SnapshotSummary{
			Id: "s-1", HostId: req.GetHostId(), Source: invv1.SnapshotSource_SNAPSHOT_SOURCE_AGENT,
			OsName: "Windows", CollectedAt: 1000,
		},
		Payload: &invv1.Inventory{
			Identity: &invv1.Identity{Hostname: "host-a", HardwareUuid: "hw-1"},
			Os:       &invv1.OSInfo{Name: "Windows", Version: "11"},
			Memory:   &invv1.MemoryInfo{TotalPhysicalBytes: 4096},
			Disks:    []*invv1.Disk{{Model: "SSD", SizeBytes: 512, Partitions: []*invv1.Partition{{Mount: "/", Fs: "ext4"}}}},
		},
	}, nil
}

func (s *stubSnapshots) DiffSnapshots(_ context.Context, _ *invv1.DiffSnapshotsRequest) (*invv1.DiffSnapshotsResponse, error) {
	return &invv1.DiffSnapshotsResponse{Changes: []*invv1.Change{
		{Id: "c-1", Category: "software", ChangeType: invv1.ChangeType_CHANGE_TYPE_MODIFIED, ComponentKey: "curl"},
	}}, nil
}

type stubStats struct {
	invv1.UnimplementedInventoryStatisticsServiceServer
}

func (s *stubStats) GetStatistics(_ context.Context, _ *invv1.GetStatisticsRequest) (*invv1.Stats, error) {
	return &invv1.Stats{HostsTotal: 3, StaleHosts: 1, HostsByStatus: map[string]int64{"active": 2, "stale": 1}, TotalMemoryBytes: 8192}, nil
}

type stubAgents struct {
	invv1.UnimplementedInventoryAgentServiceServer
	lastMint *invv1.MintEnrollmentTokenRequest
}

func (s *stubAgents) RefreshInventory(_ context.Context, _ *invv1.RefreshInventoryRequest) (*invv1.RefreshInventoryResponse, error) {
	return &invv1.RefreshInventoryResponse{Delivered: true, CommandId: "cmd-1"}, nil
}

func (s *stubAgents) MintEnrollmentToken(_ context.Context, req *invv1.MintEnrollmentTokenRequest) (*invv1.MintEnrollmentTokenResponse, error) {
	s.lastMint = req
	return &invv1.MintEnrollmentTokenResponse{Id: "tok-1", Token: "SECRET-ONCE", ExpiresAt: 2000, Label: req.GetLabel()}, nil
}

func dial(t *testing.T, hs *stubHosts, ss *stubSnapshots, sts *stubStats, as *stubAgents) *inventoryclient.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	invv1.RegisterInventoryHostServiceServer(gs, hs)
	invv1.RegisterInventorySnapshotServiceServer(gs, ss)
	invv1.RegisterInventoryStatisticsServiceServer(gs, sts)
	invv1.RegisterInventoryAgentServiceServer(gs, as)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return inventoryclient.New(conn)
}

func TestHostsAndStats(t *testing.T) {
	ctx := context.Background()
	hs := &stubHosts{}
	c := dial(t, hs, &stubSnapshots{}, &stubStats{}, &stubAgents{})

	list, err := c.ListHosts(ctx, "t1", inventoryclient.HostFilter{Status: "active", Tag: "env=prod"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v n=%d", err, len(list))
	}
	if list[0].Status != "active" || list[0].Tags["env"] != "prod" {
		t.Fatalf("host decode: %+v", list[0])
	}
	if hs.lastList.GetStatus() != invv1.HostStatus_HOST_STATUS_ACTIVE {
		t.Fatalf("status enum not encoded: %v", hs.lastList.GetStatus())
	}

	got, err := c.GetHost(ctx, "t1", "h-1")
	if err != nil || got.Status != "stale" {
		t.Fatalf("get: %v %+v", err, got)
	}

	st, err := c.GetStatistics(ctx, "t1")
	if err != nil || st.HostsTotal != 3 || st.HostsByStatus["stale"] != 1 {
		t.Fatalf("stats: %v %+v", err, st)
	}
}

func TestSnapshotAndDiff(t *testing.T) {
	ctx := context.Background()
	c := dial(t, &stubHosts{}, &stubSnapshots{}, &stubStats{}, &stubAgents{})

	snap, err := c.GetLatestSnapshot(ctx, "t1", "h-1")
	if err != nil || snap.Payload == nil {
		t.Fatalf("latest: %v", err)
	}
	if snap.Source != "agent" || snap.Payload.OS.Name != "Windows" || snap.Payload.Memory.TotalPhysicalBytes != 4096 {
		t.Fatalf("payload decode: %+v", snap)
	}
	if len(snap.Payload.Disks) != 1 || snap.Payload.Disks[0].Partitions[0].FS != "ext4" {
		t.Fatalf("disk decode: %+v", snap.Payload.Disks)
	}

	changes, err := c.DiffSnapshots(ctx, "t1", "a", "b")
	if err != nil || len(changes) != 1 || changes[0].ChangeType != "modified" {
		t.Fatalf("diff: %v %+v", err, changes)
	}
}

func TestAgentControl(t *testing.T) {
	ctx := context.Background()
	as := &stubAgents{}
	c := dial(t, &stubHosts{}, &stubSnapshots{}, &stubStats{}, as)

	delivered, cmdID, err := c.RefreshInventory(ctx, "t1", "h-1")
	if err != nil || !delivered || cmdID != "cmd-1" {
		t.Fatalf("refresh: %v delivered=%v id=%q", err, delivered, cmdID)
	}

	tok, err := c.MintEnrollmentToken(ctx, "t1", "fleet", time.Hour)
	if err != nil || tok.Token != "SECRET-ONCE" || tok.Label != "fleet" {
		t.Fatalf("mint: %v %+v", err, tok)
	}
	if as.lastMint.GetTtlSeconds() != 3600 {
		t.Fatalf("ttl not encoded: %d", as.lastMint.GetTtlSeconds())
	}
	if tok.ExpiresAt.IsZero() {
		t.Fatalf("expires_at not decoded")
	}
}

func TestErrorPropagation(t *testing.T) {
	ctx := context.Background()
	c := dial(t, &stubHosts{failGet: true}, &stubSnapshots{}, &stubStats{}, &stubAgents{})
	_, err := c.GetHost(ctx, "t1", "missing")
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}
