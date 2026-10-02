package certdelivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestOnConnectReplay (T054): active items oldest first, at most 50;
// pending become delivered (audited once); delivered/fetched are re-sent.
func TestOnConnectReplay(t *testing.T) {
	f := newFix(t)
	h, a := f.webHost(t, "web-1", false)
	names := []string{"a", "b", "c"}
	var ids []string
	for k, n := range names {
		r := f.req("job-"+n, h.ID)
		r.Name = n
		f.now = t0.Add(time.Duration(k) * time.Minute)
		v, err := f.svc.Create(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.Items[0].ID)
	}
	f.reg.online[a.ID] = true
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != 3 {
		t.Fatalf("replay: %+v %v", cmds, err)
	}
	for k, c := range cmds {
		if c.Certificate.ItemID != ids[k] || c.Certificate.Name != names[k] {
			t.Fatalf("order: %+v", cmds)
		}
	}
	if f.count("cert_delivery_delivered") != 3 || f.item(t, ids[0]).State != store.DeliveryDelivered {
		t.Fatalf("delivered: %v", f.actions())
	}
	// Fetch one; reconnect re-sends all, no new delivered rows.
	m, _ := f.svc.Fetch(ctx, a, ids[1])
	m.Wipe()
	cmds, _ = f.svc.OnConnect(ctx, a)
	if len(cmds) != 3 || f.count("cert_delivery_delivered") != 3 {
		t.Fatalf("second connect: %d %v", len(cmds), f.actions())
	}
	f.mem.FailNext("ListActiveCertItemsForAgent")
	if _, err := f.svc.OnConnect(ctx, a); err == nil {
		t.Fatal("store error")
	}
}

func TestOnConnectReplayLimit(t *testing.T) {
	f := newFix(t)
	_, a := f.webHost(t, "web-1", false)
	for k := 0; k < store.MaxReplayPerConnect+5; k++ {
		h := f.host(t, "h"+itoa(k), nil)
		// Items of other hosts bound to this agent id exercise the limit.
		r := f.req("job-"+itoa(k), h.ID)
		v, _ := f.svc.Create(ctx, r)
		_, _ = f.mem.UpdateCertItem(ctx, tenant, v.Items[0].ID, func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
			i.AgentID, i.State, i.Reason, i.FinishedAt = a.ID, store.DeliveryPending, "", nil
			return repo.CertItemChange{}, nil
		})
	}
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != store.MaxReplayPerConnect {
		t.Fatalf("limit: %d %v", len(cmds), err)
	}
}

func TestOnConnectWithoutCapability(t *testing.T) {
	f := newFix(t)
	h, a := f.webHost(t, "web-1", false)
	v, _ := f.svc.Create(ctx, f.req("job-1", h.ID))
	r := f.req("job-2", h.ID)
	r.Name = "api"
	v2, _ := f.svc.Create(ctx, r)
	// Fetched before the agent lost the capability: left alone.
	f.reg.online[a.ID] = true
	cmds, _ := f.svc.OnConnect(ctx, a)
	m, _ := f.svc.Fetch(ctx, a, v2.Items[0].ID)
	m.Wipe()
	_ = cmds
	a.Capabilities = []string{store.CapUpgradeV1}
	cmds, err := f.svc.OnConnect(ctx, a)
	if err != nil || len(cmds) != 0 {
		t.Fatalf("no capability: %v %v", cmds, err)
	}
	if g := f.item(t, v.Items[0].ID); g.State != store.DeliveryUnsupported || g.Reason != store.ReasonNoCapability {
		t.Fatalf("item = %+v", g)
	}
	if g := f.item(t, v2.Items[0].ID); g.State != store.DeliveryFetched {
		t.Fatalf("fetched item = %+v", g)
	}
	// Windows: platform.
	f = newFix(t)
	h, a = f.webHost(t, "web-1", false)
	v, _ = f.svc.Create(ctx, f.req("job-1", h.ID))
	a.OS = "windows"
	_, _ = f.svc.OnConnect(ctx, a)
	if g := f.item(t, v.Items[0].ID); g.State != store.DeliveryUnsupported || g.Reason != store.ReasonPlatform {
		t.Fatalf("windows item = %+v", g)
	}
}

// TestSupersede (T055): a new item for the same host and name supersedes
// the active one; automatic triggers never install an earlier-expiring
// certificate; manual ones do.
func TestSupersede(t *testing.T) {
	f := newFix(t)
	h, _ := f.webHost(t, "web-1", false)
	v1, _ := f.svc.Create(ctx, f.req("job-1", h.ID))
	v2, err := f.svc.Create(ctx, f.req("job-2", h.ID))
	if err != nil {
		t.Fatal(err)
	}
	if g := f.item(t, v1.Items[0].ID); g.State != store.DeliverySuperseded {
		t.Fatalf("old item = %+v", g)
	}
	var row map[string]any
	for _, r := range f.mem.AuditRows() {
		if r.Action == "cert_delivery_superseded" {
			row = r.Detail
		}
	}
	if row["superseded_by"] != v2.Items[0].ID {
		t.Fatalf("superseded row = %+v", row)
	}
	// Install a long-lived certificate, then auto-deploy a shorter one.
	_, a := f.hosts["web-1"], f.agent["web-1"]
	f.reg.online[a.ID] = true
	m, _ := f.svc.Fetch(ctx, a, v2.Items[0].ID)
	fp := m.Bundle.Fingerprint
	m.Wipe()
	_, _ = f.svc.Report(ctx, a, Report{ItemID: v2.Items[0].ID, State: store.DeliveryInstalled, Fingerprint: fp, HookExitCode: -1})
	f.lcm.certs["short"] = bundle(t, "ecdsa.crt", "ecdsa.pkcs8.key", t0.Add(30*24*time.Hour))
	r := f.req("job-3", h.ID)
	r.CertificateID, r.Trigger = "short", store.TriggerAutoDeploy
	v3, err := f.svc.Create(ctx, r)
	if err != nil || v3.Items[0].State != store.DeliverySuperseded || v3.Items[0].Reason != store.ReasonOlderThanInstalled {
		t.Fatalf("auto older: %+v %v", v3.Items, err)
	}
	if len(f.reg.delivered) != 0 {
		t.Fatal("older certificate pushed")
	}
	// Retry trigger: same rule.
	r.IdempotencyKey, r.Trigger = "job-4", store.TriggerRetry
	if v, _ := f.svc.Create(ctx, r); v.Items[0].Reason != store.ReasonOlderThanInstalled {
		t.Fatalf("retry older: %+v", v.Items)
	}
	// Manual: installed anyway (audited as a normal request).
	r.IdempotencyKey, r.Trigger = "job-5", store.TriggerManual
	if v, _ := f.svc.Create(ctx, r); v.Items[0].State != store.DeliveryDelivered {
		t.Fatalf("manual older: %+v", v.Items)
	}
	// A revoked installed certificate never blocks an automatic delivery.
	f2 := newFix(t)
	it, a2 := f2.fetched(t, "job-1")
	_, _ = f2.svc.Report(ctx, a2, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: -1})
	_, _, _ = f2.svc.MarkRevoked(ctx, tenant, Actor{Kind: "service", ID: "deployer"}, cert1)
	f2.lcm.certs["short"] = bundle(t, "ecdsa.crt", "ecdsa.pkcs8.key", t0.Add(30*24*time.Hour))
	r = f2.req("job-2", it.HostID)
	r.CertificateID, r.Trigger = "short", store.TriggerAutoDeploy
	if v, _ := f2.svc.Create(ctx, r); v.Items[0].State != store.DeliveryDelivered {
		t.Fatalf("revoked installed: %+v", v.Items)
	}
}

// TestSweep (T056): expiry and missing reports; idempotent.
func TestSweep(t *testing.T) {
	f := newFix(t)
	web1, a := f.webHost(t, "web-1", true)
	web2, _ := f.webHost(t, "web-2", false)
	v, _ := f.svc.Create(ctx, f.req("job-1", web1.ID, web2.ID))
	fetchedID := itemFor(v, web1.ID).ID
	m, _ := f.svc.Fetch(ctx, a, fetchedID)
	m.Wipe()
	// 10 minutes: nothing.
	f.now = t0.Add(10 * time.Minute)
	if e, fl, err := f.svc.Sweep(ctx); e != 0 || fl != 0 || err != nil {
		t.Fatal(e, fl, err)
	}
	// 16 minutes: the fetched item fails with no_report.
	f.now = t0.Add(16 * time.Minute)
	if e, fl, err := f.svc.Sweep(ctx); e != 0 || fl != 1 || err != nil {
		t.Fatal(e, fl, err)
	}
	if g := f.item(t, fetchedID); g.State != store.DeliveryFailed || g.Reason != store.ReasonNoReport {
		t.Fatalf("no report: %+v", g)
	}
	// After the TTL the pending one expires.
	f.now = t0.Add(169 * time.Hour)
	if e, fl, err := f.svc.Sweep(ctx); e != 1 || fl != 0 || err != nil {
		t.Fatal(e, fl, err)
	}
	if g := f.item(t, itemFor(v, web2.ID).ID); g.State != store.DeliveryExpired || g.Reason != store.ReasonExpired || g.FinishedAt == nil {
		t.Fatalf("expired: %+v", g)
	}
	if f.count("cert_delivery_expired") != 1 {
		t.Fatalf("audit: %v", f.actions())
	}
	// Idempotent.
	if e, fl, _ := f.svc.Sweep(ctx); e+fl != 0 {
		t.Fatal("second sweep changed items")
	}
	// A stale listing (another replica already swept) changes nothing.
	f2 := newFix(t)
	w, _ := f2.webHost(t, "web-1", false)
	v2, _ := f2.svc.Create(ctx, f2.req("job-1", w.ID))
	f2.svc.repo = &staleRepo{faultRepo: f2.repo, items: []repo.StaleCertItem{{Item: f2.item(t, v2.Items[0].ID), ExpiresAt: t0.Add(time.Hour)}}}
	if e, fl, err := f2.svc.Sweep(ctx); e+fl != 0 || err != nil {
		t.Fatal("stale listing swept", e, fl, err)
	}
	// Store errors.
	f2.svc.repo = f2.repo
	f2.mem.FailNext("ListStaleCertItems")
	if _, _, err := f2.svc.Sweep(ctx); err == nil {
		t.Fatal("list error")
	}
	f2.now = t0.Add(200 * time.Hour)
	f2.mem.FailNext("UpdateCertItem")
	if _, _, err := f2.svc.Sweep(ctx); err == nil {
		t.Fatal("update error")
	}
}

// staleRepo serves a fixed stale listing.
type staleRepo struct {
	*faultRepo
	items []repo.StaleCertItem
}

func (s *staleRepo) ListStaleCertItems(context.Context, time.Time, time.Time, int) ([]repo.StaleCertItem, error) {
	return s.items, nil
}

// TestRearm (T057): a retry re-arms failed, hook_failed and expired items
// (attempts+1, rerun_hook only after hook_failed, at most 5) and pushes
// them; done and queued items stay.
func TestRearm(t *testing.T) {
	f := newFix(t)
	web1, a1 := f.webHost(t, "web-1", true)
	web2, a2 := f.webHost(t, "web-2", true)
	web3, _ := f.webHost(t, "web-3", true)
	web4, _ := f.webHost(t, "web-4", false)
	v, _ := f.svc.Create(ctx, f.req("job-1", web1.ID, web2.ID, web3.ID, web4.ID))
	i1, i2, i3, i4 := itemFor(v, web1.ID).ID, itemFor(v, web2.ID).ID, itemFor(v, web3.ID).ID, itemFor(v, web4.ID).ID
	// web-1 hook_failed, web-2 failed, web-3 installed, web-4 queued.
	for _, c := range []struct {
		id    string
		a     store.Agent
		state string
	}{{i1, a1, store.DeliveryHookFailed}, {i2, a2, store.DeliveryFailed}, {i3, f.agent["web-3"], store.DeliveryInstalled}} {
		m, err := f.svc.Fetch(ctx, c.a, c.id)
		if err != nil {
			t.Fatal(err)
		}
		fp := m.Bundle.Fingerprint
		m.Wipe()
		code := 0
		if c.state == store.DeliveryHookFailed {
			code = 1
		}
		if ok, err := f.svc.Report(ctx, c.a, Report{ItemID: c.id, State: c.state, Fingerprint: fp, HookExitCode: code, Reason: map[bool]string{true: store.ReasonWriteFailed}[c.state == store.DeliveryFailed]}); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	pushes := len(f.reg.delivered)
	f.now = t0.Add(time.Hour)
	r := f.req("job-1", web1.ID, web2.ID, web3.ID, web4.ID)
	r.RearmFailed = true
	rv, err := f.svc.Create(ctx, r)
	if err != nil || rv.Created {
		t.Fatalf("rearm: %+v %v", rv, err)
	}
	g1, g2, g3, g4 := f.item(t, i1), f.item(t, i2), f.item(t, i3), f.item(t, i4)
	if g1.State != store.DeliveryDelivered || g1.Attempts != 2 || !g1.RerunHook || g1.Fetches != 0 || g1.FingerprintSHA256 != "" || g1.HookExitCode != nil || g1.FinishedAt != nil {
		t.Fatalf("hook_failed rearmed: %+v", g1)
	}
	if g2.State != store.DeliveryDelivered || g2.Attempts != 2 || g2.RerunHook {
		t.Fatalf("failed rearmed: %+v", g2)
	}
	if g3.State != store.DeliveryInstalled || g4.State != store.DeliveryPending || g4.Attempts != 1 {
		t.Fatalf("untouched: %+v %+v", g3, g4)
	}
	if len(f.reg.delivered) != pushes+2 || f.count("cert_delivery_rearmed") != 2 {
		t.Fatalf("pushes %d audit %v", len(f.reg.delivered)-pushes, f.actions())
	}
	// The push carries the new attempt, so the agent does not dedupe it away.
	if c := f.reg.delivered[len(f.reg.delivered)-1].Certificate; c == nil || c.Attempt != 2 {
		t.Fatalf("re-armed push = %+v", c)
	}
	d, _, _ := f.mem.GetCertDelivery(ctx, tenant, v.ID)
	if !d.ExpiresAt.Equal(t0.Add(time.Hour + 168*time.Hour)) {
		t.Fatalf("window not extended: %v", d.ExpiresAt)
	}
	// Attempts are bounded.
	for n := 0; n < 5; n++ {
		if m, err := f.svc.Fetch(ctx, a2, i2); err == nil {
			m.Wipe()
		}
		_, _ = f.svc.Report(ctx, a2, Report{ItemID: i2, State: store.DeliveryFailed, Reason: store.ReasonDiskFull, HookExitCode: -1})
		_, _ = f.svc.Create(ctx, r)
	}
	if g := f.item(t, i2); g.Attempts != store.MaxItemAttempts || g.State != store.DeliveryFailed {
		t.Fatalf("attempt bound: %+v", g)
	}
	// Without rearm_failed nothing changes.
	r.RearmFailed = false
	before := f.count("cert_delivery_rearmed")
	_, _ = f.svc.Create(ctx, r)
	if f.count("cert_delivery_rearmed") != before {
		t.Fatal("re-armed without rearm_failed")
	}
}

func TestRearmEdges(t *testing.T) {
	// Expired items re-arm; a newer active item for the host and name blocks.
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", false)
	v, _ := f.svc.Create(ctx, f.req("job-1", web1.ID))
	f.now = t0.Add(169 * time.Hour)
	_, _, _ = f.svc.Sweep(ctx)
	_, _ = f.svc.Create(ctx, f.req("job-2", web1.ID)) // newer active item
	r := f.req("job-1", web1.ID)
	r.RearmFailed = true
	if _, err := f.svc.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if g := f.item(t, v.Items[0].ID); g.State != store.DeliveryExpired {
		t.Fatalf("blocked rearm: %+v", g)
	}
	// Error paths.
	for name, setup := range map[string]func(f *fix){
		"get":    func(f *fix) { f.repo.getByKey = nil; f.mem.FailNext("GetCertDelivery") },
		"extend": func(f *fix) { f.repo.extendErr = errBoom },
		"update": func(f *fix) { f.repo.updateErr[f.repo.updates+1] = errBoom },
	} {
		f := newFix(t)
		web1, _ := f.webHost(t, "web-1", false)
		f.lcm.certs[cert1] = bundle(t, "rsa.crt", "rsa.pkcs8.key", time.Time{})
		_, _ = f.svc.Create(ctx, f.req("job-1", web1.ID))
		f.now = t0.Add(169 * time.Hour)
		_, _, _ = f.svc.Sweep(ctx)
		setup(f)
		if _, err := f.svc.Create(ctx, r); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Registry down after re-arming: items stay pending (replayed on connect).
	f = newFix(t)
	web1, _ = f.webHost(t, "web-1", true)
	v, _ = f.svc.Create(ctx, f.req("job-1", web1.ID))
	f.lcm.errs[cert1] = lcmclient.ErrRevoked
	_, _ = f.svc.Fetch(ctx, f.agent["web-1"], v.Items[0].ID)
	f.reg.failList = errBoom
	d, _ := f.mem.GetCertDeliveryByKey(ctx, tenant, "deployer", "job-1")
	if err := f.svc.rearm(ctx, d, Actor{Kind: "service", ID: "deployer"}); err != nil {
		t.Fatal(err)
	}
	if g := f.item(t, v.Items[0].ID); g.State != store.DeliveryPending {
		t.Fatalf("pending after rearm: %+v", g)
	}
}

// TestMarkRevoked (US7 server part): active items cancelled, hosts flagged,
// idempotent; a later install of another certificate clears the flag.
func TestMarkRevoked(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	_, _ = f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: -1})
	web2, _ := f.webHost(t, "web-2", false)
	v2, _ := f.svc.Create(ctx, f.req("job-2", web2.ID))
	deployer := Actor{Kind: "service", ID: "deployer"}
	c, fl, err := f.svc.MarkRevoked(ctx, tenant, deployer, cert1)
	if err != nil || c != 1 || fl != 1 {
		t.Fatalf("revoke: %d %d %v", c, fl, err)
	}
	if g := f.item(t, v2.Items[0].ID); g.State != store.DeliveryCancelled || g.Reason != store.ReasonCertificateRevoked {
		t.Fatalf("cancelled: %+v", g)
	}
	hc, _ := f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www")
	if hc.RevokedAt == nil || f.count("host_certificate_revoked") != 1 || f.count("cert_delivery_cancelled") != 1 {
		t.Fatalf("flag: %+v %v", hc, f.actions())
	}
	if c, fl, _ := f.svc.MarkRevoked(ctx, tenant, deployer, cert1); c+fl != 0 {
		t.Fatal("not idempotent")
	}
	// Verify reports the host as revoked.
	hosts, _, _ := f.svc.Verify(ctx, tenant, []string{it.HostID}, nil, "www", it.FingerprintSHA256)
	if hosts[0].Status != StatusRevoked {
		t.Fatalf("verify revoked: %+v", hosts)
	}
	// A renewal installed under the name clears the flag.
	f.lcm.certs["cert-2"] = bundle(t, "ecdsa.crt", "ecdsa.pkcs8.key", t0.AddDate(5, 0, 0))
	r := f.req("job-3", it.HostID)
	r.CertificateID = "cert-2"
	v, _ := f.svc.Create(ctx, r)
	m, _ := f.svc.Fetch(ctx, a, v.Items[0].ID)
	fp := m.Bundle.Fingerprint
	m.Wipe()
	_, _ = f.svc.Report(ctx, a, Report{ItemID: v.Items[0].ID, State: store.DeliveryInstalled, Fingerprint: fp, HookExitCode: -1})
	if hc, _ := f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www"); hc.RevokedAt != nil || hc.CertificateID != "cert-2" {
		t.Fatalf("flag kept: %+v", hc)
	}
	// Errors.
	if _, _, err := f.svc.MarkRevoked(ctx, tenant, deployer, strings.Repeat("x", 129)); err == nil {
		t.Fatal("bad id")
	}
	f.mem.FailNext("CancelCertItems")
	if _, _, err := f.svc.MarkRevoked(ctx, tenant, deployer, cert1); err == nil {
		t.Fatal("cancel error")
	}
	f.mem.FailNext("RevokeHostCertificates")
	if _, _, err := f.svc.MarkRevoked(ctx, tenant, deployer, cert1); err == nil {
		t.Fatal("flag error")
	}
}

func TestCancelAndList(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", false)
	v, _ := f.svc.Create(ctx, f.req("job-1", web1.ID))
	user := Actor{Kind: "user", ID: "u1"}
	i, err := f.svc.Cancel(ctx, tenant, user, v.Items[0].ID)
	if err != nil || i.State != store.DeliveryCancelled || i.Reason != store.ReasonCancelledByUser {
		t.Fatalf("cancel: %+v %v", i, err)
	}
	row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	if row.ActorKind != "user" || row.ActorID != "u1" || row.Action != "cert_delivery_cancelled" {
		t.Fatalf("row = %+v", row)
	}
	if _, err := f.svc.Cancel(ctx, tenant, user, v.Items[0].ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatal(err)
	}
	if _, err := f.svc.Cancel(ctx, other, user, v.Items[0].ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.svc.Cancel(ctx, tenant, user, "x"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal(err)
	}
	items, total, _, err := f.svc.List(ctx, tenant, repo.CertItemFilter{HostID: web1.ID}, listReq())
	if err != nil || total != 1 || items[0].ID != v.Items[0].ID {
		t.Fatalf("list: %v %d %v", items, total, err)
	}
}

// staleDeliveryRepo lists a delivery with stale item copies.
type staleDeliveryRepo struct {
	*faultRepo
	items []store.CertDeliveryItem
}

func (s *staleDeliveryRepo) GetCertDelivery(ctx context.Context, tenantID, id string) (store.CertDelivery, []store.CertDeliveryItem, error) {
	d, _, err := s.faultRepo.GetCertDelivery(ctx, tenantID, id)
	return d, s.items, err
}

// TestRacesRecheckUnderLock: decisions are re-checked on the locked row.
func TestRacesRecheckUnderLock(t *testing.T) {
	f := newFix(t)
	web1, a := f.webHost(t, "web-1", true)
	v, _ := f.svc.Create(ctx, f.req("job-1", web1.ID))
	stale := f.item(t, v.Items[0].ID) // delivered
	// A stale pending copy is not delivered twice.
	pending := stale
	pending.State = store.DeliveryPending
	before := f.count("cert_delivery_delivered")
	f.svc.push(ctx, []store.CertDeliveryItem{pending}, map[string]bool{a.ID: true})
	if f.count("cert_delivery_delivered") != before {
		t.Fatal("delivered twice")
	}
	// A stale failed copy is not re-armed when the row is active.
	failed := stale
	failed.State = store.DeliveryFailed
	f.svc.repo = &staleDeliveryRepo{faultRepo: f.repo, items: []store.CertDeliveryItem{failed}}
	d, _ := f.mem.GetCertDeliveryByKey(ctx, tenant, "deployer", "job-1")
	if err := f.svc.rearm(ctx, d, Actor{Kind: "service", ID: "deployer"}); err != nil || f.count("cert_delivery_rearmed") != 0 {
		t.Fatalf("stale rearm: %v %v", err, f.actions())
	}
}

func TestDefaults(t *testing.T) {
	s := New(nil, nil, nil, nil, Config{})
	s.SetMetrics(nil)
	s.metrics.transition("x", "y")
	s.metrics.refused("x")
	s.metrics.lcm(time.Second, "ok")
	if s.now().Location() != time.UTC || s.Enabled() {
		t.Fatal("defaults")
	}
}

func TestResolveOrderSameHostname(t *testing.T) {
	f := newFix(t)
	a, _ := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: "twin", MachineID: "m-1"})
	b, _ := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: "twin", MachineID: "m-2"})
	for _, h := range []store.Host{a, b} {
		_ = f.mem.SetHostTags(ctx, tenant, h.ID, map[string]string{"role": "web"})
	}
	p, err := f.svc.Preview(ctx, tenant, nil, []string{"role"})
	if err != nil || len(p.Hosts) != 2 || p.Hosts[0].HostID > p.Hosts[1].HostID {
		t.Fatalf("order: %+v %v", p.Hosts, err)
	}
}
