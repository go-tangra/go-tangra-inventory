//go:build linux

package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "certmaterial", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// realStore is the Linux store in a temp dir owned by the test user.
func realStore(t *testing.T, dir string) *agentcerts.Store {
	t.Helper()
	cfg := config.DefaultAgent().Certificates
	cfg.Directory, cfg.Owner, cfg.Group = dir, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	st, err := agentcerts.NewOS(agentcerts.ConfigFrom(cfg), agentcerts.Deps{RootUID: os.Getuid(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Recover(); err != nil {
		t.Fatal(err)
	}
	return st
}

func rsaBundle(t *testing.T) *invv1.CertificateBundle {
	return &invv1.CertificateBundle{Name: "www", CertificateId: "c1", CertPem: fixture(t, "rsa.crt"), ChainPem: fixture(t, "chain.pem"),
		KeyPem: fixture(t, "rsa.pkcs8.key"), HasKey: true}
}

// TestAgentRestartRefetches (T060): an item fetched before a crash (no
// report sent) is fetched again by the restarted agent and completes; a
// renewed bundle for the same name becomes a new generation with
// previous_serial.
func TestAgentRestartRefetches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	cl := &fakeCertClient{bundle: rsaBundle(t), reportErr: status.Error(codes.Unavailable, "ingest down")}

	first := New(config.DefaultAgent(), "4.7.0").WithCertificates(realStore(t, dir))
	first.goos = "linux"
	first.newCertClient = func(string, string) CertClient { return cl }
	first.handleCommand(context.Background(), certCmd("item-1"))
	drain(first)
	if _, err := os.Stat(filepath.Join(dir, "live/www/privkey.pem")); err != nil {
		t.Fatal("not installed before the crash")
	}

	// Restart: fresh process memory, same directory; the server replays.
	cl.mu.Lock()
	cl.reportErr = nil
	cl.mu.Unlock()
	second := New(config.DefaultAgent(), "4.7.0").WithCertificates(realStore(t, dir))
	second.goos = "linux"
	second.newCertClient = func(string, string) CertClient { return cl }
	second.handleCommand(context.Background(), certCmd("item-1"))
	drain(second)
	fetches, reps := cl.snapshot()
	if fetches != 2 || len(reps) != 1 || reps[0].GetState() != store.DeliveryUnchanged || reps[0].GetFingerprintSha256() == "" {
		t.Fatalf("fetches=%d reports=%v", fetches, reps)
	}

	// Renewal under the same name.
	cl.mu.Lock()
	cl.bundle = &invv1.CertificateBundle{Name: "www", CertificateId: "c2", CertPem: fixture(t, "ecdsa.crt"), ChainPem: fixture(t, "chain.pem"),
		KeyPem: fixture(t, "ecdsa.pkcs8.key"), HasKey: true}
	cl.mu.Unlock()
	second.handleCommand(context.Background(), certCmd("item-2"))
	drain(second)
	_, reps = cl.snapshot()
	if len(reps) != 2 || reps[1].GetState() != store.DeliveryInstalled {
		t.Fatalf("renewal report = %v", reps)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "renewal/www.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta agentcerts.Metadata
	if err := json.Unmarshal(raw, &meta); err != nil || meta.PreviousSerial != reps[0].GetSerial() || meta.RenewalCount != 1 ||
		meta.ItemID != "item-2" || meta.CertificateID != "c2" {
		t.Fatalf("metadata = %+v %v", meta, err)
	}
	gens, _ := os.ReadDir(filepath.Join(dir, "archive/www"))
	if len(gens) != 2 {
		t.Fatalf("generations = %d", len(gens))
	}
}

// streamIngest is an in-process ingest edge: each StreamCommands call sends
// the configured commands (a replay) and keeps the stream open until the
// test ends it.
type streamIngest struct {
	invv1.UnimplementedIngestServiceServer
	mu         sync.Mutex
	caps       [][]string
	cmds       []*invv1.Command
	bundle     *invv1.CertificateBundle
	fetches    int
	reports    []*invv1.ReportCertificateRequest
	streams    chan struct{}
	endCurrent chan struct{}
}

func (s *streamIngest) StreamCommands(req *invv1.StreamRequest, st grpc.ServerStreamingServer[invv1.Command]) error {
	s.mu.Lock()
	s.caps = append(s.caps, req.GetCapabilities())
	cmds := s.cmds
	end := s.endCurrent
	s.mu.Unlock()
	for _, c := range cmds {
		if err := st.Send(c); err != nil {
			return err
		}
	}
	s.streams <- struct{}{}
	select {
	case <-end:
		return status.Error(codes.Unavailable, "replica restart")
	case <-st.Context().Done():
		return nil
	}
}

func (s *streamIngest) FetchCertificate(_ context.Context, r *invv1.FetchCertificateRequest) (*invv1.CertificateBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	b := proto.Clone(s.bundle).(*invv1.CertificateBundle)
	b.ItemId = r.GetItemId()
	return b, nil
}

func (s *streamIngest) ReportCertificate(_ context.Context, r *invv1.ReportCertificateRequest) (*invv1.ReportCertificateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports = append(s.reports, r)
	return &invv1.ReportCertificateResponse{Accepted: true}, nil
}

// TestStreamReplayAcrossReconnect: the agent announces cert.v1, handles a
// CERTIFICATE command pushed twice on one stream once, and ignores the
// replay of the reported item after a reconnect.
func TestStreamReplayAcrossReconnect(t *testing.T) {
	srv := &streamIngest{bundle: rsaBundle(t), streams: make(chan struct{}, 4), endCurrent: make(chan struct{}),
		cmds: []*invv1.Command{certCmd("item-1"), certCmd("item-1")}}
	gs := grpc.NewServer()
	invv1.RegisterIngestServiceServer(gs, srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	cfg := config.DefaultAgent()
	cfg.IngestEndpoint, cfg.Insecure, cfg.Certificates.AllowInsecureTransport = lis.Addr().String(), true, true
	dir := filepath.Join(t.TempDir(), "certs")
	d := New(cfg, "4.7.0").WithCertificates(realStore(t, dir))
	d.goos, d.agentID, d.credential = "linux", "a1", "cred"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.certLoop(ctx)

	waitReports := func(n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			srv.mu.Lock()
			got := len(srv.reports)
			srv.mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("reports < %d", n)
	}

	errc := make(chan error, 1)
	go func() { errc <- d.streamLoop(ctx) }()
	<-srv.streams
	waitReports(1)
	close(srv.endCurrent) // replica restart: the stream breaks
	if err := <-errc; err == nil {
		t.Fatal("stream end not reported")
	}
	srv.mu.Lock()
	srv.endCurrent = make(chan struct{})
	srv.mu.Unlock()
	go func() { errc <- d.streamLoop(ctx) }()
	<-srv.streams
	time.Sleep(100 * time.Millisecond)

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.fetches != 1 || len(srv.reports) != 1 || srv.reports[0].GetState() != store.DeliveryInstalled {
		t.Fatalf("fetches=%d reports=%v", srv.fetches, srv.reports)
	}
	for _, caps := range srv.caps {
		found := false
		for _, c := range caps {
			found = found || c == store.CapCertV1
		}
		if !found {
			t.Fatalf("cert.v1 not announced: %v", caps)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "live/www/fullchain.pem")); err != nil {
		t.Fatal(err)
	}
}
