package certdelivery

import (
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestNoMaterialOutsideTheFetch (SC-003, unit level): after a full
// delivery (create, push, fetch, report, renewal, revocation) no audit row,
// realtime event, registry command or stored item/host certificate carries
// PEM or the private key bytes. The integration suite repeats this against
// PostgreSQL, Valkey and the service logs.
func TestNoMaterialOutsideTheFetch(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	_, _ = f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: 0,
		Detail: "-----BEGIN PRIVATE KEY----- smuggled"})
	f.lcm.certs["cert-2"] = bundle(t, "ecdsa.crt", "ecdsa.pkcs8.key", t0.AddDate(3, 0, 0))
	r := f.req("job-2", it.HostID)
	r.CertificateID = "cert-2"
	v, _ := f.svc.Create(ctx, r)
	m, err := f.svc.Fetch(ctx, a, v.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Wipe()
	_, _, _ = f.svc.MarkRevoked(ctx, tenant, Actor{Kind: "service", ID: "deployer"}, "cert-2")

	var keyDER [][]byte
	for _, k := range []string{"rsa.pkcs8.key", "ecdsa.pkcs8.key"} {
		blk, _ := pem.Decode([]byte(fixture(t, k)))
		keyDER = append(keyDER, blk.Bytes)
	}
	var blobs []string
	for _, x := range []any{f.mem.AuditRows(), f.pub.payloads, f.reg.delivered} {
		b, _ := json.Marshal(x)
		blobs = append(blobs, string(b))
	}
	items, _, _, _ := f.mem.ListCertItemsPage(ctx, tenant, repoFilter(), listReq())
	hcs, _ := f.mem.ListHostCertificates(ctx, tenant, it.HostID)
	for _, x := range []any{items, hcs} {
		b, _ := json.Marshal(x)
		blobs = append(blobs, string(b))
	}
	for _, b := range blobs {
		if strings.Contains(b, "PRIVATE KEY") || strings.Contains(b, "-----BEGIN") {
			t.Fatalf("PEM found: %.200s", b)
		}
		for _, der := range keyDER {
			if strings.Contains(b, string(der)) {
				t.Fatal("key DER found")
			}
		}
	}
}
