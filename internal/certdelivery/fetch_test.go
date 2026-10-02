package certdelivery

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// delivered creates a manual delivery of cert1 to web-1 (online agent) and
// returns the item and agent.
func (f *fix) delivered(t *testing.T, key string) (store.CertDeliveryItem, store.Agent) {
	t.Helper()
	h, ok := f.hosts["web-1"]
	if !ok {
		h, _ = f.webHost(t, "web-1", true)
	}
	v, err := f.svc.Create(ctx, f.req(key, h.ID))
	if err != nil {
		t.Fatal(err)
	}
	return v.Items[0].CertDeliveryItem, f.agent["web-1"]
}

func rsaFingerprint(t *testing.T) string {
	t.Helper()
	b, err := certmaterial.ParseBundle([]byte(fixture(t, "rsa.crt")), nil, nil, certmaterial.Options{Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	return b.Fingerprint
}

// TestFetchOwnItem (T032): the bundle comes from lcm, the item becomes
// fetched, fetches+1, audit without material; Wipe zeroes the key.
func TestFetchOwnItem(t *testing.T) {
	f := newFix(t)
	it, a := f.delivered(t, "job-1")
	m, err := f.svc.Fetch(ctx, a, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.ItemID != it.ID || m.Name != "www" || m.CertificateID != cert1 || !m.Bundle.HasKey || m.IsRenewal || m.RerunHook ||
		m.Bundle.Fingerprint != rsaFingerprint(t) || m.Bundle.CommonName != "www.example.com" || len(m.Bundle.Chain) != 2 {
		t.Fatalf("material = %+v", m)
	}
	if f.lcm.calls[len(f.lcm.calls)-1] != tenant+"|cert-1|true" {
		t.Fatalf("lcm call = %v", f.lcm.calls)
	}
	g := f.item(t, it.ID)
	if g.State != store.DeliveryFetched || g.Fetches != 1 || g.FetchedAt == nil || g.Serial != m.Bundle.Serial ||
		g.FingerprintSHA256 != m.Bundle.Fingerprint || !g.NotAfter.Equal(m.Bundle.NotAfter) {
		t.Fatalf("item = %+v", g)
	}
	row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	if row.Action != "cert_delivery_fetched" || row.ActorKind != "agent" || row.ActorID != a.ID || row.Detail["has_key"] != true ||
		row.Detail["fetches"] != 1 || row.Detail["serial"] != g.Serial {
		t.Fatalf("fetched row = %+v", row)
	}
	key := f.lcm.served[0]
	if !bytes.Contains(m.Bundle.KeyPEM, []byte("PRIVATE KEY")) {
		t.Fatal("no key in the material")
	}
	m.Wipe()
	if bytes.ContainsFunc(key, func(r rune) bool { return r != 0 }) {
		t.Fatal("key bytes not zeroed")
	}
	// Re-fetch (crash recovery) is allowed while fetched.
	if _, err := f.svc.Fetch(ctx, a, it.ID); err != nil || f.item(t, it.ID).Fetches != 2 {
		t.Fatalf("refetch: %v", err)
	}
}

func TestFetchRefusals(t *testing.T) {
	f := newFix(t)
	it, a := f.delivered(t, "job-1")
	other := f.agentFor(t, "web-1", "linux", "4.7.0", true, store.CapCertV1) // second agent of the same host
	foreign := store.Agent{ID: a.ID, TenantID: "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"}
	if _, err := f.svc.Fetch(ctx, a, "not-a-uuid"); err == nil {
		t.Fatal("bad id")
	}
	for name, c := range map[string]struct {
		a  store.Agent
		id string
	}{
		"other agent":  {other, it.ID},
		"other tenant": {foreign, it.ID},
		"missing":      {a, "0190f7c2-6a3e-7c1a-9b2e-000000000009"},
	} {
		if _, err := f.svc.Fetch(ctx, c.a, c.id); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.count("cert_delivery_refused") != 3 || f.met.refused[RefusedNotFound] != 3 {
		t.Fatalf("refusals = %v %v", f.actions(), f.met.refused)
	}
	// Fetch limit.
	for n := 0; n < store.MaxItemFetches; n++ {
		if _, err := f.svc.Fetch(ctx, a, it.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedFetchLimit] != 1 {
		t.Fatalf("fetch limit: %v", err)
	}
	// Terminal item.
	ok, err := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: rsaFingerprint(t), HookExitCode: -1})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedNotActive] != 1 {
		t.Fatalf("terminal: %v", err)
	}
	// Refusals are throttled per agent and reason.
	before := f.count("cert_delivery_refused")
	_, _ = f.svc.Fetch(ctx, a, it.ID)
	if f.count("cert_delivery_refused") != before {
		t.Fatal("refusal not throttled")
	}
	f.now = f.now.Add(11 * time.Second)
	_, _ = f.svc.Fetch(ctx, a, it.ID)
	if f.count("cert_delivery_refused") != before+1 {
		t.Fatal("refusal after the window not audited")
	}
	f.svc.RefusePlaintext(ctx, a, it.ID)
	f.svc.Refuse(ctx, tenant, Actor{Kind: "service", ID: "ipam"}, RefusedSourceNotAllowed)
	if f.met.refused[RefusedPlaintext] != 1 || f.met.refused[RefusedSourceNotAllowed] != 1 {
		t.Fatalf("refused = %v", f.met.refused)
	}
	// Suppressed refusals are counted on the next audited row; an
	// attacker-chosen subject that is not a uuid is not recorded.
	f.now = f.now.Add(11 * time.Second)
	f.svc.RefusePlaintext(ctx, a, strings.Repeat("x", 4096))
	f.svc.RefusePlaintext(ctx, a, it.ID)
	f.svc.RefusePlaintext(ctx, a, it.ID)
	rows := f.mem.AuditRows()
	if r := rows[len(rows)-1]; r.SubjectID != "" || r.Reason != RefusedPlaintext {
		t.Fatalf("non-uuid subject audited: %+v", r)
	}
	f.now = f.now.Add(11 * time.Second)
	f.svc.RefusePlaintext(ctx, a, it.ID)
	rows = f.mem.AuditRows()
	if r := rows[len(rows)-1]; r.SubjectID != it.ID || r.Detail["suppressed"] != 2 {
		t.Fatalf("suppressed count: %+v", r)
	}
	// Long-idle throttle entries are pruned.
	f.now = f.now.Add(time.Hour)
	f.svc.Refuse(ctx, tenant, Actor{Kind: "service", ID: "ipam"}, RefusedSourceNotAllowed)
	if len(f.svc.refused) != 1 {
		t.Fatalf("throttle entries = %d", len(f.svc.refused))
	}
}

// TestFetchAfterDeliveryWindow: an item whose delivery expired is not served
// even before the sweeper ran.
func TestFetchAfterDeliveryWindow(t *testing.T) {
	f := newFix(t)
	it, a := f.delivered(t, "job-1")
	f.now = f.now.Add(169 * time.Hour)
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedNotActive] != 1 {
		t.Fatalf("expired delivery served: %v", err)
	}
	if len(f.lcm.calls) != 1 { // the create's metadata call only
		t.Fatalf("lcm calls = %v", f.lcm.calls)
	}
}

func TestFetchKeyPolicies(t *testing.T) {
	// require + lcm without a key -> failed/key_unavailable.
	f := newFix(t)
	it, a := f.delivered(t, "job-1")
	f.lcm.errs[cert1] = lcmclient.ErrNoKey
	_, err := f.svc.Fetch(ctx, a, it.ID)
	var fe *ItemFailedError
	if !errors.As(err, &fe) || fe.Reason != store.ReasonKeyUnavailable || !strings.Contains(err.Error(), "key_unavailable") {
		t.Fatalf("no key: %v", err)
	}
	if g := f.item(t, it.ID); g.State != store.DeliveryFailed || g.Reason != store.ReasonKeyUnavailable || g.FinishedAt == nil {
		t.Fatalf("item = %+v", g)
	}
	// lcm answering without a key although asked: the same.
	f = newFix(t)
	it, a = f.delivered(t, "job-1")
	f.lcm.noKeyOK = true
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.As(err, &fe) || fe.Reason != store.ReasonKeyUnavailable {
		t.Fatalf("empty key: %v", err)
	}
	// certificate_only -> include_key=false, no key.
	f = newFix(t)
	h, a := f.webHost(t, "web-1", true)
	r := f.req("job-1", h.ID)
	r.KeyPolicy = store.KeyPolicyCertificateOnly
	v, _ := f.svc.Create(ctx, r)
	m, err := f.svc.Fetch(ctx, a, v.Items[0].ID)
	if err != nil || m.Bundle.HasKey || len(m.Bundle.KeyPEM) != 0 || f.lcm.calls[1] != tenant+"|cert-1|false" {
		t.Fatalf("certificate_only: %+v %v %v", m, err, f.lcm.calls)
	}
}

func TestFetchLCMFailures(t *testing.T) {
	for _, c := range []struct {
		err    error
		reason string
	}{
		{lcmclient.ErrNotFound, store.ReasonCertificateNotFound},
		{lcmclient.ErrRevoked, store.ReasonCertificateRevoked},
		{lcmclient.ErrExpired, store.ReasonCertificateExpired},
	} {
		f := newFix(t)
		it, a := f.delivered(t, "job-1")
		f.lcm.errs[cert1] = c.err
		_, err := f.svc.Fetch(ctx, a, it.ID)
		var fe *ItemFailedError
		if !errors.As(err, &fe) || fe.Reason != c.reason {
			t.Errorf("%v: %v", c.err, err)
		}
		if f.count("cert_delivery_failed") != 1 {
			t.Errorf("%v: audit %v", c.err, f.actions())
		}
	}
	// Unavailable: the item stays as it is.
	f := newFix(t)
	it, a := f.delivered(t, "job-1")
	f.lcm.errs[cert1] = lcmclient.ErrUnavailable
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.Is(err, ErrLCMUnavailable) {
		t.Fatal(err)
	}
	if g := f.item(t, it.ID); g.State != store.DeliveryDelivered || g.Fetches != 0 {
		t.Fatalf("item changed: %+v", g)
	}
}

func TestFetchInvalidMaterial(t *testing.T) {
	for _, c := range []struct {
		name   string
		mut    func(b *lcmclient.Bundle)
		reason string
	}{
		{"garbage", func(b *lcmclient.Bundle) { b.CertPEM = "not pem" }, store.ReasonInvalidBundle},
		{"oversized", func(b *lcmclient.Bundle) { b.ChainPEM = strings.Repeat(b.ChainPEM, 300) }, store.ReasonBundleTooLarge},
		{"mismatch", func(b *lcmclient.Bundle) { b.KeyPEM = []byte(fixture(t, "mismatch.key")) }, store.ReasonKeyMismatch},
	} {
		f := newFix(t)
		it, a := f.delivered(t, "job-1")
		b := f.lcm.certs[cert1]
		c.mut(&b)
		f.lcm.certs[cert1] = b
		_, err := f.svc.Fetch(ctx, a, it.ID)
		var fe *ItemFailedError
		if !errors.As(err, &fe) || fe.Reason != c.reason {
			t.Errorf("%s: %v", c.name, err)
		}
		if g := f.item(t, it.ID); g.State != store.DeliveryFailed || g.Reason != c.reason {
			t.Errorf("%s: item %+v", c.name, g)
		}
		for _, k := range f.lcm.served {
			if bytes.ContainsFunc(k, func(r rune) bool { return r != 0 }) {
				t.Errorf("%s: key not wiped", c.name)
			}
		}
	}
}

func TestFetchStoreErrorsAndRaces(t *testing.T) {
	for _, method := range []string{"GetCertItem", "GetCertDelivery", "GetHostCertificate", "UpdateCertItem"} {
		f := newFix(t)
		it, a := f.delivered(t, "job-1")
		f.mem.FailNext(method)
		if _, err := f.svc.Fetch(ctx, a, it.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", method, err)
		}
	}
	// The item turns terminal between the check and the update.
	f := newFix(t)
	it, a := f.delivered(t, "job-1")
	stale := f.item(t, it.ID)
	if _, err := f.svc.Cancel(ctx, tenant, Actor{Kind: "user", ID: "u1"}, it.ID); err != nil {
		t.Fatal(err)
	}
	f.svc.repo = &raceRepo{faultRepo: f.repo, item: stale}
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("race: %v", err)
	}
	// Failing a raced item: not found.
	f.lcm.errs[cert1] = lcmclient.ErrRevoked
	if _, err := f.svc.Fetch(ctx, a, it.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("fail race: %v", err)
	}
	// Failing the item can fail.
	f = newFix(t)
	it, a = f.delivered(t, "job-1")
	f.lcm.errs[cert1] = lcmclient.ErrRevoked
	f.mem.FailNext("UpdateCertItem")
	if _, err := f.svc.Fetch(ctx, a, it.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("fail error: %v", err)
	}
}

// raceRepo returns a stale, still active copy of item from GetCertItem.
type raceRepo struct {
	*faultRepo
	item store.CertDeliveryItem
}

func (r *raceRepo) GetCertItem(context.Context, string, string) (store.CertDeliveryItem, error) {
	return r.item, nil
}
