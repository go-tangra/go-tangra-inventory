package sender_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// moduleIngest streams scripted bundles per item id.
type moduleIngest struct {
	invv1.UnimplementedIngestServiceServer
	mu       sync.Mutex
	streams  map[string][]*invv1.ModuleBundleChunk
	reports  []*invv1.ReportModuleRequest
	failOnce bool
}

func hdr(size int64) *invv1.ModuleBundleChunk {
	return &invv1.ModuleBundleChunk{Part: &invv1.ModuleBundleChunk_Header{Header: &invv1.ModuleBundleHeader{ItemId: "i", Module: "sms-gw", Size: size}}}
}

func chk(off int64, data string) *invv1.ModuleBundleChunk {
	return &invv1.ModuleBundleChunk{Part: &invv1.ModuleBundleChunk_Chunk{Chunk: &invv1.ArtifactChunk{Offset: off, Data: []byte(data)}}}
}

func (m *moduleIngest) FetchModuleBundle(r *invv1.FetchModuleBundleRequest, st grpc.ServerStreamingServer[invv1.ModuleBundleChunk]) error {
	m.mu.Lock()
	msgs, ok := m.streams[r.GetItemId()]
	m.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "not found")
	}
	for _, c := range msgs {
		if err := st.Send(c); err != nil {
			return err
		}
	}
	if r.GetItemId() == "broken" {
		return status.Error(codes.Unavailable, "reset")
	}
	return nil
}

func (m *moduleIngest) ReportModule(_ context.Context, r *invv1.ReportModuleRequest) (*invv1.ReportModuleResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOnce {
		m.failOnce = false
		return nil, status.Error(codes.Unavailable, "try again")
	}
	m.reports = append(m.reports, r)
	return &invv1.ReportModuleResponse{Accepted: r.GetState() != "failed"}, nil
}

func TestModuleClient(t *testing.T) {
	fake := &moduleIngest{failOnce: true, streams: map[string][]*invv1.ModuleBundleChunk{
		"ok":        {hdr(6), chk(0, "abc"), chk(3, "def")},
		"short":     {hdr(6), chk(0, "abc")}, // returned as is: the writer checks size and digest
		"no header": {chk(0, "abc")},
		"negative":  {hdr(-1)},
		"too large": {hdr(store.MaxModuleBundleBytes + 1)},
		"gap":       {hdr(6), chk(0, "abc"), chk(4, "ef")},
		"overflow":  {hdr(4), chk(0, "abc"), chk(3, "def")},
		"twice":     {hdr(6), hdr(6)},
		"broken":    {hdr(6), chk(0, "abc")},
		"empty":     {},
	}}
	gs := grpc.NewServer()
	invv1.RegisterIngestServiceServer(gs, fake)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	c := sender.New(lis.Addr().String(), sender.Options{Insecure: true}).ModuleClient("a1", "cred1")
	ctx := context.Background()
	h, data, err := c.Fetch(ctx, "ok")
	if err != nil || h.GetModule() != "sms-gw" || !bytes.Equal(data, []byte("abcdef")) {
		t.Fatalf("fetch: %v %q %v", h, data, err)
	}
	if _, data, err := c.Fetch(ctx, "short"); err != nil || string(data) != "abc" {
		t.Fatalf("short: %q %v", data, err)
	}
	for id, want := range map[string]error{"no header": sender.ErrBundleMalformed, "negative": sender.ErrBundleMalformed,
		"too large": sender.ErrBundleTooLarge, "gap": sender.ErrBundleMalformed, "overflow": sender.ErrBundleMalformed,
		"twice": sender.ErrBundleMalformed} {
		if _, _, err := c.Fetch(ctx, id); !errors.Is(err, want) {
			t.Errorf("%s: %v", id, err)
		}
	}
	for id, want := range map[string]codes.Code{"broken": codes.Unavailable, "missing": codes.NotFound} {
		if _, _, err := c.Fetch(ctx, id); status.Code(err) != want {
			t.Errorf("%s: %v", id, err)
		}
	}
	if _, _, err := c.Fetch(ctx, "empty"); err == nil {
		t.Fatal("stream without a header accepted")
	}
	ok, err := c.Report(ctx, &invv1.ReportModuleRequest{ItemId: "ok", State: "installed", HookExitCode: -1})
	if err != nil || !ok {
		t.Fatalf("report: %v %v", ok, err)
	}
	if ok, _ := c.Report(ctx, &invv1.ReportModuleRequest{ItemId: "ok", State: "failed"}); ok {
		t.Fatal("accepted flag")
	}
	bad := sender.New("127.0.0.1:1", sender.Options{CAFile: "/nonexistent/ca.pem"}).ModuleClient("a", "c")
	if _, _, err := bad.Fetch(ctx, "x"); err == nil {
		t.Fatal("dial error")
	}
	if _, err := bad.Report(ctx, &invv1.ReportModuleRequest{}); err == nil {
		t.Fatal("dial error")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Report(cctx, &invv1.ReportModuleRequest{}); err == nil {
		t.Fatal("canceled report")
	}
}
