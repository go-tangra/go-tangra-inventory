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
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type fakeInstaller struct {
	mu   sync.Mutex
	reqs []agentcerts.Request
	res  agentcerts.Result
}

func (f *fakeInstaller) Install(_ context.Context, r agentcerts.Request) agentcerts.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return f.res
}

func (f *fakeInstaller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

type fakeCertClient struct {
	mu         sync.Mutex
	fetchErrs  []error // returned in order before success
	bundle     *invv1.CertificateBundle
	fetches    int
	reports    []*invv1.ReportCertificateRequest
	reportErr  error
	notAccept  bool
	agentCreds []string
}

func (f *fakeCertClient) Fetch(_ context.Context, id string) (*invv1.CertificateBundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if len(f.fetchErrs) > 0 {
		err := f.fetchErrs[0]
		f.fetchErrs = f.fetchErrs[1:]
		return nil, err
	}
	b := proto.Clone(f.bundle).(*invv1.CertificateBundle)
	b.ItemId = id
	return b, nil
}

func (f *fakeCertClient) Report(_ context.Context, r *invv1.ReportCertificateRequest) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reportErr != nil {
		return false, f.reportErr
	}
	f.reports = append(f.reports, r)
	return !f.notAccept, nil
}

func (f *fakeCertClient) snapshot() (int, []*invv1.ReportCertificateRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches, append([]*invv1.ReportCertificateRequest(nil), f.reports...)
}

func certDaemon(mut func(*config.AgentConfig)) (*Daemon, *fakeInstaller, *fakeCertClient) {
	cfg := config.DefaultAgent()
	if mut != nil {
		mut(&cfg)
	}
	inst := &fakeInstaller{res: agentcerts.Result{State: store.DeliveryInstalled, Serial: "4f3a", Fingerprint: "ab12", HookExitCode: 0}}
	cl := &fakeCertClient{bundle: &invv1.CertificateBundle{Name: "www", CertificateId: "c1", CertPem: "CERT", ChainPem: "CHAIN",
		KeyPem: "KEY", HasKey: true, RerunHook: true, IsRenewal: true}}
	d := New(cfg, "4.7.0").WithCertificates(inst)
	d.goos = "linux"
	d.agentID, d.credential = "a1", "cred"
	d.newCertClient = func(id, cred string) CertClient {
		cl.mu.Lock()
		cl.agentCreds = append(cl.agentCreds, id+"/"+cred)
		cl.mu.Unlock()
		return cl
	}
	d.certs.backoff = func(int) time.Duration { return time.Millisecond }
	return d, inst, cl
}

func certCmd(id string) *invv1.Command {
	return &invv1.Command{CommandId: "cmd-" + id, Type: invv1.CommandType_COMMAND_TYPE_CERTIFICATE,
		Certificate: &invv1.CertificateCommand{ItemId: id, Name: "www"}}
}

// drain processes every queued job.
func drain(d *Daemon) {
	for {
		select {
		case j := <-d.certs.queue:
			d.processCertificate(context.Background(), j)
		default:
			return
		}
	}
}

func hasCap(req *invv1.StreamRequest, c string) bool {
	for _, x := range req.GetCapabilities() {
		if x == c {
			return true
		}
	}
	return false
}

func TestCertCapabilityAnnounced(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*config.AgentConfig)
		goos  string
		store bool
		want  bool
	}{
		{"enabled linux tls", nil, "linux", true, true},
		{"disabled locally", func(c *config.AgentConfig) { c.Certificates.Enabled = false }, "linux", true, false},
		{"windows", nil, "windows", true, false},
		{"no store", nil, "linux", false, false},
		{"plaintext", func(c *config.AgentConfig) { c.Insecure = true }, "linux", true, false},
		{"plaintext allowed", func(c *config.AgentConfig) { c.Insecure, c.Certificates.AllowInsecureTransport = true, true }, "linux", true, true},
	}
	for _, c := range cases {
		d, _, _ := certDaemon(c.mut)
		d.goos = c.goos
		if !c.store {
			d.certStore = nil
		}
		if got := hasCap(d.streamRequest(), store.CapCertV1); got != c.want {
			t.Fatalf("%s: cert.v1 = %v", c.name, got)
		}
	}
	// Both capabilities together.
	d, _, _ := certDaemon(nil)
	d.WithUpgrades(deb, func(string, string) Upgrader { return &fakeUpgrader{} })
	d.upg = d.newUpgrader("a1", "cred")
	if req := d.streamRequest(); !hasCap(req, store.CapUpgradeV1) || !hasCap(req, store.CapCertV1) {
		t.Fatalf("capabilities = %v", req.GetCapabilities())
	}
}

// TestCertificateCommandFlow: CERTIFICATE (ids only) → fetch → install →
// report with the store's result.
func TestCertificateCommandFlow(t *testing.T) {
	d, inst, cl := certDaemon(nil)
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	if inst.count() != 1 {
		t.Fatalf("installs = %d", inst.count())
	}
	r := inst.reqs[0]
	if r.ItemID != "item-1" || r.Name != "www" || r.CertificateID != "c1" || string(r.CertPEM) != "CERT" || string(r.ChainPEM) != "CHAIN" ||
		string(r.KeyPEM) != "KEY" || !r.HasKey || !r.RerunHook || !r.IsRenewal {
		t.Fatalf("request = %+v", r)
	}
	_, reps := cl.snapshot()
	if len(reps) != 1 {
		t.Fatalf("reports = %d", len(reps))
	}
	rep := reps[0]
	if rep.GetItemId() != "item-1" || rep.GetState() != "installed" || rep.GetSerial() != "4f3a" || rep.GetFingerprintSha256() != "ab12" ||
		rep.GetHookExitCode() != 0 || rep.GetReason() != "" {
		t.Fatalf("report = %v", rep)
	}
	if cl.agentCreds[0] != "a1/cred" {
		t.Fatalf("client built for %v", cl.agentCreds)
	}
	// A replay of a reported item is ignored.
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	if f, _ := cl.snapshot(); f != 1 || inst.count() != 1 {
		t.Fatal("replayed item handled again")
	}
}

func TestCertificateDuplicatesWhileQueued(t *testing.T) {
	d, inst, cl := certDaemon(nil)
	for i := 0; i < 3; i++ {
		d.enqueueCertificate(context.Background(), certCmd("item-1"))
	}
	d.enqueueCertificate(context.Background(), &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_CERTIFICATE})
	if len(d.certs.queue) != 1 {
		t.Fatalf("queued = %d", len(d.certs.queue))
	}
	drain(d)
	if f, reps := cl.snapshot(); f != 1 || len(reps) != 1 || inst.count() != 1 {
		t.Fatalf("fetches=%d reports=%d", f, len(reps))
	}
}

func TestCertificateDisabledLocally(t *testing.T) {
	d, inst, cl := certDaemon(func(c *config.AgentConfig) { c.Certificates.Enabled = false })
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	f, reps := cl.snapshot()
	if f != 0 || inst.count() != 0 || len(reps) != 1 || reps[0].GetState() != "failed" || reps[0].GetReason() != "disabled_locally" ||
		reps[0].GetHookExitCode() != -1 {
		t.Fatalf("fetches=%d reports=%v", f, reps)
	}
	// No store on this platform: same answer.
	d, _, cl = certDaemon(nil)
	d.certStore = nil
	d.handleCommand(context.Background(), certCmd("item-2"))
	drain(d)
	if _, reps := cl.snapshot(); len(reps) != 1 || reps[0].GetReason() != "disabled_locally" {
		t.Fatalf("reports=%v", reps)
	}
}

func TestCertificateFetchErrors(t *testing.T) {
	// NotFound: nothing written, no report, not retried.
	d, inst, cl := certDaemon(nil)
	cl.fetchErrs = []error{status.Error(codes.NotFound, "no")}
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	if f, reps := cl.snapshot(); f != 1 || len(reps) != 0 || inst.count() != 0 {
		t.Fatalf("not found: fetches=%d reports=%d", f, len(reps))
	}

	// Unavailable/ResourceExhausted: retried with backoff, then installed.
	d, inst, cl = certDaemon(nil)
	cl.fetchErrs = []error{status.Error(codes.Unavailable, "x"), status.Error(codes.ResourceExhausted, "y")}
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	if f, reps := cl.snapshot(); f != 3 || len(reps) != 1 || inst.count() != 1 {
		t.Fatalf("transient: fetches=%d reports=%d", f, len(reps))
	}

	// A permanent error releases the item: the next command fetches again.
	d, inst, cl = certDaemon(nil)
	cl.fetchErrs = []error{status.Error(codes.FailedPrecondition, "plaintext")}
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	if f, reps := cl.snapshot(); f != 2 || len(reps) != 1 || inst.count() != 1 {
		t.Fatalf("permanent: fetches=%d reports=%d", f, len(reps))
	}

	// Retries end with the stream.
	d, inst, cl = certDaemon(nil)
	d.certs.backoff = func(int) time.Duration { return time.Hour }
	cl.fetchErrs = []error{status.Error(codes.Unavailable, "x")}
	sctx, cancel := context.WithCancel(context.Background())
	d.enqueueCertificate(sctx, certCmd("item-1"))
	done := make(chan struct{})
	go func() { drain(d); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch retry outlived the stream")
	}
	cl.fetchErrs = []error{status.Error(codes.Unavailable, "x")}
	cctx, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := d.fetchCertificate(cctx, "item-9"); err == nil {
		t.Fatal("canceled fetch")
	}
	if inst.count() != 0 || !d.certs.claim("item-1") {
		t.Fatal("item not released after the stream ended")
	}
}

// TestCertificateReportRetriedFromMemory: a result whose report failed is
// re-sent on the next command for the item without installing again.
func TestCertificateReportRetriedFromMemory(t *testing.T) {
	d, inst, cl := certDaemon(nil)
	cl.reportErr = errors.New("unavailable")
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	cl.mu.Lock()
	cl.reportErr, cl.notAccept = nil, true
	cl.mu.Unlock()
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	f, reps := cl.snapshot()
	if f != 1 || inst.count() != 1 || len(reps) != 1 || reps[0].GetState() != "installed" {
		t.Fatalf("fetches=%d installs=%d reports=%v", f, inst.count(), reps)
	}
	if d.certs.takeUnsent("item-1") != nil {
		t.Fatal("unsent kept after delivery")
	}
}

func TestCertificateQueueFull(t *testing.T) {
	d, _, cl := certDaemon(nil)
	for i := 0; i < certQueueSize; i++ {
		d.enqueueCertificate(context.Background(), certCmd("q"+string(rune('A'+i%26))+string(rune('a'+i/26))))
	}
	d.enqueueCertificate(context.Background(), certCmd("overflow"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, reps := cl.snapshot(); len(reps) == 1 {
			if reps[0].GetItemId() != "overflow" || reps[0].GetReason() != "busy" || reps[0].GetState() != "failed" {
				t.Fatalf("busy report = %v", reps[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no busy report")
}

func TestCertLoopStopsWithContext(t *testing.T) {
	d, inst, _ := certDaemon(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.certLoop(ctx); close(done) }()
	d.enqueueCertificate(ctx, certCmd("item-1"))
	deadline := time.Now().Add(5 * time.Second)
	for inst.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if inst.count() != 1 {
		t.Fatal("loop did not process")
	}
}

func TestCertStateMemoryBounded(t *testing.T) {
	c := newCertState()
	for i := 0; i < certDoneMemory+10; i++ {
		id := strings.Repeat("x", i%7) + string(rune(i))
		c.claim(id)
		c.finish(id)
	}
	c.finish(c.order[len(c.order)-1])
	if len(c.done) != certDoneMemory || len(c.order) != certDoneMemory {
		t.Fatalf("done=%d order=%d", len(c.done), len(c.order))
	}
	if certBackoff(1) != certBaseBackoff || certBackoff(30) != certMaxBackoff || certBackoff(100) != certMaxBackoff {
		t.Fatal("backoff")
	}
}

func TestReportOfBoundsDetail(t *testing.T) {
	r := reportOf("i", agentcerts.Result{State: "failed", Reason: "write_failed", Detail: strings.Repeat("d", 400), HookExitCode: -1})
	if len(r.GetDetail()) != maxReportDetail || r.GetHookExitCode() != -1 {
		t.Fatalf("%v", r)
	}
}

// TestCertificateProtocolGuard (T069): the server can name a certificate
// and nothing else — no path, command, environment, owner or mode field
// exists in the messages the agent acts on, and unknown fields are ignored.
func TestCertificateProtocolGuard(t *testing.T) {
	forbidden := []string{"path", "dir", "command", "cmd", "env", "owner", "group", "mode", "exec", "script", "arg", "user", "file", "url"}
	for _, md := range []protoreflect.MessageDescriptor{(&invv1.CertificateCommand{}).ProtoReflect().Descriptor(),
		(&invv1.CertificateBundle{}).ProtoReflect().Descriptor()} {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			name := string(f.Name())
			if name == "rerun_hook" {
				if f.Kind() != protoreflect.BoolKind {
					t.Fatal("rerun_hook must stay a flag")
				}
				continue
			}
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Fatalf("%s.%s looks like host-side control data", md.Name(), name)
				}
			}
		}
	}
	// A bundle carrying an unknown field (a newer server) installs the same.
	d, inst, cl := certDaemon(nil)
	raw, _ := proto.Marshal(cl.bundle)
	raw = protowire.AppendTag(raw, 99, protowire.BytesType)
	raw = protowire.AppendString(raw, "/bin/sh -c 'rm -rf /'")
	var withUnknown invv1.CertificateBundle
	if err := proto.Unmarshal(raw, &withUnknown); err != nil {
		t.Fatal(err)
	}
	cl.bundle = &withUnknown
	d.handleCommand(context.Background(), certCmd("item-1"))
	drain(d)
	if inst.count() != 1 || inst.reqs[0].Name != "www" {
		t.Fatalf("unknown field changed handling: %+v", inst.reqs)
	}
}
