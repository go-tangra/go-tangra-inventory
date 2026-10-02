package certdelivery

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestHostStatusOrder(t *testing.T) {
	fp := strings.Repeat("a", 64)
	at := t0
	cases := []struct {
		hc     store.HostCertificate
		has    bool
		active bool
		want   string
	}{
		{store.HostCertificate{FingerprintSHA256: fp, State: store.DeliveryInstalled}, true, true, StatusMatch},
		{store.HostCertificate{FingerprintSHA256: fp, State: store.DeliveryUnchanged}, true, false, StatusMatch},
		{store.HostCertificate{FingerprintSHA256: fp, State: store.DeliveryInstalled, RevokedAt: &at}, true, false, StatusRevoked},
		{store.HostCertificate{FingerprintSHA256: "b", State: store.DeliveryInstalled}, true, true, StatusPending},
		{store.HostCertificate{}, false, true, StatusPending},
		{store.HostCertificate{FingerprintSHA256: fp, State: store.DeliveryFailed}, true, false, StatusFailed},
		{store.HostCertificate{FingerprintSHA256: fp, State: store.DeliveryHookFailed}, true, false, StatusFailed},
		{store.HostCertificate{FingerprintSHA256: "b", State: store.DeliveryInstalled}, true, false, StatusMismatch},
		{store.HostCertificate{State: store.DeliveryCancelled}, true, false, StatusMissing},
		{store.HostCertificate{}, false, false, StatusMissing},
	}
	for k, c := range cases {
		if got := hostStatus(c.hc, c.has, c.active, fp); got != c.want {
			t.Errorf("case %d: %s want %s", k, got, c.want)
		}
	}
}

// TestVerify (T033): match/mismatch/pending/missing over a selection.
func TestVerify(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	_, _ = f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: -1})
	web2, _ := f.webHost(t, "web-2", false)
	_, _ = f.svc.Create(ctx, f.req("job-2", web2.ID))
	f.host(t, "web-3", map[string]string{"role": "web"})
	hosts, matched, err := f.svc.Verify(ctx, tenant, nil, []string{"role=web"}, "www", it.FingerprintSHA256)
	if err != nil || matched != 1 || len(hosts) != 3 {
		t.Fatalf("verify: %+v %d %v", hosts, matched, err)
	}
	got := map[string]string{}
	for _, h := range hosts {
		got[h.Hostname] = h.Status
	}
	if got["web-1"] != StatusMatch || got["web-2"] != StatusPending || got["web-3"] != StatusMissing {
		t.Fatalf("statuses = %v", got)
	}
	if hosts[0].Fingerprint != it.FingerprintSHA256 || hosts[0].Serial != it.Serial || hosts[0].LastDeliveredAt == nil {
		t.Fatalf("web-1 = %+v", hosts[0])
	}
	_, matched, _ = f.svc.Verify(ctx, tenant, nil, []string{"role=web"}, "www", strings.Repeat("c", 64))
	if matched != 0 {
		t.Fatal("other fingerprint matched")
	}
	// Validation and store errors.
	for _, c := range []struct {
		ids, tags []string
		name, fp  string
	}{
		{nil, nil, "www", it.FingerprintSHA256},
		{nil, []string{"role"}, "../x", it.FingerprintSHA256},
		{nil, []string{"role"}, "www", "ABC"},
	} {
		var ie *InvalidError
		if _, _, err := f.svc.Verify(ctx, tenant, c.ids, c.tags, c.name, c.fp); !errors.As(err, &ie) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	for _, method := range []string{"ListHosts", "ListHostCertificatesByName", "ListActiveCertItemsByName"} {
		f.mem.FailNext(method)
		if _, _, err := f.svc.Verify(ctx, tenant, nil, []string{"role"}, "www", it.FingerprintSHA256); err == nil {
			t.Errorf("%s: no error", method)
		}
	}
}
