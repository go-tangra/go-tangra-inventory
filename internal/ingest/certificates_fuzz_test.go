package ingest

import (
	"context"
	"testing"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzReportCertificate: arbitrary agent reports never panic, are either
// refused (InvalidArgument/NotFound) or recorded with a closed-set state and
// reason and a sanitised detail of at most 256 bytes without PEM.
func FuzzReportCertificate(f *testing.F) {
	f.Add("installed", "", "", "0a", int32(-1), "ok")
	f.Add("failed", "write_failed", "", "", int32(-1), "disk\x00full")
	f.Add("hook_failed", "hook_timeout", "", "", int32(256), "-----BEGIN PRIVATE KEY-----")
	f.Add("unchanged", "", "zz", "", int32(0), "\xff\xfe")
	f.Fuzz(func(t *testing.T, state, reason, fp, serial string, code int32, detail string) {
		h := newCertHarness(t, 1, false)
		ctx, _, hostID := h.enrollCertAgent(t, "web-1")
		itemID := h.deliver(t, "job-1", hostID)
		_, _ = h.srv.ReportCertificate(ctx, &inventoryv1.ReportCertificateRequest{ItemId: itemID, State: state, Reason: reason,
			FingerprintSha256: fp, Serial: serial, HookExitCode: code, Detail: detail})
		it, err := h.mem.GetCertItem(context.Background(), certTenant, itemID)
		if err != nil {
			t.Fatal(err)
		}
		known := false
		for _, s := range store.DeliveryStates {
			known = known || s == it.State
		}
		if !known || len(it.Detail) > store.MaxDetailBytes || len(it.Reason) > 32 {
			t.Fatalf("item = %+v", it)
		}
		for _, r := range it.Detail {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("control character kept: %q", it.Detail)
			}
		}
		if len(it.Detail) > 0 && contains(it.Detail, "-----BEGIN") {
			t.Fatal("PEM kept")
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
