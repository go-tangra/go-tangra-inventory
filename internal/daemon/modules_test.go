package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentmodules"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type fakeModuleWriter struct {
	mu   sync.Mutex
	reqs []agentmodules.Request
	data [][]byte // the slices handed in (to check they are zeroed)
	res  agentmodules.Result
}

func (f *fakeModuleWriter) Install(_ context.Context, r agentmodules.Request) agentmodules.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	f.data = append(f.data, r.Data)
	return f.res
}

func (f *fakeModuleWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

type fakeModuleClient struct {
	mu        sync.Mutex
	fetchErrs []error
	header    *invv1.ModuleBundleHeader
	fetches   int
	reports   []*invv1.ReportModuleRequest
	reportErr error
	notAccept bool
}

func (f *fakeModuleClient) Fetch(_ context.Context, id string) (*invv1.ModuleBundleHeader, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if len(f.fetchErrs) > 0 {
		err := f.fetchErrs[0]
		f.fetchErrs = f.fetchErrs[1:]
		return nil, nil, err
	}
	h := &invv1.ModuleBundleHeader{ItemId: id, Module: "sms-gw", Version: "1.2.0", Size: 6, Sha256: "ab"}
	if f.header != nil {
		h = f.header
	}
	return h, []byte("secret"), nil
}

func (f *fakeModuleClient) Report(_ context.Context, r *invv1.ReportModuleRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reportErr != nil {
		return false, f.reportErr
	}
	f.reports = append(f.reports, r)
	return !f.notAccept, nil
}

func (f *fakeModuleClient) snapshot() (int, []*invv1.ReportModuleRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches, append([]*invv1.ReportModuleRequest(nil), f.reports...)
}

func moduleDaemon(mut func(*config.AgentConfig)) (*Daemon, *fakeModuleWriter, *fakeModuleClient) {
	cfg := config.DefaultAgent()
	if mut != nil {
		mut(&cfg)
	}
	w := &fakeModuleWriter{res: agentmodules.Result{State: store.DeliveryInstalled, HookExitCode: 0}}
	cl := &fakeModuleClient{}
	d := New(cfg, "4.8.0").WithModules(w)
	d.goos = "linux"
	d.agentID, d.credential = "a1", "cred"
	d.newModuleClient = func(string, string) ModuleClient { return cl }
	d.mods.backoff = func(int) time.Duration { return time.Millisecond }
	return d, w, cl
}

func moduleCmd(id string, attempt uint32) *invv1.Command {
	return &invv1.Command{CommandId: "cmd-" + id, Type: invv1.CommandType_COMMAND_TYPE_MODULE,
		Module: &invv1.ModuleCommand{ItemId: id, Module: "sms-gw", Attempt: attempt}}
}

func drainModules(d *Daemon) {
	for {
		select {
		case j := <-d.mods.queue:
			d.processModule(context.Background(), j)
		default:
			return
		}
	}
}

// TestModuleCapabilityAnnounced (T014): module.v1 only with a writer, when
// enabled, on Linux and over TLS unless the opt-out is set.
func TestModuleCapabilityAnnounced(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*config.AgentConfig)
		goos   string
		writer bool
		want   bool
	}{
		{"enabled linux tls", nil, "linux", true, true},
		{"disabled locally", func(c *config.AgentConfig) { c.Modules.Enabled = false }, "linux", true, false},
		{"windows", nil, "windows", true, false},
		{"no writer", nil, "linux", false, false},
		{"plaintext", func(c *config.AgentConfig) { c.Insecure = true }, "linux", true, false},
		{"plaintext opt-out", func(c *config.AgentConfig) { c.Insecure, c.Modules.AllowInsecureTransport = true, true }, "linux", true, true},
	}
	for _, c := range cases {
		d, _, _ := moduleDaemon(c.mut)
		d.goos = c.goos
		if !c.writer {
			d.moduleWriter = nil
		}
		if got := hasCap(d.streamRequest(), store.CapModuleV1); got != c.want {
			t.Errorf("%s: announced %v", c.name, got)
		}
	}
}

// TestModuleFetchWriteReport (T015): fetch by item id, the header's module,
// version, size and digest go to the writer, the bytes are zeroed, the
// result is reported; a duplicate command of the same attempt is ignored,
// a new attempt runs again.
func TestModuleFetchWriteReport(t *testing.T) {
	d, w, cl := moduleDaemon(nil)
	d.handleCommand(context.Background(), moduleCmd("item-1", 1))
	d.enqueueModule(context.Background(), moduleCmd("item-1", 1))
	d.enqueueModule(context.Background(), &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_MODULE})
	if len(d.mods.queue) != 1 {
		t.Fatalf("queued = %d", len(d.mods.queue))
	}
	drainModules(d)
	f, reps := cl.snapshot()
	if f != 1 || w.count() != 1 || len(reps) != 1 || reps[0].GetState() != "installed" || reps[0].GetHookExitCode() != 0 {
		t.Fatalf("fetches=%d writes=%d reports=%v", f, w.count(), reps)
	}
	r := w.reqs[0]
	if r.ItemID != "item-1" || r.Module != "sms-gw" || r.Version != "1.2.0" || r.Size != 6 || r.SHA256 != "ab" {
		t.Fatalf("request = %+v", r)
	}
	if strings.Trim(string(w.data[0]), "\x00") != "" {
		t.Fatal("bundle bytes not zeroed")
	}
	d.enqueueModule(context.Background(), moduleCmd("item-1", 1))
	d.enqueueModule(context.Background(), moduleCmd("item-1", 2))
	drainModules(d)
	if w.count() != 2 {
		t.Fatalf("writes = %d", w.count())
	}
}

// TestModuleFetchOutcomes (T015): transient errors are retried; NotFound
// drops the item silently; a malformed or oversized stream, a header for
// another item or module, or a permanent refusal is reported failed.
func TestModuleFetchOutcomes(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "x")
	cases := []struct {
		name   string
		errs   []error
		header *invv1.ModuleBundleHeader
		state  string
		reason string
		writes int
	}{
		{"retried", []error{unavailable, status.Error(codes.ResourceExhausted, "x")}, nil, "installed", "", 1},
		{"not found", []error{status.Error(codes.NotFound, "x")}, nil, "", "", 0},
		{"too large", []error{sender.ErrBundleTooLarge}, nil, "failed", "too_large", 0},
		{"malformed", []error{sender.ErrBundleMalformed}, nil, "failed", "invalid_bundle", 0},
		{"refused", []error{status.Error(codes.FailedPrecondition, "plaintext")}, nil, "failed", "fetch_failed", 0},
		{"other item", nil, &invv1.ModuleBundleHeader{ItemId: "item-2", Module: "sms-gw"}, "failed", "invalid_bundle", 0},
		{"other module", nil, &invv1.ModuleBundleHeader{ItemId: "item-1", Module: "mail"}, "failed", "invalid_bundle", 0},
	}
	for _, c := range cases {
		d, w, cl := moduleDaemon(nil)
		cl.fetchErrs, cl.header = c.errs, c.header
		d.handleCommand(context.Background(), moduleCmd("item-1", 1))
		drainModules(d)
		_, reps := cl.snapshot()
		if w.count() != c.writes {
			t.Errorf("%s: writes = %d", c.name, w.count())
		}
		if c.state == "" {
			if len(reps) != 0 {
				t.Errorf("%s: reported %v", c.name, reps)
			}
			continue
		}
		if len(reps) != 1 || reps[0].GetState() != c.state || reps[0].GetReason() != c.reason {
			t.Errorf("%s: reports = %v", c.name, reps)
		}
	}
	// The stream ends while retrying: released for the replay.
	d, w, cl := moduleDaemon(nil)
	cl.fetchErrs = []error{unavailable}
	sctx, cancel := context.WithCancel(context.Background())
	d.enqueueModule(sctx, moduleCmd("item-1", 1))
	cancel()
	drainModules(d)
	if _, reps := cl.snapshot(); len(reps) != 0 || w.count() != 0 || !d.mods.claim(certKey("item-1", 1)) {
		t.Fatal("not released")
	}
	// Retry backoff waits, and stops with the stream.
	d, _, cl = moduleDaemon(nil)
	d.mods.backoff = func(int) time.Duration { return time.Hour }
	cl.fetchErrs = []error{unavailable}
	sctx, cancel = context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, _, err := d.fetchModule(sctx, "item-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff: %v", err)
	}
}

// TestModuleDisabledLocally (US3): disabled_locally, nothing fetched.
func TestModuleDisabledLocally(t *testing.T) {
	d, w, cl := moduleDaemon(func(c *config.AgentConfig) { c.Modules.Enabled = false })
	d.handleCommand(context.Background(), moduleCmd("item-1", 1))
	drainModules(d)
	f, reps := cl.snapshot()
	if f != 0 || w.count() != 0 || len(reps) != 1 || reps[0].GetReason() != "disabled_locally" || reps[0].GetHookExitCode() != -1 {
		t.Fatalf("fetches=%d reports=%v", f, reps)
	}
}

// TestModuleUnsentReport (T015): a report that could not be sent is kept
// and re-sent on the next command for the item, without writing again.
func TestModuleUnsentReport(t *testing.T) {
	d, w, cl := moduleDaemon(nil)
	cl.reportErr = errors.New("down")
	d.handleCommand(context.Background(), moduleCmd("item-1", 1))
	drainModules(d)
	if _, ok := d.mods.takeUnsent("item-1"); !ok {
		t.Fatal("report not kept")
	}
	cl.reportErr, cl.notAccept = nil, true
	d.handleCommand(context.Background(), moduleCmd("item-1", 1))
	drainModules(d)
	if _, reps := cl.snapshot(); w.count() != 1 || len(reps) != 1 || reps[0].GetState() != "installed" {
		t.Fatalf("writes=%d reports=%v", w.count(), reps)
	}
	if _, ok := d.mods.takeUnsent("item-1"); ok {
		t.Fatal("kept after sending")
	}
}

// TestModuleQueueFull: one item at a time; a full queue reports busy.
func TestModuleQueueFull(t *testing.T) {
	d, _, cl := moduleDaemon(nil)
	for i := 0; i < certQueueSize; i++ {
		d.enqueueModule(context.Background(), moduleCmd("item-"+string(rune('a'+i%26))+strings.Repeat("x", i/26), 1))
	}
	d.enqueueModule(context.Background(), moduleCmd("overflow", 1))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, reps := cl.snapshot(); len(reps) == 1 {
			if reps[0].GetItemId() != "overflow" || reps[0].GetReason() != "busy" {
				t.Fatalf("report = %v", reps[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no busy report")
		}
		time.Sleep(time.Millisecond)
	}
	// The loop drains the queue and stops with ctx.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.moduleLoop(ctx); close(done) }()
	for len(d.mods.queue) > 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestModuleReportOf(t *testing.T) {
	r := moduleReportOf("i", agentmodules.Result{State: "hook_failed", Reason: "hook_failed", Detail: strings.Repeat("d", 400), HookExitCode: 3})
	if r.GetItemId() != "i" || len(r.GetDetail()) != maxReportDetail || r.GetHookExitCode() != 3 || r.GetReason() != "hook_failed" {
		t.Fatalf("report = %v", r)
	}
}
