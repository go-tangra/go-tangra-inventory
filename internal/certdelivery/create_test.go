package certdelivery

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestCreateValidation (T031): every request field is validated.
func TestCreateValidation(t *testing.T) {
	f := newFix(t)
	h, _ := f.webHost(t, "web-1", true)
	long := strings.Repeat("x", 129)
	many := make([]string, store.MaxDeliveryHosts+1)
	for i := range many {
		many[i] = h.ID
	}
	cases := map[string]func(r *Request){
		"tenant_id":        func(r *Request) { r.TenantID = "t1" },
		"source":           func(r *Request) { r.Source = "Deployer" },
		"idempotency_key":  func(r *Request) { r.IdempotencyKey = "" },
		"configuration_id": func(r *Request) { r.ConfigurationID = long },
		"target_id":        func(r *Request) { r.TargetID = long },
		"trigger":          func(r *Request) { r.Trigger = "cron" },
		"certificate_id":   func(r *Request) { r.CertificateID = "" },
		"name":             func(r *Request) { r.Name = "../etc" },
		"key_policy":       func(r *Request) { r.KeyPolicy = "maybe" },
		"selector":         func(r *Request) { r.HostIDs, r.HostTags = nil, nil },
		"host_ids":         func(r *Request) { r.HostIDs = []string{"not-a-uuid"} },
		"host_ids ":        func(r *Request) { r.HostIDs = many },
		"host_tags":        func(r *Request) { r.HostTags = []string{"=x"} },
		"host_tags ":       func(r *Request) { r.HostTags = make([]string, 17) },
	}
	for field, mut := range cases {
		r := f.req("job-1", h.ID)
		mut(&r)
		_, err := f.svc.Create(ctx, r)
		var ie *InvalidError
		if !errors.As(err, &ie) || ie.Field != strings.TrimSpace(field) || err.Error() != "invalid "+strings.TrimSpace(field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	if len(f.lcm.calls) != 0 || len(f.mem.AuditRows()) != 0 {
		t.Fatal("invalid requests reached lcm or the store")
	}
}

func TestDisabled(t *testing.T) {
	f := newFix(t)
	f.svc.cfg.Enabled = false
	if f.svc.Enabled() {
		t.Fatal("enabled")
	}
	a := store.Agent{ID: "a", TenantID: tenant}
	if _, err := f.svc.Create(ctx, f.req("k")); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, tenant, "x"); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := f.svc.Preview(ctx, tenant, nil, []string{"x"}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Verify(ctx, tenant, nil, []string{"x"}, "www", ""); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, _, err := f.svc.MarkRevoked(ctx, tenant, Actor{}, cert1); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := f.svc.Fetch(ctx, a, "x"); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := f.svc.Report(ctx, a, Report{}); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := f.svc.Cancel(ctx, tenant, Actor{}, "x"); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if cmds, err := f.svc.OnConnect(ctx, a); cmds != nil || err != nil {
		t.Fatal("OnConnect while disabled")
	}
	if e, fl, err := f.svc.Sweep(ctx); e+fl != 0 || err != nil {
		t.Fatal("Sweep while disabled")
	}
	// Reads keep working.
	if _, _, _, err := f.svc.List(ctx, tenant, repo.CertItemFilter{}, listReq()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.HostCertificates(ctx, tenant, "h"); err != nil {
		t.Fatal(err)
	}
}

// TestCreateSelectionAndCapabilities (T031): ids ∪ tag matches,
// deduplicated; retired hosts only when named; unsupported per capability;
// unknown ids reported; online agents get one push with ids only.
func TestCreateSelectionAndCapabilities(t *testing.T) {
	f := newFix(t)
	web1, a1 := f.webHost(t, "web-1", true)
	web2, a2 := f.webHost(t, "web-2", false)                              // offline: pending
	old := f.host(t, "old", map[string]string{"role": "web"})             // online, no cert.v1
	f.agentFor(t, "old", "linux", "4.6.0", true, store.CapUpgradeV1)      //
	offOld := f.host(t, "off-old", map[string]string{"role": "web"})      // offline, no cert.v1: judged on connect
	f.agentFor(t, "off-old", "linux", "4.6.0", false)                     //
	win := f.host(t, "win", map[string]string{"role": "web"})             //
	f.agentFor(t, "win", "windows", "4.7.0", true, store.CapCertV1)       //
	bare := f.host(t, "bare", map[string]string{"role": "web"})           // no agent
	ret := f.host(t, "retired", map[string]string{"role": "web"})         //
	f.agentFor(t, "retired", "linux", "4.7.0", true, store.CapCertV1)     //
	_ = f.mem.RetireHost(ctx, tenant, ret.ID)                             //
	f.host(t, "db-1", map[string]string{"role": "db"})                    // not selected
	f.host(t, "web-env", map[string]string{"role": "web", "env": "prod"}) // selected only by env tag below
	unknown := "0190f7c2-6a3e-7c1a-9b2e-000000000001"

	r := f.req("job-1", strings.ToUpper(web1.ID), web1.ID, unknown)
	r.HostTags = []string{"role=web"}
	v, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Created || v.Name != "www" || v.CertificateID != cert1 || v.KeyPolicy != store.KeyPolicyRequire ||
		len(v.UnknownHostIDs) != 1 || v.UnknownHostIDs[0] != unknown {
		t.Fatalf("view = %+v", v)
	}
	if len(v.Items) != 7 { // web-env matches role=web too; the retired host does not
		t.Fatalf("items = %d (%+v)", len(v.Items), v.Items)
	}
	want := map[string][2]string{
		web1.ID:   {store.DeliveryDelivered, ""},
		web2.ID:   {store.DeliveryPending, ""},
		old.ID:    {store.DeliveryUnsupported, store.ReasonNoCapability},
		offOld.ID: {store.DeliveryPending, ""},
		win.ID:    {store.DeliveryUnsupported, store.ReasonPlatform},
		bare.ID:   {store.DeliveryUnsupported, store.ReasonNoAgent},
	}
	for id, w := range want {
		i := itemFor(v, id)
		if i.State != w[0] || i.Reason != w[1] {
			t.Errorf("host %s (%s): %s/%s want %v", id, i.Hostname, i.State, i.Reason, w)
		}
	}
	if i := itemFor(v, web1.ID); !i.AgentOnline || i.Hostname != "web-1" || i.AgentID != a1.ID || i.DeliveredAt == nil || i.Attempts != 1 {
		t.Fatalf("web-1 item = %+v", i)
	}
	if i := itemFor(v, web2.ID); i.AgentOnline || i.AgentID != a2.ID {
		t.Fatalf("web-2 item = %+v", i)
	}
	if itemFor(v, ret.ID).ID != "" {
		t.Fatal("retired host selected by tag")
	}
	// One push, ids only.
	if len(f.reg.delivered) != 1 || f.reg.to[0] != a1.ID {
		t.Fatalf("pushes = %+v", f.reg.delivered)
	}
	c := f.reg.delivered[0]
	if c.Type != registry.CommandCertificate || c.Certificate == nil || c.Certificate.ItemID != itemFor(v, web1.ID).ID ||
		c.Certificate.Name != "www" || c.ID != c.Certificate.ItemID || c.Upgrade != nil {
		t.Fatalf("command = %+v", c)
	}
	// lcm asked for metadata only.
	if len(f.lcm.calls) != 1 || f.lcm.calls[0] != tenant+"|cert-1|false" {
		t.Fatalf("lcm calls = %v", f.lcm.calls)
	}
	// Expiry is the TTL (the certificate expires later).
	if !v.ExpiresAt.Equal(t0.Add(168*time.Hour)) || !v.CreatedAt.Equal(t0) {
		t.Fatalf("expiry = %v", v.ExpiresAt)
	}
	// Audit: one request row, unsupported rows, one delivered row.
	if f.count("cert_delivery_requested") != 1 || f.count("cert_delivery_unsupported") != 4 || f.count("cert_delivery_delivered") != 1 {
		t.Fatalf("audit = %v", f.actions())
	}
	req := f.mem.AuditRows()[0]
	if req.ActorKind != "service" || req.ActorID != "deployer" || req.SubjectKind != "cert_delivery" || req.SubjectID != v.ID ||
		req.Detail["hosts"] != 7 || req.Detail["unknown_hosts"] != 1 || req.Detail["idempotency_key"] != "job-1" ||
		req.Detail["configuration_id"] != "cfg-1" || req.Detail["trigger"] != "manual" || req.Detail["created"] != true {
		t.Fatalf("requested row = %+v", req)
	}
	// Explicitly named retired host -> unsupported/host_retired.
	r2 := f.req("job-2", ret.ID)
	r2.Name = "api"
	v2, err := f.svc.Create(ctx, r2)
	if err != nil || len(v2.Items) != 1 || v2.Items[0].State != store.DeliveryUnsupported || v2.Items[0].Reason != store.ReasonHostRetired {
		t.Fatalf("retired: %+v %v", v2, err)
	}
	// Tags must all match.
	r3 := f.req("job-3")
	r3.Name, r3.HostTags = "env", []string{"role=web", "env"}
	v3, err := f.svc.Create(ctx, r3)
	if err != nil || len(v3.Items) != 1 || v3.Items[0].Hostname != "web-env" {
		t.Fatalf("all tags: %+v %v", v3.Items, err)
	}
	// Metrics and events follow every committed transition.
	if f.met.transitions["delivered:"] != 1 || f.met.transitions["unsupported:no_agent"] != 3 || f.met.lcm["ok"] != 3 {
		t.Fatalf("metrics = %+v %+v", f.met.transitions, f.met.lcm)
	}
	for _, p := range f.pub.payloads {
		if p["type"] != "inventory.certificate.delivery" || len(p) != 4 {
			t.Fatalf("event = %+v", p)
		}
	}
}

func TestCreateIdempotentReplay(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", true)
	v1, err := f.svc.Create(ctx, f.req("job-1", web1.ID))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := f.svc.Create(ctx, f.req("job-1", web1.ID))
	if err != nil || v2.Created || v2.ID != v1.ID || len(v2.Items) != 1 || v2.Items[0].ID != v1.Items[0].ID {
		t.Fatalf("replay: %+v %v", v2, err)
	}
	if f.count("cert_delivery_requested") != 1 || len(f.reg.delivered) != 1 || len(f.lcm.calls) != 1 {
		t.Fatalf("replay wrote: %v", f.actions())
	}
	// A different source is another key space.
	r := f.req("job-1", web1.ID)
	r.Source = "other"
	if v3, err := f.svc.Create(ctx, r); err != nil || !v3.Created {
		t.Fatalf("other source: %+v %v", v3, err)
	}
}

func TestCreateLCMRefusals(t *testing.T) {
	for _, c := range []struct {
		err  error
		want error
		out  string
	}{
		{lcmclient.ErrNotFound, ErrCertificateNotFound, "not_found"},
		{lcmclient.ErrRevoked, ErrCertificateRevoked, "revoked"},
		{lcmclient.ErrExpired, ErrCertificateExpired, "expired"},
		{lcmclient.ErrUnavailable, ErrLCMUnavailable, "unavailable"},
		{lcmclient.ErrNoKey, ErrLCMUnavailable, "no_key"},
	} {
		f := newFix(t)
		web1, _ := f.webHost(t, "web-1", true)
		f.lcm.errs[cert1] = c.err
		if _, err := f.svc.Create(ctx, f.req("job-1", web1.ID)); !errors.Is(err, c.want) {
			t.Errorf("%v: %v", c.err, err)
		}
		if f.met.lcm[c.out] != 1 || len(f.mem.AuditRows()) != 0 {
			t.Errorf("%v: metrics %v audit %v", c.err, f.met.lcm, f.actions())
		}
	}
	// Unknown certificate of another tenant: not found.
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", true)
	r := f.req("job-1", web1.ID)
	r.CertificateID = "cert-x"
	if _, err := f.svc.Create(ctx, r); !errors.Is(err, ErrCertificateNotFound) {
		t.Fatal(err)
	}
}

func TestCreateExpiryCappedByCertificate(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", false)
	na := t0.Add(24 * time.Hour)
	b := f.lcm.certs[cert1]
	b.NotAfter = na
	f.lcm.certs[cert1] = b
	v, err := f.svc.Create(ctx, f.req("job-1", web1.ID))
	if err != nil || !v.ExpiresAt.Equal(na) || v.Items[0].NotAfter == nil || !v.Items[0].NotAfter.Equal(na) {
		t.Fatalf("expiry: %+v %v", v, err)
	}
	// No notAfter from lcm: TTL only, item without one.
	b.NotAfter = time.Time{}
	f.lcm.certs[cert1] = b
	v, err = f.svc.Create(ctx, f.req("job-2", web1.ID))
	if err != nil || !v.ExpiresAt.Equal(t0.Add(168*time.Hour)) || v.Items[0].NotAfter != nil {
		t.Fatalf("no notAfter: %+v %v", v, err)
	}
}

func TestCreateTooManyHostsAndEmpty(t *testing.T) {
	f := newFix(t)
	for i := 0; i < store.MaxDeliveryHosts+1; i++ {
		h, _ := f.mem.ResolveHost(ctx, tenant, store.Host{Hostname: "h" + itoa(i), MachineID: "m" + itoa(i)})
		_ = f.mem.SetHostTags(ctx, tenant, h.ID, map[string]string{"fleet": "x"})
	}
	r := f.req("job-1")
	r.HostTags = []string{"fleet"}
	if _, err := f.svc.Create(ctx, r); !errors.Is(err, ErrTooManyHosts) || !strings.Contains(err.Error(), "too_many_hosts") {
		t.Fatalf("too many: %v", err)
	}
	p, err := f.svc.Preview(ctx, tenant, nil, []string{"fleet"})
	if err != nil || !p.Truncated || len(p.Hosts) != store.MaxDeliveryHosts {
		t.Fatalf("preview truncation: %d %v %v", len(p.Hosts), p.Truncated, err)
	}
	if _, _, err := f.svc.Verify(ctx, tenant, nil, []string{"fleet"}, "www", strings.Repeat("a", 64)); !errors.Is(err, ErrTooManyHosts) {
		t.Fatalf("verify too many: %v", err)
	}
	// Nothing matched: an empty delivery (the provider fails the job).
	r = f.req("job-2")
	r.HostTags = []string{"role=none"}
	v, err := f.svc.Create(ctx, r)
	if err != nil || !v.Created || len(v.Items) != 0 {
		t.Fatalf("empty: %+v %v", v, err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{digits[i%10]}, b...)
	}
	return string(b)
}

// TestCreateConflictRetry: a concurrent create of the same key is replayed;
// a concurrent active item for the same host and name is retried once.
func TestCreateConflictRetry(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", true)
	// Same key created concurrently: the first insert conflicts, the
	// lookup then finds the other delivery.
	first, err := f.svc.Create(ctx, f.req("job-1", web1.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.repo.getByKey = []error{repo.ErrNotFound} // the pre-check misses it
	f.repo.createErrs = []error{repo.ErrConflict}
	v, err := f.svc.Create(ctx, f.req("job-1", web1.ID))
	if err != nil || v.Created || v.ID != first.ID {
		t.Fatalf("concurrent key: %+v %v", v, err)
	}
	// Host+name conflict: retried with a new build.
	f.repo.createErrs = []error{repo.ErrConflict}
	v, err = f.svc.Create(ctx, f.req("job-2", web1.ID))
	if err != nil || !v.Created {
		t.Fatalf("retry: %+v %v", v, err)
	}
	// A second conflict is returned.
	f.repo.createErrs = []error{repo.ErrConflict, repo.ErrConflict}
	if _, err := f.svc.Create(ctx, f.req("job-3", web1.ID)); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("double conflict: %v", err)
	}
	// Lookup errors.
	f.repo.getByKey = []error{errBoom}
	if _, err := f.svc.Create(ctx, f.req("job-4", web1.ID)); !errors.Is(err, errBoom) {
		t.Fatalf("lookup error: %v", err)
	}
	f.repo.getByKey = []error{repo.ErrNotFound, errBoom}
	f.repo.createErrs = []error{repo.ErrConflict}
	if _, err := f.svc.Create(ctx, f.req("job-5", web1.ID)); !errors.Is(err, errBoom) {
		t.Fatalf("lookup after conflict: %v", err)
	}
}

func TestCreateStoreErrors(t *testing.T) {
	for _, method := range []string{"ListHosts", "ListAgents", "ListHostCertificatesByName", "GetCertDelivery"} {
		f := newFix(t)
		web1, _ := f.webHost(t, "web-1", true)
		r := f.req("job-1", web1.ID)
		r.Trigger = store.TriggerAutoDeploy
		f.mem.FailNext(method)
		if _, err := f.svc.Create(ctx, r); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", true)
	f.reg.failList = errBoom
	if _, err := f.svc.Create(ctx, f.req("job-1", web1.ID)); !errors.Is(err, errBoom) {
		t.Fatalf("registry list: %v", err)
	}
	f.reg.failList = nil
	f.repo.createErrs = []error{errBoom}
	if _, err := f.svc.Create(ctx, f.req("job-1", web1.ID)); !errors.Is(err, errBoom) {
		t.Fatalf("insert: %v", err)
	}
	// A failing push leaves the item pending.
	f.reg.failPush = errBoom
	v, err := f.svc.Create(ctx, f.req("job-2", web1.ID))
	if err != nil || v.Items[0].State != store.DeliveryPending {
		t.Fatalf("push failure: %+v %v", v, err)
	}
}

func TestGet(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", true)
	unknown := "0190f7c2-6a3e-7c1a-9b2e-000000000001"
	v, _ := f.svc.Create(ctx, f.req("job-1", web1.ID, unknown))
	g, err := f.svc.Get(ctx, tenant, v.ID)
	if err != nil || g.Created || len(g.UnknownHostIDs) != 1 || g.UnknownHostIDs[0] != unknown || g.Items[0].Hostname != "web-1" {
		t.Fatalf("get: %+v %v", g, err)
	}
	for _, c := range []struct{ tenant, id string }{{tenant, "nope"}, {other, v.ID}, {tenant, "0190f7c2-6a3e-7c1a-9b2e-000000000009"}} {
		if _, err := f.svc.Get(ctx, c.tenant, c.id); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("get %v: %v", c, err)
		}
	}
	f.mem.FailNext("ListHosts")
	if _, err := f.svc.Get(ctx, tenant, v.ID); err == nil {
		t.Fatal("hosts error")
	}
	f.reg.failList = errBoom
	if _, err := f.svc.Get(ctx, tenant, v.ID); err == nil {
		t.Fatal("registry error")
	}
}

func TestCapability(t *testing.T) {
	cases := []struct {
		enabled bool
		a       *store.Agent
		want    string
	}{
		{false, &store.Agent{}, CapabilityDisabledOnServer},
		{true, nil, CapabilityNoAgent},
		{true, &store.Agent{OS: "windows", Capabilities: []string{store.CapCertV1}}, CapabilityNotSupported},
		{true, &store.Agent{OS: "linux", Capabilities: []string{store.CapCertV1}}, CapabilityEnabled},
		{true, &store.Agent{OS: "linux", AgentVersion: "4.7.0"}, CapabilityDisabledOnHost},
		{true, &store.Agent{OS: "linux", AgentVersion: "4.8.1"}, CapabilityDisabledOnHost},
		{true, &store.Agent{OS: "linux", AgentVersion: "4.6.3"}, CapabilityUpgradeRequired},
		{true, &store.Agent{OS: "linux", AgentVersion: "dev"}, CapabilityUpgradeRequired},
	}
	for _, c := range cases {
		if got := Capability(c.enabled, c.a); got != c.want {
			t.Errorf("%+v: %s want %s", c.a, got, c.want)
		}
	}
}

func TestPreview(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", true)
	bare := f.host(t, "bare", map[string]string{"role": "web"})
	unknown := "0190f7c2-6a3e-7c1a-9b2e-000000000001"
	p, err := f.svc.Preview(ctx, tenant, []string{unknown}, []string{"role"})
	if err != nil || p.Truncated || len(p.Hosts) != 2 || len(p.UnknownHostIDs) != 1 {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if p.Hosts[0].HostID != bare.ID || p.Hosts[0].Capability != CapabilityNoAgent || p.Hosts[0].AgentOnline {
		t.Fatalf("bare = %+v", p.Hosts[0])
	}
	if p.Hosts[1].HostID != web1.ID || p.Hosts[1].Capability != CapabilityEnabled || !p.Hosts[1].AgentOnline || p.Hosts[1].Tags["role"] != "web" {
		t.Fatalf("web = %+v", p.Hosts[1])
	}
	if len(f.mem.AuditRows()) != 0 || len(f.reg.delivered) != 0 {
		t.Fatal("preview wrote")
	}
	if _, err := f.svc.Preview(ctx, tenant, nil, nil); err == nil {
		t.Fatal("empty selector")
	}
	f.mem.FailNext("ListAgents")
	if _, err := f.svc.Preview(ctx, tenant, nil, []string{"role"}); err == nil {
		t.Fatal("store error")
	}
	// The most recently seen agent of a host wins.
	newer := store.Agent{ID: store.NewID(), TenantID: tenant, HostID: web1.ID, AgentVersion: "4.6.0", EnrolledAt: t0, LastSeen: t0.Add(time.Hour)}
	_ = f.mem.CreateAgent(ctx, newer)
	p, _ = f.svc.Preview(ctx, tenant, []string{web1.ID}, nil)
	if p.Hosts[0].Capability != CapabilityUpgradeRequired {
		t.Fatalf("newest agent: %+v", p.Hosts[0])
	}
}
