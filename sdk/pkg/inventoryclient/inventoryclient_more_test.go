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

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/inventoryclient"
)

// dialServers wires arbitrary service implementations over bufconn.
func dialServers(t *testing.T, hs invv1.InventoryHostServiceServer, ss invv1.InventorySnapshotServiceServer,
	sts invv1.InventoryStatisticsServiceServer, as invv1.InventoryAgentServiceServer) *inventoryclient.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	if hs != nil {
		invv1.RegisterInventoryHostServiceServer(gs, hs)
	}
	if ss != nil {
		invv1.RegisterInventorySnapshotServiceServer(gs, ss)
	}
	if sts != nil {
		invv1.RegisterInventoryStatisticsServiceServer(gs, sts)
	}
	if as != nil {
		invv1.RegisterInventoryAgentServiceServer(gs, as)
	}
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

// ---- enum mapping coverage via public methods

type enumHosts struct {
	invv1.UnimplementedInventoryHostServiceServer
	status invv1.HostStatus
}

func (s *enumHosts) ListHosts(_ context.Context, req *invv1.ListHostsRequest) (*invv1.ListHostsResponse, error) {
	// Echo the requested status enum back so the caller can verify encode+decode.
	return &invv1.ListHostsResponse{Hosts: []*invv1.Host{{Id: "h", Status: req.GetStatus()}}}, nil
}

func (s *enumHosts) GetHost(_ context.Context, _ *invv1.GetHostRequest) (*invv1.Host, error) {
	return &invv1.Host{Id: "h", Status: s.status}, nil
}

func TestHostStatusEnumRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, st := range []string{"active", "stale", "retired", "unknown"} {
		c := dialServers(t, &enumHosts{}, nil, nil, nil)
		list, err := c.ListHosts(ctx, "t1", inventoryclient.HostFilter{Status: st})
		if err != nil || len(list) != 1 {
			t.Fatalf("list %q: %v", st, err)
		}
		want := st
		if st == "unknown" {
			want = "" // unknown status encodes to UNSPECIFIED, decodes back to ""
		}
		if list[0].Status != want {
			t.Fatalf("status %q round trip -> %q", st, list[0].Status)
		}
	}
	// GetHost decodes each enum value directly, including UNSPECIFIED -> "".
	for enum, want := range map[invv1.HostStatus]string{
		invv1.HostStatus_HOST_STATUS_ACTIVE:      "active",
		invv1.HostStatus_HOST_STATUS_STALE:       "stale",
		invv1.HostStatus_HOST_STATUS_RETIRED:     "retired",
		invv1.HostStatus_HOST_STATUS_UNSPECIFIED: "",
	} {
		c := dialServers(t, &enumHosts{status: enum}, nil, nil, nil)
		got, err := c.GetHost(ctx, "t1", "h")
		if err != nil || got.Status != want {
			t.Fatalf("GetHost enum %v -> %q (want %q): %v", enum, got.Status, want, err)
		}
	}
}

type enumSnaps struct {
	invv1.UnimplementedInventorySnapshotServiceServer
	source     invv1.SnapshotSource
	changeType invv1.ChangeType
}

func (s *enumSnaps) GetLatestByHost(_ context.Context, _ *invv1.GetLatestByHostRequest) (*invv1.Snapshot, error) {
	return &invv1.Snapshot{Summary: &invv1.SnapshotSummary{Id: "s", Source: s.source}}, nil
}

func (s *enumSnaps) DiffSnapshots(_ context.Context, _ *invv1.DiffSnapshotsRequest) (*invv1.DiffSnapshotsResponse, error) {
	return &invv1.DiffSnapshotsResponse{Changes: []*invv1.Change{{Id: "c", ChangeType: s.changeType}}}, nil
}

func TestSnapshotSourceAndChangeTypeEnums(t *testing.T) {
	ctx := context.Background()
	for enum, want := range map[invv1.SnapshotSource]string{
		invv1.SnapshotSource_SNAPSHOT_SOURCE_AGENT:       "agent",
		invv1.SnapshotSource_SNAPSHOT_SOURCE_MANUAL:      "manual",
		invv1.SnapshotSource_SNAPSHOT_SOURCE_IMPORT:      "import",
		invv1.SnapshotSource_SNAPSHOT_SOURCE_UNSPECIFIED: "",
	} {
		c := dialServers(t, nil, &enumSnaps{source: enum}, nil, nil)
		snap, err := c.GetLatestSnapshot(ctx, "t1", "h")
		if err != nil || snap.Source != want {
			t.Fatalf("source enum %v -> %q (want %q): %v", enum, snap.Source, want, err)
		}
	}
	for enum, want := range map[invv1.ChangeType]string{
		invv1.ChangeType_CHANGE_TYPE_ADDED:       "added",
		invv1.ChangeType_CHANGE_TYPE_REMOVED:     "removed",
		invv1.ChangeType_CHANGE_TYPE_MODIFIED:    "modified",
		invv1.ChangeType_CHANGE_TYPE_UNSPECIFIED: "",
	} {
		c := dialServers(t, nil, &enumSnaps{changeType: enum}, nil, nil)
		changes, err := c.DiffSnapshots(ctx, "t1", "a", "b")
		if err != nil || len(changes) != 1 || changes[0].ChangeType != want {
			t.Fatalf("change type enum %v -> %q (want %q): %v", enum, changes, want, err)
		}
	}
}

// ---- error propagation on every method (codes.PermissionDenied)

type failAll struct {
	invv1.UnimplementedInventoryHostServiceServer
	invv1.UnimplementedInventorySnapshotServiceServer
	invv1.UnimplementedInventoryStatisticsServiceServer
	invv1.UnimplementedInventoryAgentServiceServer
}

var errDenied = status.Error(codes.PermissionDenied, "forbidden")

func (failAll) ListHosts(context.Context, *invv1.ListHostsRequest) (*invv1.ListHostsResponse, error) {
	return nil, errDenied
}
func (failAll) GetLatestByHost(context.Context, *invv1.GetLatestByHostRequest) (*invv1.Snapshot, error) {
	return nil, errDenied
}
func (failAll) DiffSnapshots(context.Context, *invv1.DiffSnapshotsRequest) (*invv1.DiffSnapshotsResponse, error) {
	return nil, errDenied
}
func (failAll) GetStatistics(context.Context, *invv1.GetStatisticsRequest) (*invv1.Stats, error) {
	return nil, errDenied
}
func (failAll) RefreshInventory(context.Context, *invv1.RefreshInventoryRequest) (*invv1.RefreshInventoryResponse, error) {
	return nil, errDenied
}
func (failAll) MintEnrollmentToken(context.Context, *invv1.MintEnrollmentTokenRequest) (*invv1.MintEnrollmentTokenResponse, error) {
	return nil, errDenied
}

func TestErrorPropagationAllMethods(t *testing.T) {
	ctx := context.Background()
	f := &failAll{}
	c := dialServers(t, f, f, f, f)

	assertDenied := func(name string, err error) {
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: want PermissionDenied, got %v", name, err)
		}
	}
	_, err := c.ListHosts(ctx, "t1", inventoryclient.HostFilter{})
	assertDenied("ListHosts", err)
	_, err = c.GetLatestSnapshot(ctx, "t1", "h")
	assertDenied("GetLatestSnapshot", err)
	_, err = c.DiffSnapshots(ctx, "t1", "a", "b")
	assertDenied("DiffSnapshots", err)
	_, err = c.GetStatistics(ctx, "t1")
	assertDenied("GetStatistics", err)
	_, _, err = c.RefreshInventory(ctx, "t1", "h")
	assertDenied("RefreshInventory", err)
	_, err = c.MintEnrollmentToken(ctx, "t1", "l", time.Hour)
	assertDenied("MintEnrollmentToken", err)
}
