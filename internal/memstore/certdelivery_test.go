package memstore

import (
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestCertDeliveryContract runs the shared storage contract (also run by the
// repodb integration suite) against the in-memory store (T015).
func TestCertDeliveryContract(t *testing.T) {
	var cur *Mem
	open := func(t *testing.T) repo.Store {
		cur = New()
		cur.Now = func() time.Time { return time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC) }
		return cur
	}
	audits := func(t *testing.T, tenantID, action, subjectID string) int {
		n := 0
		for _, r := range cur.AuditRows() {
			if r.TenantID == tenantID && r.Action == action && r.SubjectID == subjectID {
				n++
			}
		}
		return n
	}
	repotest.CertDeliveryContract(t, open, audits)
}

func TestCertDeliveryFailNext(t *testing.T) {
	tid := repotest.TenantA
	calls := map[string]func(m *Mem) error{
		"CreateCertDelivery": func(m *Mem) error {
			_, e := m.CreateCertDelivery(ctx(), repo.NewCertDelivery{Delivery: store.CertDelivery{ID: "d", TenantID: tid}})
			return e
		},
		"GetCertDelivery":      func(m *Mem) error { _, _, e := m.GetCertDelivery(ctx(), tid, "d"); return e },
		"GetCertDeliveryByKey": func(m *Mem) error { _, e := m.GetCertDeliveryByKey(ctx(), tid, "s", "k"); return e },
		"GetCertItem":          func(m *Mem) error { _, e := m.GetCertItem(ctx(), tid, "i"); return e },
		"UpdateCertItem": func(m *Mem) error {
			_, e := m.UpdateCertItem(ctx(), tid, "i", func(*store.CertDeliveryItem) (repo.CertItemChange, error) { return repo.CertItemChange{}, nil })
			return e
		},
		"ListActiveCertItemsForAgent": func(m *Mem) error { _, e := m.ListActiveCertItemsForAgent(ctx(), tid, "a", 0); return e },
		"ListCertItemsPage": func(m *Mem) error {
			_, _, _, e := m.ListCertItemsPage(ctx(), tid, repo.CertItemFilter{}, listquery.Request{})
			return e
		},
		"CancelCertItems": func(m *Mem) error {
			_, e := m.CancelCertItems(ctx(), tid, repo.CertCancelScope{HostID: "h"}, "x", nil)
			return e
		},
		"GetHostCertificate":   func(m *Mem) error { _, e := m.GetHostCertificate(ctx(), tid, "h", "n"); return e },
		"ListHostCertificates": func(m *Mem) error { _, e := m.ListHostCertificates(ctx(), tid, "h"); return e },
		"PurgeCertItems":       func(m *Mem) error { _, e := m.PurgeCertItems(ctx(), time.Now()); return e },
	}
	for method, run := range calls {
		m := New()
		m.FailNext(method)
		var ie injectedErr
		if err := run(m); !errors.As(err, &ie) {
			t.Errorf("%s: want injected error, got %v", method, err)
		}
	}
}

func TestCertDeliveryMemEdges(t *testing.T) {
	m := New()
	tid := repotest.TenantA
	d := store.CertDelivery{ID: "d1", TenantID: tid, Source: "deployer", IdempotencyKey: "k1", Name: "www", CreatedAt: time.Now()}
	it := store.CertDeliveryItem{ID: "i1", TenantID: tid, DeliveryID: "d1", HostID: "h1", Name: "www", State: store.DeliveryPending}
	if _, err := m.CreateCertDelivery(ctx(), repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{it}}); err != nil {
		t.Fatal(err)
	}
	// Reused delivery id, reused item id and two items for one host conflict.
	d2 := d
	d2.IdempotencyKey = "k2"
	if _, err := m.CreateCertDelivery(ctx(), repo.NewCertDelivery{Delivery: d2}); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("reused delivery id: %v", err)
	}
	d2.ID = "d2"
	if _, err := m.CreateCertDelivery(ctx(), repo.NewCertDelivery{Delivery: d2, Items: []store.CertDeliveryItem{it}}); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("reused item id: %v", err)
	}
	a := store.CertDeliveryItem{ID: "i2", TenantID: tid, DeliveryID: "d2", HostID: "h2", Name: "www", State: store.DeliveryPending}
	b := a
	b.ID = "i3"
	if _, err := m.CreateCertDelivery(ctx(), repo.NewCertDelivery{Delivery: d2, Items: []store.CertDeliveryItem{a, b}}); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("two items for one host: %v", err)
	}
	// A transaction failing after fn leaves the item unchanged.
	m.FailNext("UpdateCertItem.commit")
	if _, err := m.UpdateCertItem(ctx(), tid, "i1", func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
		i.State = store.DeliveryFailed
		return repo.CertItemChange{}, nil
	}); err == nil {
		t.Fatal("commit failure not surfaced")
	}
	if g, _ := m.GetCertItem(ctx(), tid, "i1"); g.State != store.DeliveryPending {
		t.Fatalf("failed commit changed the item: %+v", g)
	}
}

func TestCertDeliveryFailNextAdditions(t *testing.T) {
	tid := repotest.TenantA
	calls := map[string]func(m *Mem) error{
		"ExtendCertDelivery":         func(m *Mem) error { return m.ExtendCertDelivery(ctx(), tid, "d", time.Now()) },
		"ListStaleCertItems":         func(m *Mem) error { _, e := m.ListStaleCertItems(ctx(), time.Now(), time.Now(), 0); return e },
		"ListHostCertificatesByName": func(m *Mem) error { _, e := m.ListHostCertificatesByName(ctx(), tid, "n"); return e },
		"ListActiveCertItemsByName":  func(m *Mem) error { _, e := m.ListActiveCertItemsByName(ctx(), tid, "n"); return e },
		"RevokeHostCertificates": func(m *Mem) error {
			_, e := m.RevokeHostCertificates(ctx(), tid, "c", time.Now(), nil)
			return e
		},
	}
	for method, run := range calls {
		m := New()
		m.FailNext(method)
		var ie injectedErr
		if err := run(m); !errors.As(err, &ie) {
			t.Errorf("%s: want injected error, got %v", method, err)
		}
	}
}
