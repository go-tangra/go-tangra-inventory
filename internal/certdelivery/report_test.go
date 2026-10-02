package certdelivery

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// fetched creates a delivery to web-1 and fetches it.
func (f *fix) fetched(t *testing.T, key string) (store.CertDeliveryItem, store.Agent) {
	t.Helper()
	it, a := f.delivered(t, key)
	m, err := f.svc.Fetch(ctx, a, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Wipe()
	return f.item(t, it.ID), a
}

func TestReportValidation(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	ok := Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, Serial: it.Serial, HookExitCode: -1}
	for field, mut := range map[string]func(r *Report){
		"item_id":            func(r *Report) { r.ItemID = "x" },
		"state":              func(r *Report) { r.State = store.DeliveryPending },
		"reason":             func(r *Report) { r.Reason = store.ReasonKeyUnavailable }, // a server reason
		"fingerprint_sha256": func(r *Report) { r.Fingerprint = strings.ToUpper(it.FingerprintSHA256) },
		"serial":             func(r *Report) { r.Serial = "zz" },
		"hook_exit_code":     func(r *Report) { r.HookExitCode = 257 },
		"hook_exit_code ":    func(r *Report) { r.HookExitCode = -2 },
	} {
		r := ok
		mut(&r)
		_, err := f.svc.Report(ctx, a, r)
		var ie *InvalidError
		if !errors.As(err, &ie) || ie.Field != strings.TrimSpace(field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	if g := f.item(t, it.ID); g.State != store.DeliveryFetched {
		t.Fatal("invalid report changed the item")
	}
}

// TestReportInstalledAndRenewal (T032): installed upserts the host
// certificate; a later delivery of another certificate is a renewal.
func TestReportInstalledAndRenewal(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	accepted, err := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256,
		Serial: it.Serial, HookExitCode: 0, Detail: "ok\nfine"})
	if !accepted || err != nil {
		t.Fatal(accepted, err)
	}
	g := f.item(t, it.ID)
	if g.State != store.DeliveryInstalled || g.Reason != "" || g.HookExitCode == nil || *g.HookExitCode != 0 || g.Detail != "ok fine" || g.FinishedAt == nil {
		t.Fatalf("item = %+v", g)
	}
	hc, err := f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www")
	if err != nil || hc.CertificateID != cert1 || hc.ConfigurationID != "cfg-1" || hc.FingerprintSHA256 != it.FingerprintSHA256 ||
		hc.Serial != it.Serial || hc.State != store.DeliveryInstalled || hc.LastItemID != it.ID || hc.LastDeliveredAt == nil ||
		hc.NotAfter == nil || hc.RevokedAt != nil {
		t.Fatalf("host certificate = %+v %v", hc, err)
	}
	row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	if row.Action != "cert_delivery_installed" || row.Detail["is_renewal"] != false || row.Detail["hook_exit_code"] != 0 {
		t.Fatalf("installed row = %+v", row)
	}
	// Reports for terminal items are ignored; an identical repeat is accepted
	// without a change (the agent lost the answer).
	if accepted, err := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryFailed, HookExitCode: -1}); accepted || err != nil {
		t.Fatal("terminal report accepted", err)
	}
	n := len(f.mem.AuditRows())
	if accepted, err := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256,
		Serial: it.Serial, HookExitCode: 0}); !accepted || err != nil || len(f.mem.AuditRows()) != n {
		t.Fatal("repeated report", accepted, err)
	}
	if accepted, _ := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256,
		Reason: store.ReasonBusy, HookExitCode: 0}); accepted {
		t.Fatal("repeat with another reason accepted")
	}
	// Renewal: a different certificate under the same name.
	f.lcm.certs["cert-2"] = bundle(t, "ecdsa.crt", "ecdsa.pkcs8.key", hc.NotAfter.AddDate(1, 0, 0))
	r := f.req("job-2", it.HostID)
	r.CertificateID = "cert-2"
	v, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.Fetch(ctx, a, v.Items[0].ID)
	if err != nil || !m.IsRenewal {
		t.Fatalf("renewal fetch: %+v %v", m, err)
	}
	fp := m.Bundle.Fingerprint
	m.Wipe()
	if ok, err := f.svc.Report(ctx, a, Report{ItemID: v.Items[0].ID, State: store.DeliveryInstalled, Fingerprint: fp, HookExitCode: -1}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	row = f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	hc2, _ := f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www")
	if row.Detail["is_renewal"] != true || hc2.CertificateID != "cert-2" || hc2.FingerprintSHA256 != fp || hc2.LastItemID != v.Items[0].ID {
		t.Fatalf("renewal: %+v %+v", row, hc2)
	}
}

func TestReportUnchangedMismatchAndFailures(t *testing.T) {
	// unchanged with the served fingerprint.
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryUnchanged, Fingerprint: it.FingerprintSHA256, HookExitCode: -1}); !ok {
		t.Fatal("unchanged refused")
	}
	if hc, _ := f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www"); hc.State != store.DeliveryUnchanged || hc.LastDeliveredAt == nil {
		t.Fatalf("unchanged host certificate = %+v", hc)
	}
	// installed with another fingerprint -> failed/fingerprint_mismatch.
	f = newFix(t)
	it, a = f.fetched(t, "job-1")
	ok, err := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: strings.Repeat("0", 64), HookExitCode: -1})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	g := f.item(t, it.ID)
	if g.State != store.DeliveryFailed || g.Reason != store.ReasonFingerprintMismatch {
		t.Fatalf("mismatch item = %+v", g)
	}
	row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]
	if row.Action != "cert_delivery_failed" || row.Reason != store.ReasonFingerprintMismatch || row.Detail["reported_fingerprint_sha256"] != strings.Repeat("0", 64) {
		t.Fatalf("mismatch row = %+v", row)
	}
	hc, _ := f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www")
	if hc.State != store.DeliveryFailed || hc.FingerprintSHA256 != "" || hc.LastDeliveredAt != nil || hc.CertificateID != cert1 {
		t.Fatalf("mismatch host certificate = %+v", hc)
	}
	// installed before any fetch -> mismatch too.
	f = newFix(t)
	it, a = f.delivered(t, "job-1")
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: strings.Repeat("a", 64), HookExitCode: -1}); !ok ||
		f.item(t, it.ID).Reason != store.ReasonFingerprintMismatch {
		t.Fatal("unfetched installed report")
	}
	// failed before fetch (disabled locally) is accepted; previous identity kept.
	f = newFix(t)
	it, a = f.fetched(t, "job-1")
	_, _ = f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: -1})
	it2, _ := f.delivered(t, "job-2")
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: it2.ID, State: store.DeliveryFailed, Reason: store.ReasonDisabledLocally, HookExitCode: -1}); !ok {
		t.Fatal("failed refused")
	}
	hc, _ = f.mem.GetHostCertificate(ctx, tenant, it.HostID, "www")
	if hc.State != store.DeliveryFailed || hc.Reason != store.ReasonDisabledLocally || hc.FingerprintSHA256 != it.FingerprintSHA256 || hc.LastItemID != it2.ID {
		t.Fatalf("failed keeps identity: %+v", hc)
	}
	// hook_failed without a reason gets hook_failed; exit code kept.
	it3, _ := f.fetched(t, "job-3")
	if ok, _ := f.svc.Report(ctx, a, Report{ItemID: it3.ID, State: store.DeliveryHookFailed, Fingerprint: it3.FingerprintSHA256, HookExitCode: 3}); !ok {
		t.Fatal("hook_failed refused")
	}
	g = f.item(t, it3.ID)
	if g.State != store.DeliveryHookFailed || g.Reason != store.ReasonHookFailed || *g.HookExitCode != 3 {
		t.Fatalf("hook_failed = %+v", g)
	}
	if row := f.mem.AuditRows()[len(f.mem.AuditRows())-1]; row.Action != "cert_delivery_hook_failed" || row.Outcome != "error" {
		t.Fatalf("hook_failed row = %+v", row)
	}
}

func TestReportForeignAndErrors(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	other := f.agentFor(t, "web-1", "linux", "4.7.0", true, store.CapCertV1)
	r := Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: -1}
	if _, err := f.svc.Report(ctx, other, r); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("foreign: %v", err)
	}
	for _, method := range []string{"GetCertItem", "GetCertDelivery", "GetHostCertificate", "UpdateCertItem"} {
		f.mem.FailNext(method)
		if ok, err := f.svc.Report(ctx, a, r); ok || err == nil {
			t.Errorf("%s: %v %v", method, ok, err)
		}
	}
	if g := f.item(t, it.ID); g.State != store.DeliveryFetched {
		t.Fatalf("item changed: %+v", g)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain":                         "plain",
		"tab\there":                     "tab here",
		"bad\xffutf8":                   "bad?utf8",
		"c1\u0085x":                     "c1 x",
		"-----BEGIN PRIVATE KEY-----\n": "",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 200) // 400 bytes
	if got := sanitize(long); len(got) > store.MaxDetailBytes || !strings.HasPrefix(long, got) || len(got) != 256 {
		t.Fatalf("truncation: %d", len(got))
	}
}
