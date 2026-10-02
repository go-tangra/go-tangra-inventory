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

type certIngest struct {
	invv1.UnimplementedIngestServiceServer
	mu       sync.Mutex
	creds    []string
	fetched  []string
	reports  []*invv1.ReportCertificateRequest
	failOnce bool
}

func (c *certIngest) cred(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	c.creds = append(c.creds, md.Get(sender.MetaAgentIDKey)[0]+"/"+md.Get(sender.MetaCredentialKey)[0])
}

func (c *certIngest) FetchCertificate(ctx context.Context, r *invv1.FetchCertificateRequest) (*invv1.CertificateBundle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cred(ctx)
	c.fetched = append(c.fetched, r.GetItemId())
	if r.GetItemId() == "gone" {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return &invv1.CertificateBundle{ItemId: r.GetItemId(), Name: "www"}, nil
}

func (c *certIngest) ReportCertificate(ctx context.Context, r *invv1.ReportCertificateRequest) (*invv1.ReportCertificateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cred(ctx)
	if c.failOnce {
		c.failOnce = false
		return nil, status.Error(codes.Unavailable, "try again")
	}
	c.reports = append(c.reports, r)
	return &invv1.ReportCertificateResponse{Accepted: r.GetState() != "unchanged"}, nil
}

func TestCertClient(t *testing.T) {
	fake := &certIngest{failOnce: true}
	gs := grpc.NewServer()
	invv1.RegisterIngestServiceServer(gs, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	c := sender.New(lis.Addr().String(), sender.Options{Insecure: true}).CertClient("a1", "cred1")
	ctx := context.Background()
	b, err := c.Fetch(ctx, "item-1")
	if err != nil || b.GetItemId() != "item-1" {
		t.Fatalf("fetch: %v %v", b, err)
	}
	if _, err := c.Fetch(ctx, "gone"); status.Code(err) != codes.NotFound {
		t.Fatalf("not found: %v", err)
	}
	ok, err := c.Report(ctx, &invv1.ReportCertificateRequest{ItemId: "item-1", State: "installed", HookExitCode: -1})
	if err != nil || !ok {
		t.Fatalf("report: %v %v", ok, err)
	}
	if ok, _ := c.Report(ctx, &invv1.ReportCertificateRequest{ItemId: "item-1", State: "unchanged"}); ok {
		t.Fatal("accepted flag")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.reports) != 2 || fake.creds[0] != "a1/cred1" {
		t.Fatalf("reports=%d creds=%v", len(fake.reports), fake.creds)
	}

	bad := sender.New("127.0.0.1:1", sender.Options{CAFile: "/nonexistent/ca.pem"}).CertClient("a", "c")
	if _, err := bad.Fetch(ctx, "x"); err == nil {
		t.Fatal("dial error")
	}
	if _, err := bad.Report(ctx, &invv1.ReportCertificateRequest{}); err == nil {
		t.Fatal("dial error")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sender.New(lis.Addr().String(), sender.Options{Insecure: true}).CertClient("a", "c").Report(cctx,
		&invv1.ReportCertificateRequest{}); err == nil {
		t.Fatal("canceled report")
	}
}
