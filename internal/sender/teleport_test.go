package sender_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
)

type teleportIngest struct {
	invv1.UnimplementedIngestServiceServer
	mu       sync.Mutex
	reports  []*invv1.ReportTeleportJoinRequest
	failOnce bool
	agents   []string
}

func (f *teleportIngest) FetchTeleportJoin(ctx context.Context, r *invv1.FetchTeleportJoinRequest) (*invv1.TeleportJoinMaterial, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.mu.Lock()
	f.agents = append(f.agents, md.Get("x-agent-id")...)
	f.mu.Unlock()
	if r.GetItemId() != "ok" {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return &invv1.TeleportJoinMaterial{ItemId: "ok", Token: "tok", TeleportVersion: "17.0.0"}, nil
}

func (f *teleportIngest) ReportTeleportJoin(_ context.Context, r *invv1.ReportTeleportJoinRequest) (*invv1.ReportTeleportJoinResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnce {
		f.failOnce = false
		return nil, status.Error(codes.Unavailable, "try again")
	}
	f.reports = append(f.reports, r)
	return &invv1.ReportTeleportJoinResponse{Accepted: r.GetState() != "failed"}, nil
}

func TestTeleportClient(t *testing.T) {
	fake := &teleportIngest{failOnce: true}
	gs := grpc.NewServer()
	invv1.RegisterIngestServiceServer(gs, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	c := sender.New(lis.Addr().String(), sender.Options{Insecure: true}).TeleportClient("a1", "cred1")
	ctx := context.Background()
	m, err := c.Fetch(ctx, "ok")
	if err != nil || m.GetToken() != "tok" || m.GetTeleportVersion() != "17.0.0" {
		t.Fatalf("fetch: %v %v", m, err)
	}
	if _, err := c.Fetch(ctx, "missing"); status.Code(err) != codes.NotFound {
		t.Fatalf("missing: %v", err)
	}
	ok, err := c.Report(ctx, &invv1.ReportTeleportJoinRequest{ItemId: "ok", State: "installing"})
	if err != nil || !ok || len(fake.reports) != 1 {
		t.Fatalf("report: %v %v %d", ok, err, len(fake.reports))
	}
	if ok, _ := c.Report(ctx, &invv1.ReportTeleportJoinRequest{ItemId: "ok", State: "failed"}); ok {
		t.Fatal("accepted flag")
	}
	bad := sender.New("127.0.0.1:1", sender.Options{CAFile: "/nonexistent/ca.pem"}).TeleportClient("a", "c")
	if _, err := bad.Fetch(ctx, "x"); err == nil {
		t.Fatal("dial error")
	}
	if _, err := bad.Report(ctx, &invv1.ReportTeleportJoinRequest{}); err == nil {
		t.Fatal("dial error")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Report(cctx, &invv1.ReportTeleportJoinRequest{}); err == nil {
		t.Fatal("canceled report")
	}
}
