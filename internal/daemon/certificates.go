package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// CertInstaller is the local certificate store (agentcerts.Store).
type CertInstaller interface {
	Install(ctx context.Context, req agentcerts.Request) agentcerts.Result
}

// CertClient fetches delivery items and reports their outcome
// (sender.CertClient).
type CertClient interface {
	Fetch(ctx context.Context, itemID string) (*invv1.CertificateBundle, error)
	Report(ctx context.Context, req *invv1.ReportCertificateRequest) (bool, error)
}

const (
	// certQueueSize bounds queued CERTIFICATE commands (the server replays
	// at most 50 per connect).
	certQueueSize = 128
	// certDoneMemory bounds the item ids remembered as finished.
	certDoneMemory = 1024
	// maxReportDetail is the report detail bound (data-model §1.1).
	maxReportDetail = 256

	certBaseBackoff = 2 * time.Second
	certMaxBackoff  = time.Minute
)

// certJob is one CERTIFICATE command. ctx is the stream's lifetime: fetch
// retries stop when the stream ends (the server replays active items on the
// next connect).
type certJob struct {
	ctx    context.Context
	itemID string
}

// certState deduplicates and queues CERTIFICATE commands: an item is handled
// once at a time, finished items are remembered (bounded), and a result
// whose report could not be sent is kept and re-sent on the next command for
// the item instead of installing again.
type certState struct {
	mu       sync.Mutex
	inflight map[string]bool
	done     map[string]bool
	order    []string
	unsent   map[string]*invv1.ReportCertificateRequest
	queue    chan certJob
	backoff  func(attempt int) time.Duration
}

func newCertState() *certState {
	return &certState{inflight: map[string]bool{}, done: map[string]bool{}, unsent: map[string]*invv1.ReportCertificateRequest{},
		queue: make(chan certJob, certQueueSize), backoff: certBackoff}
}

func certBackoff(attempt int) time.Duration {
	d := certBaseBackoff << (attempt - 1)
	if d > certMaxBackoff || d <= 0 {
		d = certMaxBackoff
	}
	return d
}

// claim marks an item in flight; false when it is already queued, running
// or finished.
func (c *certState) claim(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight[id] || c.done[id] {
		return false
	}
	c.inflight[id] = true
	return true
}

// release lets a later command for the item run again.
func (c *certState) release(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inflight, id)
}

// finish remembers an item as reported.
func (c *certState) finish(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inflight, id)
	delete(c.unsent, id)
	if c.done[id] {
		return
	}
	c.done[id] = true
	c.order = append(c.order, id)
	if len(c.order) > certDoneMemory {
		delete(c.done, c.order[0])
		c.order = c.order[1:]
	}
}

// keepUnsent stores a result whose report failed and releases the item.
func (c *certState) keepUnsent(r *invv1.ReportCertificateRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unsent[r.GetItemId()] = r
	delete(c.inflight, r.GetItemId())
}

func (c *certState) takeUnsent(id string) *invv1.ReportCertificateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unsent[id]
}

// WithCertificates enables certificate delivery with the local store (Linux
// only; the caller passes nil elsewhere). The agent announces cert.v1 when
// the local configuration enables it and the transport allows it.
func (d *Daemon) WithCertificates(st CertInstaller) *Daemon {
	d.certStore = st
	return d
}

// certsActive reports whether the agent announces and handles cert.v1.
func (d *Daemon) certsActive() bool {
	return d.certStore != nil && d.cfg.Certificates.Announce(d.goos, d.cfg.Insecure)
}

// enqueueCertificate queues a CERTIFICATE command (ids only), ignoring
// duplicates of an item that is queued, running or already reported.
func (d *Daemon) enqueueCertificate(ctx context.Context, cmd *invv1.Command) {
	id := cmd.GetCertificate().GetItemId()
	if id == "" {
		log.Printf("daemon: ignoring certificate command %s without an item id", cmd.GetCommandId())
		return
	}
	if !d.certs.claim(id) {
		log.Printf("daemon: certificate item %s already handled; duplicate command ignored", id)
		return
	}
	select {
	case d.certs.queue <- certJob{ctx: ctx, itemID: id}:
	default:
		log.Printf("daemon: certificate item %s refused: queue full", id)
		go d.sendCertReport(ctx, &invv1.ReportCertificateRequest{ItemId: id, State: store.DeliveryFailed, Reason: store.ReasonBusy,
			HookExitCode: agentcerts.HookNotRun})
	}
}

// certLoop handles queued certificate commands one at a time until ctx ends.
func (d *Daemon) certLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.certs.queue:
			d.processCertificate(ctx, j)
		}
	}
}

// processCertificate fetches, installs and reports one item. Material is
// held only for the install; nothing of it is logged.
func (d *Daemon) processCertificate(ctx context.Context, j certJob) {
	if r := d.certs.takeUnsent(j.itemID); r != nil {
		d.sendCertReport(ctx, r)
		return
	}
	if !d.certsActive() {
		d.sendCertReport(ctx, &invv1.ReportCertificateRequest{ItemId: j.itemID, State: store.DeliveryFailed,
			Reason: store.ReasonDisabledLocally, HookExitCode: agentcerts.HookNotRun})
		return
	}
	b, err := d.fetchCertificate(j.ctx, j.itemID)
	if err != nil {
		log.Printf("daemon: certificate item %s: fetch: %s", j.itemID, status.Code(err))
		if status.Code(err) == codes.NotFound {
			d.certs.finish(j.itemID) // not ours or no longer active
			return
		}
		d.certs.release(j.itemID)
		return
	}
	res := d.certStore.Install(ctx, agentcerts.Request{ItemID: j.itemID, Name: b.GetName(), CertificateID: b.GetCertificateId(),
		CertPEM: []byte(b.GetCertPem()), ChainPEM: []byte(b.GetChainPem()), KeyPEM: []byte(b.GetKeyPem()), HasKey: b.GetHasKey(),
		RerunHook: b.GetRerunHook(), IsRenewal: b.GetIsRenewal()})
	b.KeyPem = ""
	d.sendCertReport(ctx, reportOf(j.itemID, res))
}

// fetchCertificate calls FetchCertificate, retrying transient errors with
// backoff while ctx (the stream) lives.
func (d *Daemon) fetchCertificate(ctx context.Context, id string) (*invv1.CertificateBundle, error) {
	client := d.newCertClient(d.agentID, d.credential)
	for attempt := 1; ; attempt++ {
		b, err := client.Fetch(ctx, id)
		if err == nil {
			return b, nil
		}
		if !transientFetch(err) || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.certs.backoff(attempt)):
		}
	}
}

func transientFetch(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.DeadlineExceeded, codes.Aborted:
		return true
	}
	return false
}

// sendCertReport reports a result; a failed report is kept for the next
// command of the item (the server replays it while the item is active).
func (d *Daemon) sendCertReport(ctx context.Context, r *invv1.ReportCertificateRequest) {
	accepted, err := d.newCertClient(d.agentID, d.credential).Report(ctx, r)
	if err != nil {
		log.Printf("daemon: certificate item %s: report %s failed: %v", r.GetItemId(), r.GetState(), err)
		d.certs.keepUnsent(r)
		return
	}
	if !accepted {
		log.Printf("daemon: certificate item %s: report %s ignored by the server", r.GetItemId(), r.GetState())
	}
	d.certs.finish(r.GetItemId())
}

// reportOf maps a store result to the report (detail bounded, never hook
// output).
func reportOf(itemID string, res agentcerts.Result) *invv1.ReportCertificateRequest {
	detail := res.Detail
	if len(detail) > maxReportDetail {
		detail = detail[:maxReportDetail]
	}
	return &invv1.ReportCertificateRequest{ItemId: itemID, State: res.State, Serial: res.Serial, FingerprintSha256: res.Fingerprint,
		Reason: res.Reason, HookExitCode: int32(res.HookExitCode), Detail: detail} // #nosec G115 -- -1..256
}
