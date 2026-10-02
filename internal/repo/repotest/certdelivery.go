// Package repotest holds storage contract tests shared by the in-memory store
// (memstore unit tests) and the database store (repodb integration suite), so
// both implementations are held to the same behaviour.
package repotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Tenants used by the contract (uuids, as the database requires).
const (
	TenantA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c77"
	TenantB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c88"
)

// AuditCounter returns how many audit rows with action exist for subject id.
type AuditCounter func(t *testing.T, tenantID, action, subjectID string) int

// base is a second-aligned clock so both stores round-trip times exactly.
var base = time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)

func delivery(tenant, key, name, certID string, at time.Time) store.CertDelivery {
	return store.CertDelivery{
		ID: store.NewID(), TenantID: tenant, Source: "deployer", IdempotencyKey: key, ConfigurationID: "cfg-1",
		Trigger: store.TriggerManual, CertificateID: certID, Name: name, KeyPolicy: store.KeyPolicyRequire,
		HostIDs: []string{}, HostTags: []string{"role=web"}, RequestedBy: "spiffe://example.org/svc/deployer",
		CreatedAt: at, ExpiresAt: at.Add(168 * time.Hour),
	}
}

func item(d store.CertDelivery, hostID, agentID, state string) store.CertDeliveryItem {
	return store.CertDeliveryItem{
		ID: store.NewID(), TenantID: d.TenantID, DeliveryID: d.ID, HostID: hostID, AgentID: agentID, Name: d.Name,
		CertificateID: d.CertificateID, State: state, Attempts: 1, CreatedAt: d.CreatedAt, UpdatedAt: d.CreatedAt,
	}
}

func auditRow(tenant, action, subject string, at time.Time) store.AuditRow {
	return store.AuditRow{ID: store.NewID(), TenantID: tenant, At: at, ActorKind: "service", ActorID: "deployer",
		Action: action, SubjectKind: "cert_delivery", SubjectID: subject, Outcome: "ok",
		Detail: map[string]any{"name": "www"}}
}

func supersedeAudit(old store.CertDeliveryItem, newID string) store.AuditRow {
	r := auditRow(old.TenantID, "cert_delivery_superseded", old.ID, base)
	r.ActorKind, r.ActorID = "system", "inventory"
	r.Detail = map[string]any{"superseded_by": newID}
	return r
}

// CertDeliveryContract runs the certificate delivery storage contract
// (feature 033, T015) against a fresh store from open.
func CertDeliveryContract(t *testing.T, open func(t *testing.T) repo.Store, audits AuditCounter) {
	ctx := context.Background()

	t.Run("create, get, idempotency, isolation", func(t *testing.T) {
		st := open(t)
		h1, h2, agent := store.NewID(), store.NewID(), store.NewID()
		d := delivery(TenantA, "job-1", "www", "cert-1", base)
		i1 := item(d, h1, agent, store.DeliveryPending)
		i2 := item(d, h2, "", store.DeliveryUnsupported)
		i2.Reason = store.ReasonNoAgent
		fin := base
		i2.FinishedAt = &fin
		sup, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{i1, i2},
			Audit: []store.AuditRow{auditRow(TenantA, "cert_delivery_requested", d.ID, base)}, SupersedeAudit: supersedeAudit})
		if err != nil || len(sup) != 0 {
			t.Fatalf("create: %v %v", sup, err)
		}
		if n := audits(t, TenantA, "cert_delivery_requested", d.ID); n != 1 {
			t.Fatalf("requested audit rows = %d", n)
		}
		gd, items, err := st.GetCertDelivery(ctx, TenantA, d.ID)
		if err != nil || gd.IdempotencyKey != "job-1" || gd.Name != "www" || gd.KeyPolicy != store.KeyPolicyRequire ||
			len(gd.HostTags) != 1 || gd.HostTags[0] != "role=web" || !gd.CreatedAt.Equal(base) || !gd.ExpiresAt.Equal(d.ExpiresAt) ||
			gd.ConfigurationID != "cfg-1" || gd.RequestedBy != d.RequestedBy || gd.Trigger != store.TriggerManual {
			t.Fatalf("get delivery: %+v %v", gd, err)
		}
		if len(items) != 2 {
			t.Fatalf("items = %d", len(items))
		}
		byID := map[string]store.CertDeliveryItem{items[0].ID: items[0], items[1].ID: items[1]}
		if g := byID[i1.ID]; g.AgentID != agent || g.State != store.DeliveryPending || g.Attempts != 1 || g.HookExitCode != nil {
			t.Fatalf("item 1: %+v", g)
		}
		if g := byID[i2.ID]; g.AgentID != "" || g.State != store.DeliveryUnsupported || g.Reason != store.ReasonNoAgent ||
			g.FinishedAt == nil || !g.FinishedAt.Equal(base) {
			t.Fatalf("item 2: %+v", g)
		}
		if got, err := st.GetCertDeliveryByKey(ctx, TenantA, "deployer", "job-1"); err != nil || got.ID != d.ID {
			t.Fatalf("by key: %v %v", got.ID, err)
		}
		if _, err := st.GetCertDeliveryByKey(ctx, TenantA, "deployer", "job-2"); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("by unknown key: %v", err)
		}
		// Same idempotency key: conflict, nothing written.
		d2 := delivery(TenantA, "job-1", "www", "cert-1", base)
		i3 := item(d2, store.NewID(), "", store.DeliveryPending)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d2, Items: []store.CertDeliveryItem{i3}}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("duplicate key: %v", err)
		}
		if _, err := st.GetCertItem(ctx, TenantA, i3.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("conflicting create left an item: %v", err)
		}
		// Another tenant sees nothing.
		if _, _, err := st.GetCertDelivery(ctx, TenantB, d.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("tenant B get delivery: %v", err)
		}
		if _, err := st.GetCertItem(ctx, TenantB, i1.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("tenant B get item: %v", err)
		}
		if _, err := st.GetCertDeliveryByKey(ctx, TenantB, "deployer", "job-1"); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("tenant B by key: %v", err)
		}
		// The same key in another tenant is a different delivery.
		db := delivery(TenantB, "job-1", "www", "cert-1", base)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: db}); err != nil {
			t.Fatalf("tenant B same key: %v", err)
		}
	})

	t.Run("supersede active item of the same host and name", func(t *testing.T) {
		st := open(t)
		host, agent := store.NewID(), store.NewID()
		d1 := delivery(TenantA, "job-1", "www", "cert-1", base)
		old := item(d1, host, agent, store.DeliveryDelivered)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d1, Items: []store.CertDeliveryItem{old}}); err != nil {
			t.Fatal(err)
		}
		// A different name on the same host stays active.
		dOther := delivery(TenantA, "job-api", "api", "cert-1", base)
		other := item(dOther, host, agent, store.DeliveryPending)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: dOther, Items: []store.CertDeliveryItem{other}}); err != nil {
			t.Fatal(err)
		}
		// Terminal items of the same host and name may accumulate.
		d0 := delivery(TenantA, "job-0", "www", "cert-0", base.Add(-time.Hour))
		term := item(d0, host, agent, store.DeliveryInstalled)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d0, Items: []store.CertDeliveryItem{term}}); err != nil {
			t.Fatalf("terminal item next to an active one: %v", err)
		}
		d2 := delivery(TenantA, "job-2", "www", "cert-2", base.Add(time.Minute))
		nw := item(d2, host, agent, store.DeliveryPending)
		sup, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d2, Items: []store.CertDeliveryItem{nw}, SupersedeAudit: supersedeAudit})
		if err != nil || len(sup) != 1 || sup[0].ID != old.ID || sup[0].State != store.DeliverySuperseded {
			t.Fatalf("supersede: %+v %v", sup, err)
		}
		g, err := st.GetCertItem(ctx, TenantA, old.ID)
		if err != nil || g.State != store.DeliverySuperseded || g.FinishedAt == nil || !g.UpdatedAt.Equal(d2.CreatedAt) {
			t.Fatalf("old item: %+v %v", g, err)
		}
		if n := audits(t, TenantA, "cert_delivery_superseded", old.ID); n != 1 {
			t.Fatalf("superseded audit rows = %d", n)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, other.ID); g.State != store.DeliveryPending {
			t.Fatalf("other name was touched: %+v", g)
		}
		// A terminal new item supersedes nothing.
		d3 := delivery(TenantA, "job-3", "www", "cert-3", base.Add(2*time.Minute))
		unsup := item(d3, host, agent, store.DeliverySuperseded)
		if sup, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d3, Items: []store.CertDeliveryItem{unsup}}); err != nil || len(sup) != 0 {
			t.Fatalf("terminal new item: %v %v", sup, err)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, nw.ID); g.State != store.DeliveryPending {
			t.Fatalf("terminal new item superseded the active one: %+v", g)
		}
		// Supersession without an audit builder still supersedes.
		d4 := delivery(TenantA, "job-4", "www", "cert-4", base.Add(3*time.Minute))
		n4 := item(d4, host, agent, store.DeliveryPending)
		if sup, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d4, Items: []store.CertDeliveryItem{n4}}); err != nil || len(sup) != 1 || sup[0].ID != nw.ID {
			t.Fatalf("supersede without audit: %v %v", sup, err)
		}
	})

	t.Run("active items per agent", func(t *testing.T) {
		st := open(t)
		agent, otherAgent := store.NewID(), store.NewID()
		var ids []string
		for i := 0; i < 4; i++ {
			d := delivery(TenantA, "job-"+string(rune('a'+i)), "n"+string(rune('a'+i)), "cert", base.Add(time.Duration(i)*time.Minute))
			state := []string{store.DeliveryFetched, store.DeliveryPending, store.DeliveryInstalled, store.DeliveryDelivered}[i]
			it := item(d, store.NewID(), agent, state)
			if state == store.DeliveryInstalled {
				it.FinishedAt = &d.CreatedAt
			} else {
				ids = append(ids, it.ID)
			}
			o := item(d, store.NewID(), otherAgent, store.DeliveryPending)
			if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{it, o}}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.ListActiveCertItemsForAgent(ctx, TenantA, agent, 0)
		if err != nil || len(got) != 3 || got[0].ID != ids[0] || got[1].ID != ids[1] || got[2].ID != ids[2] {
			t.Fatalf("active for agent: %v %v", got, err)
		}
		if got, _ := st.ListActiveCertItemsForAgent(ctx, TenantA, agent, 2); len(got) != 2 || got[1].ID != ids[1] {
			t.Fatalf("limit: %v", got)
		}
		if got, _ := st.ListActiveCertItemsForAgent(ctx, TenantB, agent, 0); len(got) != 0 {
			t.Fatalf("other tenant: %v", got)
		}
	})

	t.Run("update item and host certificate", func(t *testing.T) {
		st := open(t)
		host, agent := store.NewID(), store.NewID()
		d := delivery(TenantA, "job-1", "www", "cert-1", base)
		it := item(d, host, agent, store.DeliveryDelivered)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{it}}); err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Minute)
		notAfter := base.Add(90 * 24 * time.Hour)
		fp := "9c1d000000000000000000000000000000000000000000000000000000000000"
		got, err := st.UpdateCertItem(ctx, TenantA, it.ID, func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
			if i.State != store.DeliveryDelivered {
				t.Fatalf("callback sees %q", i.State)
			}
			i.State, i.Fetches, i.Serial, i.NotAfter, i.FetchedAt, i.UpdatedAt = store.DeliveryFetched, 1, "4f3a", &notAfter, &at, at
			return repo.CertItemChange{Audit: []store.AuditRow{auditRow(TenantA, "cert_delivery_fetched", it.ID, at)}}, nil
		})
		if err != nil || got.State != store.DeliveryFetched || got.Fetches != 1 || got.Serial != "4f3a" || !got.NotAfter.Equal(notAfter) {
			t.Fatalf("fetch transition: %+v %v", got, err)
		}
		code := 0
		hc := store.HostCertificate{TenantID: TenantA, HostID: host, Name: "www", CertificateID: "cert-1", ConfigurationID: "cfg-1",
			CommonName: "www.example.com", Serial: "4f3a", FingerprintSHA256: fp, NotAfter: &notAfter, State: store.DeliveryInstalled,
			HookExitCode: &code, LastItemID: it.ID, LastDeliveredAt: &at, UpdatedAt: at}
		got, err = st.UpdateCertItem(ctx, TenantA, it.ID, func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
			i.State, i.FingerprintSHA256, i.HookExitCode, i.FinishedAt, i.UpdatedAt = store.DeliveryInstalled, fp, &code, &at, at
			return repo.CertItemChange{Audit: []store.AuditRow{auditRow(TenantA, "cert_delivery_installed", it.ID, at)}, HostCert: &hc}, nil
		})
		if err != nil || got.State != store.DeliveryInstalled || got.HookExitCode == nil || *got.HookExitCode != 0 || got.FingerprintSHA256 != fp {
			t.Fatalf("install transition: %+v %v", got, err)
		}
		if audits(t, TenantA, "cert_delivery_installed", it.ID) != 1 || audits(t, TenantA, "cert_delivery_fetched", it.ID) != 1 {
			t.Fatal("transition audit rows missing")
		}
		g, err := st.GetHostCertificate(ctx, TenantA, host, "www")
		if err != nil || g.FingerprintSHA256 != fp || g.LastItemID != it.ID || g.CommonName != "www.example.com" ||
			g.HookExitCode == nil || g.LastDeliveredAt == nil || !g.LastDeliveredAt.Equal(at) || g.RevokedAt != nil {
			t.Fatalf("host certificate: %+v %v", g, err)
		}
		// Upsert replaces the row.
		hc2 := hc
		hc2.State, hc2.Reason, hc2.HookExitCode, hc2.UpdatedAt = store.DeliveryHookFailed, store.ReasonHookFailed, nil, at.Add(time.Minute)
		if _, err := st.UpdateCertItem(ctx, TenantA, it.ID, func(*store.CertDeliveryItem) (repo.CertItemChange, error) {
			return repo.CertItemChange{HostCert: &hc2}, nil
		}); err != nil {
			t.Fatal(err)
		}
		hcB := hc
		hcB.Name = "api"
		if _, err := st.UpdateCertItem(ctx, TenantA, it.ID, func(*store.CertDeliveryItem) (repo.CertItemChange, error) {
			return repo.CertItemChange{HostCert: &hcB}, nil
		}); err != nil {
			t.Fatal(err)
		}
		list, err := st.ListHostCertificates(ctx, TenantA, host)
		if err != nil || len(list) != 2 || list[0].Name != "api" || list[1].Name != "www" || list[1].State != store.DeliveryHookFailed || list[1].HookExitCode != nil {
			t.Fatalf("list host certificates: %+v %v", list, err)
		}
		if l, _ := st.ListHostCertificates(ctx, TenantB, host); len(l) != 0 {
			t.Fatal("host certificates leak across tenants")
		}
		if _, err := st.GetHostCertificate(ctx, TenantA, host, "nope"); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("missing host certificate: %v", err)
		}
		// An error of fn changes nothing.
		boom := errors.New("boom")
		if _, err := st.UpdateCertItem(ctx, TenantA, it.ID, func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
			i.State = store.DeliveryFailed
			return repo.CertItemChange{}, boom
		}); !errors.Is(err, boom) {
			t.Fatalf("fn error: %v", err)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, it.ID); g.State != store.DeliveryInstalled {
			t.Fatalf("fn error changed the item: %+v", g)
		}
		// Another tenant: not found.
		if _, err := st.UpdateCertItem(ctx, TenantB, it.ID, func(*store.CertDeliveryItem) (repo.CertItemChange, error) {
			t.Fatal("callback for another tenant")
			return repo.CertItemChange{}, nil
		}); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("other tenant update: %v", err)
		}
		// Re-arming next to another active item for the host and name conflicts.
		d2 := delivery(TenantA, "job-2", "www", "cert-2", base.Add(time.Hour))
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d2, Items: []store.CertDeliveryItem{item(d2, host, agent, store.DeliveryPending)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateCertItem(ctx, TenantA, it.ID, func(i *store.CertDeliveryItem) (repo.CertItemChange, error) {
			i.State, i.Attempts = store.DeliveryPending, 2
			return repo.CertItemChange{}, nil
		}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("second active item: %v", err)
		}
	})

	t.Run("paged item list", func(t *testing.T) {
		st := open(t)
		host := store.NewID()
		var first store.CertDelivery
		for i := 0; i < 5; i++ {
			name := []string{"www", "api", "www", "mail", "www"}[i]
			d := delivery(TenantA, "job-"+string(rune('a'+i)), name, "cert-"+string(rune('a'+i%2)), base.Add(time.Duration(i)*time.Minute))
			if i == 0 {
				first = d
			}
			state := store.DeliveryInstalled
			if i == 4 {
				state = store.DeliveryPending
			}
			it := item(d, host, "", state)
			if i == 3 {
				it.HostID = store.NewID()
			}
			if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{it}}); err != nil {
				t.Fatal(err)
			}
		}
		page, total, applied, err := st.ListCertItemsPage(ctx, TenantA, repo.CertItemFilter{}, listquery.Request{Page: 1, PageSize: 2})
		if err != nil || total != 5 || len(page) != 2 || page[0].Name != "www" || page[1].Name != "mail" || applied.Sort != "created_at" {
			t.Fatalf("default page: %v %d %+v %v", page, total, applied, err)
		}
		if page, total, applied, _ := st.ListCertItemsPage(ctx, TenantA, repo.CertItemFilter{}, listquery.Request{Page: 9, PageSize: 2}); total != 5 || len(page) != 1 || applied.Page != 3 {
			t.Fatalf("clamped page: %v %d %+v", page, total, applied)
		}
		for _, c := range []struct {
			f    repo.CertItemFilter
			want int
		}{
			{repo.CertItemFilter{HostID: host}, 4},
			{repo.CertItemFilter{State: store.DeliveryPending}, 1},
			{repo.CertItemFilter{Name: "www"}, 3},
			{repo.CertItemFilter{CertificateID: "cert-b"}, 2},
			{repo.CertItemFilter{DeliveryID: first.ID}, 1},
			{repo.CertItemFilter{HostID: host, Name: "www", State: store.DeliveryInstalled}, 2},
		} {
			if _, total, _, err := st.ListCertItemsPage(ctx, TenantA, c.f, listquery.Request{}); err != nil || total != c.want {
				t.Errorf("filter %+v: total %d want %d (%v)", c.f, total, c.want, err)
			}
		}
		if page, _, _, _ := st.ListCertItemsPage(ctx, TenantA, repo.CertItemFilter{}, listquery.Request{Sort: "name", Order: listquery.Asc}); len(page) != 5 || page[0].Name != "api" || page[4].Name != "www" {
			t.Fatalf("sort by name: %v", page)
		}
		if page, _, _, _ := st.ListCertItemsPage(ctx, TenantA, repo.CertItemFilter{}, listquery.Request{Sort: "state", Order: listquery.Desc}); page[0].State != store.DeliveryPending {
			t.Fatalf("sort by state: %v", page)
		}
		if page, _, _, _ := st.ListCertItemsPage(ctx, TenantA, repo.CertItemFilter{}, listquery.Request{Sort: "updated_at", Order: listquery.Asc}); page[0].DeliveryID != first.ID {
			t.Fatalf("sort by updated_at: %v", page)
		}
		if _, total, _, _ := st.ListCertItemsPage(ctx, TenantB, repo.CertItemFilter{}, listquery.Request{}); total != 0 {
			t.Fatal("items leak across tenants")
		}
	})

	t.Run("cancel by host, agent and certificate", func(t *testing.T) {
		st := open(t)
		h1, h2, a1, a2 := store.NewID(), store.NewID(), store.NewID(), store.NewID()
		d := delivery(TenantA, "job-1", "www", "cert-1", base)
		i1 := item(d, h1, a1, store.DeliveryPending)
		i2 := item(d, h2, a2, store.DeliveryFetched)
		d2 := delivery(TenantA, "job-2", "api", "cert-2", base)
		i3 := item(d2, h1, a1, store.DeliveryDelivered)
		i4 := item(d2, h2, a2, store.DeliveryInstalled)
		for _, n := range []repo.NewCertDelivery{{Delivery: d, Items: []store.CertDeliveryItem{i1, i2}}, {Delivery: d2, Items: []store.CertDeliveryItem{i3, i4}}} {
			if _, err := st.CreateCertDelivery(ctx, n); err != nil {
				t.Fatal(err)
			}
		}
		row := func(i store.CertDeliveryItem) store.AuditRow {
			r := auditRow(i.TenantID, "cert_delivery_cancelled", i.ID, base)
			r.Detail = map[string]any{"reason": i.Reason}
			return r
		}
		got, err := st.CancelCertItems(ctx, TenantA, repo.CertCancelScope{HostID: h1}, store.ReasonHostDeleted, row)
		if err != nil || len(got) != 2 {
			t.Fatalf("cancel by host: %v %v", got, err)
		}
		for _, id := range []string{i1.ID, i3.ID} {
			g, _ := st.GetCertItem(ctx, TenantA, id)
			if g.State != store.DeliveryCancelled || g.Reason != store.ReasonHostDeleted || g.FinishedAt == nil {
				t.Fatalf("cancelled item: %+v", g)
			}
			if audits(t, TenantA, "cert_delivery_cancelled", id) != 1 {
				t.Fatalf("cancel audit for %s", id)
			}
		}
		if got, _ := st.CancelCertItems(ctx, TenantB, repo.CertCancelScope{AgentID: a2}, store.ReasonAgentRevoked, row); len(got) != 0 {
			t.Fatal("cancel across tenants")
		}
		got, err = st.CancelCertItems(ctx, TenantA, repo.CertCancelScope{AgentID: a2}, store.ReasonAgentRevoked, row)
		if err != nil || len(got) != 1 || got[0].ID != i2.ID || got[0].Reason != store.ReasonAgentRevoked {
			t.Fatalf("cancel by agent (terminal kept): %v %v", got, err)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, i4.ID); g.State != store.DeliveryInstalled {
			t.Fatalf("terminal item cancelled: %+v", g)
		}
		d3 := delivery(TenantA, "job-3", "mail", "cert-3", base)
		i5 := item(d3, h2, a2, store.DeliveryPending)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d3, Items: []store.CertDeliveryItem{i5}}); err != nil {
			t.Fatal(err)
		}
		got, err = st.CancelCertItems(ctx, TenantA, repo.CertCancelScope{CertificateID: "cert-3"}, store.ReasonCertificateRevoked, row)
		if err != nil || len(got) != 1 || got[0].ID != i5.ID {
			t.Fatalf("cancel by certificate: %v %v", got, err)
		}
		if got, err := st.CancelCertItems(ctx, TenantA, repo.CertCancelScope{}, store.ReasonCancelledByUser, row); err == nil || len(got) != 0 {
			t.Fatalf("empty scope must be refused: %v", err)
		}
	})

	t.Run("purge terminal items", func(t *testing.T) {
		st := open(t)
		old := delivery(TenantA, "job-old", "www", "cert-1", base.Add(-100*24*time.Hour))
		oi := item(old, store.NewID(), "", store.DeliveryInstalled)
		mixed := delivery(TenantA, "job-mixed", "api", "cert-1", base.Add(-100*24*time.Hour))
		mi1 := item(mixed, store.NewID(), "", store.DeliveryFailed)
		mi2 := item(mixed, store.NewID(), "", store.DeliveryPending) // active: kept however old
		recent := delivery(TenantB, "job-new", "www", "cert-1", base)
		ri := item(recent, store.NewID(), "", store.DeliveryInstalled)
		for _, n := range []repo.NewCertDelivery{
			{Delivery: old, Items: []store.CertDeliveryItem{oi}},
			{Delivery: mixed, Items: []store.CertDeliveryItem{mi1, mi2}},
			{Delivery: recent, Items: []store.CertDeliveryItem{ri}},
		} {
			if _, err := st.CreateCertDelivery(ctx, n); err != nil {
				t.Fatal(err)
			}
		}
		n, err := st.PurgeCertItems(ctx, base.Add(-90*24*time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("purge: %d %v", n, err)
		}
		if _, _, err := st.GetCertDelivery(ctx, TenantA, old.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("empty delivery kept: %v", err)
		}
		if _, items, err := st.GetCertDelivery(ctx, TenantA, mixed.ID); err != nil || len(items) != 1 || items[0].ID != mi2.ID {
			t.Fatalf("mixed delivery: %v %v", items, err)
		}
		if _, err := st.GetCertItem(ctx, TenantB, ri.ID); err != nil {
			t.Fatalf("recent item purged: %v", err)
		}
	})

	t.Run("extend, stale items, by-name lists", func(t *testing.T) {
		st := open(t)
		h1, h2, h3 := store.NewID(), store.NewID(), store.NewID()
		d := delivery(TenantA, "job-1", "www", "cert-1", base)
		d.ExpiresAt = base.Add(time.Hour)
		i1 := item(d, h1, "", store.DeliveryPending)
		i2 := item(d, h2, "", store.DeliveryFetched)
		i2.UpdatedAt = base.Add(-time.Hour)
		i3 := item(d, h3, "", store.DeliveryInstalled)
		d2 := delivery(TenantB, "job-2", "www", "cert-2", base)
		j1 := item(d2, h1, "", store.DeliveryDelivered)
		for _, n := range []repo.NewCertDelivery{{Delivery: d, Items: []store.CertDeliveryItem{i1, i2, i3}}, {Delivery: d2, Items: []store.CertDeliveryItem{j1}}} {
			if _, err := st.CreateCertDelivery(ctx, n); err != nil {
				t.Fatal(err)
			}
		}
		// Only the fetched item is stale before the expiry.
		stale, err := st.ListStaleCertItems(ctx, base, base.Add(-30*time.Minute), 0)
		if err != nil || len(stale) != 1 || stale[0].Item.ID != i2.ID || !stale[0].ExpiresAt.Equal(d.ExpiresAt) {
			t.Fatalf("stale before expiry: %+v %v", stale, err)
		}
		// After the expiry every active item of d is stale (both tenants: system scope).
		stale, err = st.ListStaleCertItems(ctx, base.Add(2*time.Hour), base.Add(-30*time.Minute), 0)
		if err != nil || len(stale) != 2 {
			t.Fatalf("stale after expiry: %+v %v", stale, err)
		}
		if stale, _ := st.ListStaleCertItems(ctx, base.Add(200*time.Hour), base, 1); len(stale) != 1 {
			t.Fatalf("limit: %+v", stale)
		}
		// Extending moves the expiry forward only.
		if err := st.ExtendCertDelivery(ctx, TenantA, d.ID, base.Add(48*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := st.ExtendCertDelivery(ctx, TenantA, d.ID, base); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := st.GetCertDelivery(ctx, TenantA, d.ID); !got.ExpiresAt.Equal(base.Add(48 * time.Hour)) {
			t.Fatalf("extended expiry = %v", got.ExpiresAt)
		}
		if err := st.ExtendCertDelivery(ctx, TenantB, d.ID, base.Add(72*time.Hour)); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("extend across tenants: %v", err)
		}
		active, err := st.ListActiveCertItemsByName(ctx, TenantA, "www")
		if err != nil || len(active) != 2 {
			t.Fatalf("active by name: %v %v", active, err)
		}
		if active, _ := st.ListActiveCertItemsByName(ctx, TenantA, "api"); len(active) != 0 {
			t.Fatalf("other name: %v", active)
		}
		// Host certificates by name.
		for _, h := range []string{h1, h2} {
			hc := store.HostCertificate{HostID: h, Name: "www", CertificateID: "cert-1", State: store.DeliveryInstalled, LastItemID: i3.ID, UpdatedAt: base}
			if _, err := st.UpdateCertItem(ctx, TenantA, i3.ID, func(*store.CertDeliveryItem) (repo.CertItemChange, error) {
				return repo.CertItemChange{HostCert: &hc}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		hcs, err := st.ListHostCertificatesByName(ctx, TenantA, "www")
		if err != nil || len(hcs) != 2 {
			t.Fatalf("host certificates by name: %v %v", hcs, err)
		}
		if hcs, _ := st.ListHostCertificatesByName(ctx, TenantB, "www"); len(hcs) != 0 {
			t.Fatal("host certificates leak across tenants")
		}
	})

	t.Run("revoke host certificates", func(t *testing.T) {
		st := open(t)
		h1, h2 := store.NewID(), store.NewID()
		d := delivery(TenantA, "job-1", "www", "cert-1", base)
		i1 := item(d, h1, "", store.DeliveryInstalled)
		if _, err := st.CreateCertDelivery(ctx, repo.NewCertDelivery{Delivery: d, Items: []store.CertDeliveryItem{i1}}); err != nil {
			t.Fatal(err)
		}
		for _, hc := range []store.HostCertificate{
			{HostID: h1, Name: "www", CertificateID: "cert-1", State: store.DeliveryInstalled, LastItemID: i1.ID, UpdatedAt: base},
			{HostID: h2, Name: "www", CertificateID: "cert-1", State: store.DeliveryInstalled, LastItemID: i1.ID, UpdatedAt: base},
			{HostID: h2, Name: "api", CertificateID: "cert-2", State: store.DeliveryInstalled, LastItemID: i1.ID, UpdatedAt: base},
		} {
			hc := hc
			if _, err := st.UpdateCertItem(ctx, TenantA, i1.ID, func(*store.CertDeliveryItem) (repo.CertItemChange, error) {
				return repo.CertItemChange{HostCert: &hc}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		row := func(h store.HostCertificate) store.AuditRow {
			r := auditRow(TenantA, "host_certificate_revoked", h.HostID, base)
			r.SubjectKind = "host"
			return r
		}
		at := base.Add(time.Hour)
		got, err := st.RevokeHostCertificates(ctx, TenantA, "cert-1", at, row)
		if err != nil || len(got) != 2 || got[0].RevokedAt == nil || !got[0].RevokedAt.Equal(at) {
			t.Fatalf("revoke: %+v %v", got, err)
		}
		if audits(t, TenantA, "host_certificate_revoked", h1) != 1 || audits(t, TenantA, "host_certificate_revoked", h2) != 1 {
			t.Fatal("revocation audit rows")
		}
		if hc, _ := st.GetHostCertificate(ctx, TenantA, h2, "api"); hc.RevokedAt != nil {
			t.Fatal("another certificate flagged")
		}
		// Idempotent, and tenant scoped.
		if got, err := st.RevokeHostCertificates(ctx, TenantA, "cert-1", at, row); err != nil || len(got) != 0 {
			t.Fatalf("second revoke: %v %v", got, err)
		}
		if got, _ := st.RevokeHostCertificates(ctx, TenantB, "cert-2", at, row); len(got) != 0 {
			t.Fatal("revoke across tenants")
		}
	})

	t.Run("host deletion and agent revocation cancel deliveries", func(t *testing.T) {
		st := open(t)
		host, err := st.ResolveHost(ctx, TenantA, store.Host{Hostname: "web-" + store.NewID(), MachineID: store.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		other, err := st.ResolveHost(ctx, TenantA, store.Host{Hostname: "web-" + store.NewID(), MachineID: store.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		agent := store.Agent{ID: store.NewID(), TenantID: TenantA, HostID: other.ID, CredentialSealed: []byte{1}, EnrolledAt: base, LastSeen: base}
		if err := st.CreateAgent(ctx, agent); err != nil {
			t.Fatal(err)
		}
		d := delivery(TenantA, "job-1", "www", "cert-1", base)
		i1 := item(d, host.ID, "", store.DeliveryPending)
		i2 := item(d, other.ID, agent.ID, store.DeliveryDelivered)
		d2 := delivery(TenantA, "job-2", "api", "cert-2", base)
		i3 := item(d2, host.ID, "", store.DeliveryInstalled)
		for _, n := range []repo.NewCertDelivery{{Delivery: d, Items: []store.CertDeliveryItem{i1, i2}}, {Delivery: d2, Items: []store.CertDeliveryItem{i3}}} {
			if _, err := st.CreateCertDelivery(ctx, n); err != nil {
				t.Fatal(err)
			}
		}
		hc := store.HostCertificate{HostID: host.ID, Name: "api", CertificateID: "cert-2", State: store.DeliveryInstalled, LastItemID: i3.ID, UpdatedAt: base}
		if _, err := st.UpdateCertItem(ctx, TenantA, i3.ID, func(*store.CertDeliveryItem) (repo.CertItemChange, error) {
			return repo.CertItemChange{HostCert: &hc}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteHost(ctx, TenantA, host.ID); err != nil {
			t.Fatal(err)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, i1.ID); g.State != store.DeliveryCancelled || g.Reason != store.ReasonHostDeleted || g.FinishedAt == nil {
			t.Fatalf("host deletion: %+v", g)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, i3.ID); g.State != store.DeliveryInstalled {
			t.Fatalf("terminal item changed: %+v", g)
		}
		if audits(t, TenantA, "cert_delivery_cancelled", i1.ID) != 1 {
			t.Fatal("host deletion audit row")
		}
		if hcs, _ := st.ListHostCertificates(ctx, TenantA, host.ID); len(hcs) != 0 {
			t.Fatalf("host certificates kept: %v", hcs)
		}
		if err := st.RevokeAgent(ctx, TenantA, agent.ID); err != nil {
			t.Fatal(err)
		}
		if g, _ := st.GetCertItem(ctx, TenantA, i2.ID); g.State != store.DeliveryCancelled || g.Reason != store.ReasonAgentRevoked {
			t.Fatalf("agent revocation: %+v", g)
		}
		if audits(t, TenantA, "cert_delivery_cancelled", i2.ID) != 1 {
			t.Fatal("agent revocation audit row")
		}
	})
}
