package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentteleport"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type fakeJoiner struct {
	mu        sync.Mutex
	preflight string
	res       agentteleport.Result
	joins     []agentteleport.Material
	installed []bool
}

func (f *fakeJoiner) Preflight(context.Context) string { return f.preflight }

func (f *fakeJoiner) Join(_ context.Context, m agentteleport.Material, installing func() bool) agentteleport.Result {
	ok := installing()
	f.mu.Lock()
	f.joins = append(f.joins, m)
	f.installed = append(f.installed, ok)
	f.mu.Unlock()
	m.Wipe()
	if !ok {
		return agentteleport.Result{Aborted: true}
	}
	return f.res
}

type fakeTeleportClient struct {
	mu          sync.Mutex
	fetchErrs   []error
	material    *invv1.TeleportJoinMaterial
	fetches     int
	reports     []*invv1.ReportTeleportJoinRequest
	reportErr   map[string]error // by state
	notAccepted map[string]bool
}

func (f *fakeTeleportClient) Fetch(_ context.Context, id string) (*invv1.TeleportJoinMaterial, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if len(f.fetchErrs) > 0 {
		err := f.fetchErrs[0]
		f.fetchErrs = f.fetchErrs[1:]
		return nil, err
	}
	if f.material != nil {
		return f.material, nil
	}
	return &invv1.TeleportJoinMaterial{ItemId: id, Token: "tok", ProxyAddr: "tp.example.com:443", TeleportVersion: "17.0.0"}, nil
}

func (f *fakeTeleportClient) Report(_ context.Context, r *invv1.ReportTeleportJoinRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reportErr[r.GetState()]; err != nil {
		return false, err
	}
	f.reports = append(f.reports, r)
	return !f.notAccepted[r.GetState()], nil
}

func (f *fakeTeleportClient) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reports {
		out = append(out, r.GetState()+"/"+r.GetReason())
	}
	return out
}

func teleportDaemon(mut func(*config.AgentConfig)) (*Daemon, *fakeJoiner, *fakeTeleportClient) {
	cfg := config.DefaultAgent()
	if mut != nil {
		mut(&cfg)
	}
	j := &fakeJoiner{res: agentteleport.Result{State: "joined", NodeHostUUID: "0b6b5f4e-9a3c-4d1e-8f2a-1c2d3e4f5a6b"}}
	cl := &fakeTeleportClient{reportErr: map[string]error{}, notAccepted: map[string]bool{}}
	d := New(cfg, "4.9.0").WithTeleport(j)
	d.goos = "linux"
	d.agentID, d.credential = "a1", "cred"
	d.newTeleportClient = func(string, string) TeleportClient { return cl }
	d.tps.backoff = func(int) time.Duration { return time.Millisecond }
	return d, j, cl
}

func teleportCmd(id string, attempt uint32) *invv1.Command {
	return &invv1.Command{CommandId: "cmd-" + id, Type: invv1.CommandType_COMMAND_TYPE_TELEPORT_JOIN,
		TeleportJoin: &invv1.TeleportJoinCommand{ItemId: id, Attempt: attempt}}
}

func drainTeleport(d *Daemon) {
	for {
		select {
		case j := <-d.tps.queue:
			d.processTeleport(context.Background(), j)
		default:
			return
		}
	}
}

func TestTeleportCapabilityAnnounced(t *testing.T) {
	d, _, _ := teleportDaemon(func(c *config.AgentConfig) { c.Teleport.Enabled = false })
	if !slices.Contains(d.streamRequest().GetCapabilities(), store.CapTeleportV1) {
		t.Fatal("disabled linux agent must announce teleport.v1")
	}
	d.goos = "windows"
	if slices.Contains(d.streamRequest().GetCapabilities(), store.CapTeleportV1) {
		t.Fatal("windows announces teleport.v1")
	}
}

func TestTeleportJoinFlow(t *testing.T) {
	d, j, cl := teleportDaemon(nil)
	ctx := context.Background()
	d.handleCommand(ctx, teleportCmd("i1", 1))
	d.enqueueTeleport(ctx, teleportCmd("i1", 1)) // duplicate while queued: ignored
	drainTeleport(d)
	if got := cl.states(); strings.Join(got, ",") != "installing/,joined/" {
		t.Fatalf("reports = %v", got)
	}
	last := cl.reports[1]
	if last.GetNodeHostUuid() != j.res.NodeHostUUID || last.GetItemId() != "i1" {
		t.Fatalf("joined = %v", last)
	}
	if len(j.joins) != 1 || strings.Trim(string(j.joins[0].Token), "\x00") != "" || j.joins[0].ProxyAddr != "tp.example.com:443" {
		t.Fatalf("joins = %+v", j.joins)
	}
	d.enqueueTeleport(ctx, teleportCmd("i1", 1)) // finished attempt: ignored
	d.enqueueTeleport(ctx, &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_TELEPORT_JOIN})
	drainTeleport(d)
	if cl.fetches != 1 {
		t.Fatalf("fetches = %d", cl.fetches)
	}
}

func TestTeleportBusy(t *testing.T) {
	d, _, cl := teleportDaemon(nil)
	ctx := context.Background()
	d.enqueueTeleport(ctx, teleportCmd("i1", 1))
	d.enqueueTeleport(ctx, teleportCmd("i2", 1))
	deadline := time.Now().Add(2 * time.Second)
	for len(cl.states()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := cl.states(); len(got) != 1 || got[0] != "failed/busy" || cl.reports[0].GetItemId() != "i2" {
		t.Fatalf("busy = %v", got)
	}
	drainTeleport(d)
	if d.tpBusy.Load() {
		t.Fatal("still busy")
	}
}

func TestTeleportRefusals(t *testing.T) {
	cases := map[string]struct {
		mut    func(d *Daemon, j *fakeJoiner, cl *fakeTeleportClient)
		want   string
		fetchs int
	}{
		"disabled":  {func(d *Daemon, _ *fakeJoiner, _ *fakeTeleportClient) { d.cfg.Teleport.Enabled = false }, "failed/disabled_locally", 0},
		"plaintext": {func(d *Daemon, _ *fakeJoiner, _ *fakeTeleportClient) { d.cfg.Insecure = true }, "failed/disabled_locally", 0},
		"no installer": {func(d *Daemon, _ *fakeJoiner, _ *fakeTeleportClient) { d.teleport = nil },
			"failed/unsupported_platform", 0},
		"windows": {func(d *Daemon, _ *fakeJoiner, _ *fakeTeleportClient) { d.goos = "windows" }, "failed/unsupported_platform", 0},
		"installed": {func(_ *Daemon, j *fakeJoiner, _ *fakeTeleportClient) { j.preflight = "already_installed" },
			"failed/already_installed", 0},
		"fetch failed": {func(_ *Daemon, _ *fakeJoiner, cl *fakeTeleportClient) {
			cl.fetchErrs = []error{status.Error(codes.PermissionDenied, "plaintext")}
		}, "failed/fetch_failed", 1},
		"wrong item": {func(_ *Daemon, _ *fakeJoiner, cl *fakeTeleportClient) {
			cl.material = &invv1.TeleportJoinMaterial{ItemId: "other", Token: "tok"}
		}, "failed/invalid_material", 1},
		"retried": {func(_ *Daemon, _ *fakeJoiner, cl *fakeTeleportClient) {
			cl.fetchErrs = []error{status.Error(codes.Unavailable, "x"), status.Error(codes.ResourceExhausted, "x")}
		}, "installing/,joined/", 3},
	}
	for name, c := range cases {
		d, j, cl := teleportDaemon(nil)
		c.mut(d, j, cl)
		d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
		drainTeleport(d)
		if got := strings.Join(cl.states(), ","); got != c.want || cl.fetches != c.fetchs {
			t.Errorf("%s: reports %s fetches %d", name, got, cl.fetches)
		}
	}
}

func TestTeleportFetchEnd(t *testing.T) {
	// NotFound: not ours; nothing reported, the attempt is finished.
	d, _, cl := teleportDaemon(nil)
	cl.fetchErrs = []error{status.Error(codes.NotFound, "x")}
	d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
	drainTeleport(d)
	d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
	if len(cl.states()) != 0 || len(d.tps.queue) != 0 {
		t.Fatalf("not found: %v", cl.states())
	}
	// The stream ended during transient errors: released for the replay.
	d, _, cl = teleportDaemon(nil)
	cl.fetchErrs = []error{status.Error(codes.Unavailable, "x")}
	sctx, cancel := context.WithCancel(context.Background())
	d.enqueueTeleport(sctx, teleportCmd("i1", 1))
	cancel()
	drainTeleport(d)
	if len(cl.states()) != 0 || !d.tps.claim(certKey("i1", 1)) {
		t.Fatalf("released: %v", cl.states())
	}
	// Cancelled while waiting for the backoff.
	d, _, cl = teleportDaemon(nil)
	d.tps.backoff = func(int) time.Duration { return time.Hour }
	cl.fetchErrs = []error{status.Error(codes.Unavailable, "x")}
	sctx, cancel = context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, err := d.fetchTeleport(sctx, "i1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff cancel: %v", err)
	}
}

func TestTeleportReports(t *testing.T) {
	// The installing report is not accepted (cancelled meanwhile): abort.
	d, j, cl := teleportDaemon(nil)
	cl.notAccepted["installing"] = true
	d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
	drainTeleport(d)
	if got := cl.states(); strings.Join(got, ",") != "installing/" || j.installed[0] {
		t.Fatalf("aborted: %v", got)
	}
	// The installing report fails: the join goes on.
	d, _, cl = teleportDaemon(nil)
	cl.reportErr["installing"] = errors.New("down")
	d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
	drainTeleport(d)
	if got := cl.states(); strings.Join(got, ",") != "joined/" {
		t.Fatalf("installing failed: %v", got)
	}
	// The outcome report fails: kept and re-sent on the next command
	// without joining again; a failed outcome not accepted is logged.
	d, j, cl = teleportDaemon(nil)
	j.res = agentteleport.Result{State: "failed", Reason: "join_failed", Detail: strings.Repeat("d", 300)}
	cl.reportErr["failed"] = errors.New("down")
	d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
	drainTeleport(d)
	cl.mu.Lock()
	delete(cl.reportErr, "failed")
	cl.notAccepted["failed"] = true
	cl.mu.Unlock()
	d.enqueueTeleport(context.Background(), teleportCmd("i1", 1))
	drainTeleport(d)
	if got := cl.states(); strings.Join(got, ",") != "installing/,failed/join_failed" || len(j.joins) != 1 {
		t.Fatalf("resent: %v joins %d", got, len(j.joins))
	}
	if len(cl.reports[1].GetDetail()) != maxReportDetail {
		t.Fatalf("detail len %d", len(cl.reports[1].GetDetail()))
	}
}

func TestTeleportLoop(t *testing.T) {
	d, _, cl := teleportDaemon(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.teleportLoop(ctx); close(done) }()
	d.enqueueTeleport(ctx, teleportCmd("i1", 1))
	deadline := time.Now().Add(2 * time.Second)
	for len(cl.states()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if len(cl.states()) != 2 {
		t.Fatalf("loop: %v", cl.states())
	}
}
