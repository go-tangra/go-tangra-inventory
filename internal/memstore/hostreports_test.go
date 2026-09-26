package memstore

import (
	"context"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestHostReportRows(t *testing.T) {
	m := New()
	ctx := context.Background()
	const tA, tB = "tenant-a", "tenant-b"
	mk := func(tenant, name string) store.Host {
		h, err := m.ResolveHost(ctx, tenant, store.Host{Hostname: name})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	a1, a2, a3 := mk(tA, "a1"), mk(tA, "a2"), mk(tA, "a3")
	b1 := mk(tB, "b1")
	_ = mk(tB, "b-legacy") // never reported after migration: changed_at zero
	t0 := time.UnixMilli(1_000).UTC()

	if changed, err := m.SetReportDigest(ctx, tA, a1.ID, "d1", t0); err != nil || !changed {
		t.Fatalf("set: %v %v", changed, err)
	}
	if changed, _ := m.SetReportDigest(ctx, tA, a1.ID, "d1", t0.Add(time.Second)); changed {
		t.Fatal("same digest must not bump")
	}
	if _, err := m.SetReportDigest(ctx, tB, a1.ID, "x", t0); err != repo.ErrNotFound {
		t.Fatalf("cross-tenant set: %v", err)
	}
	_, _ = m.SetReportDigest(ctx, tA, a2.ID, "d2", t0.Add(2*time.Second))
	_, _ = m.SetReportDigest(ctx, tA, a3.ID, "d3", t0.Add(2*time.Second))
	_, _ = m.SetReportDigest(ctx, tB, b1.ID, "d4", t0.Add(3*time.Second))

	ids, maxAt, err := m.ListReportTenants(ctx, time.Time{}, 0)
	if err != nil || len(ids) != 2 || !maxAt.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("all tenants = %v %v %v", ids, maxAt, err)
	}
	ids, maxAt, _ = m.ListReportTenants(ctx, t0.Add(2*time.Second), 0)
	if len(ids) != 1 || ids[0] != tB || !maxAt.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("since = %v %v", ids, maxAt)
	}
	ids, maxAt, _ = m.ListReportTenants(ctx, t0.Add(time.Hour), 0)
	if len(ids) != 0 || !maxAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("nothing changed: %v %v (watermark must not regress)", ids, maxAt)
	}
	if ids, _, _ = m.ListReportTenants(ctx, time.Time{}, 1); len(ids) != 1 {
		t.Fatalf("limit: %v", ids)
	}

	// Tenant A, ordered by (changed_at, id), keyset paged.
	rows, err := m.ListHostReportRows(ctx, tA, store.ReportRowFilter{Limit: 2})
	if err != nil || len(rows) != 2 || rows[0].ID != a1.ID {
		t.Fatalf("page 1 = %v %v", rows, err)
	}
	last := rows[1]
	rows, _ = m.ListHostReportRows(ctx, tA, store.ReportRowFilter{Limit: 2, After: &store.ReportCursor{ChangedAt: last.ReportChangedAt, ID: last.ID}})
	if len(rows) != 1 || rows[0].ID == last.ID || rows[0].ID == a1.ID {
		t.Fatalf("page 2 = %v", rows)
	}
	rows, _ = m.ListHostReportRows(ctx, tA, store.ReportRowFilter{ChangedSince: t0})
	if len(rows) != 2 {
		t.Fatalf("since = %d", len(rows))
	}
	// Legacy host (zero changed_at) only appears in a full listing, first.
	rows, _ = m.ListHostReportRows(ctx, tB, store.ReportRowFilter{})
	if len(rows) != 2 || !rows[0].ReportChangedAt.IsZero() {
		t.Fatalf("tenant B = %v", rows)
	}
	if rows, _ = m.ListHostReportRows(ctx, tB, store.ReportRowFilter{ChangedSince: t0}); len(rows) != 1 {
		t.Fatalf("legacy host in a since listing: %v", rows)
	}

	// Retire invalidates the digest and bumps the change time.
	m.Now = func() time.Time { return t0.Add(time.Minute) }
	if err := m.RetireHost(ctx, tA, a1.ID); err != nil {
		t.Fatal(err)
	}
	h, _ := m.GetHost(ctx, tA, a1.ID)
	if h.ReportDigest != "" || !h.ReportChangedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("retired host = %q %v", h.ReportDigest, h.ReportChangedAt)
	}

	m.FailNext("ListReportTenants")
	if _, _, err := m.ListReportTenants(ctx, time.Time{}, 0); err == nil {
		t.Fatal("injected")
	}
	m.FailNext("ListHostReportRows")
	if _, err := m.ListHostReportRows(ctx, tA, store.ReportRowFilter{}); err == nil {
		t.Fatal("injected")
	}
}
