package certdelivery

import (
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestHistory (T073/T076): history items carry the hostname and their
// delivery (configuration, trigger, requested by); filters and paging are
// the store's; reads work while the relay is disabled.
func TestHistory(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1")
	_, _ = f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: 0})
	web2, _ := f.webHost(t, "web-2", false)
	r := f.req("job-2", web2.ID)
	r.Trigger, r.ConfigurationID = store.TriggerAutoDeploy, "cfg-2"
	v2, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	page, err := f.svc.History(ctx, tenant, repo.CertItemFilter{}, listquery.Request{})
	if err != nil || page.Total != 2 || len(page.Items) != 2 || page.Sort != "created_at" || page.Order != listquery.Desc {
		t.Fatalf("history: %+v %v", page, err)
	}
	byHost := map[string]HistoryItem{}
	for _, h := range page.Items {
		byHost[h.Hostname] = h
	}
	if h := byHost["web-1"]; h.ID != it.ID || h.Delivery.ConfigurationID != "cfg-1" || h.Delivery.Trigger != store.TriggerManual ||
		h.State != store.DeliveryInstalled || h.CommonName != "www.example.com" || h.Delivery.RequestedBy == "" {
		t.Fatalf("web-1 item: %+v", h)
	}
	if h := byHost["web-2"]; h.ID != v2.Items[0].ID || h.Delivery.ConfigurationID != "cfg-2" || h.Delivery.Trigger != store.TriggerAutoDeploy ||
		h.State != store.DeliveryPending {
		t.Fatalf("web-2 item: %+v", h)
	}
	// Filters: one host, another tenant, nothing.
	if page, _ := f.svc.History(ctx, tenant, repo.CertItemFilter{HostID: web2.ID}, listquery.Request{}); page.Total != 1 || page.Items[0].Hostname != "web-2" {
		t.Fatalf("host filter: %+v", page)
	}
	if page, err := f.svc.History(ctx, other, repo.CertItemFilter{}, listquery.Request{}); err != nil || page.Total != 0 || page.Items == nil {
		t.Fatalf("other tenant: %+v %v", page, err)
	}
	// One item with its delivery.
	h, err := f.svc.Item(ctx, tenant, it.ID)
	if err != nil || h.Hostname != "web-1" || h.Delivery.ID != it.DeliveryID || h.Delivery.Source != "deployer" {
		t.Fatalf("item: %+v %v", h, err)
	}
	for _, id := range []string{"x", store.NewID()} {
		if _, err := f.svc.Item(ctx, tenant, id); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("item %q: %v", id, err)
		}
	}
	if _, err := f.svc.Item(ctx, other, it.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("other tenant item: %v", err)
	}
	// Disabled relay: reads still work.
	f.svc.cfg.Enabled = false
	if page, err := f.svc.History(ctx, tenant, repo.CertItemFilter{}, listquery.Request{}); err != nil || page.Total != 2 {
		t.Fatalf("disabled read: %+v %v", page, err)
	}
	if _, err := f.svc.Item(ctx, tenant, it.ID); err != nil {
		t.Fatalf("disabled item read: %v", err)
	}
	f.svc.cfg.Enabled = true
	// Store errors.
	for _, method := range []string{"ListCertItemsPage", "ListCertDeliveriesByID", "HostnamesByID"} {
		f.mem.FailNext(method)
		if _, err := f.svc.History(ctx, tenant, repo.CertItemFilter{}, listquery.Request{}); err == nil {
			t.Fatalf("%s error swallowed", method)
		}
	}
	for _, method := range []string{"GetCertItem", "ListCertDeliveriesByID"} {
		f.mem.FailNext(method)
		if _, err := f.svc.Item(ctx, tenant, it.ID); err == nil {
			t.Fatalf("%s error swallowed", method)
		}
	}
}

// TestHostCertificatePage (T073/T076/T105): one row per name; the active
// item is attached; a name with only a queued item is listed with that
// item's state; revoked and state filters; sorting and paging in Go.
func TestHostCertificatePage(t *testing.T) {
	f := newFix(t)
	it, a := f.fetched(t, "job-1") // www on web-1
	_, _ = f.svc.Report(ctx, a, Report{ItemID: it.ID, State: store.DeliveryInstalled, Fingerprint: it.FingerprintSHA256, HookExitCode: 0})
	host := it.HostID
	// A renewal of www is queued (active item on an installed name).
	f.now = f.now.Add(time.Minute)
	queued, err := f.svc.Create(ctx, f.req("job-2", host))
	if err != nil {
		t.Fatal(err)
	}
	// A name delivered for the first time, waiting for the agent: "api".
	r := f.req("job-3", host)
	r.Name, r.ConfigurationID = "api", "cfg-api"
	f.now = f.now.Add(time.Minute)
	api, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	// A name whose delivery failed: "mail" (host certificate with state failed).
	r = f.req("job-4", host)
	r.Name = "mail"
	f.now = f.now.Add(time.Minute)
	mail, err := f.svc.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Report(ctx, a, Report{ItemID: mail.Items[0].ID, State: store.DeliveryFailed, Reason: store.ReasonWriteFailed, HookExitCode: -1}); err != nil {
		t.Fatal(err)
	}

	page, err := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{}, listquery.Request{})
	if err != nil || page.Total != 3 || len(page.Items) != 3 || page.Sort != "name" || page.Order != listquery.Asc {
		t.Fatalf("page: %+v %v", page, err)
	}
	names := []string{page.Items[0].Name, page.Items[1].Name, page.Items[2].Name}
	if names[0] != "api" || names[1] != "mail" || names[2] != "www" {
		t.Fatalf("order: %v", names)
	}
	apiRow, mailRow, www := page.Items[0], page.Items[1], page.Items[2]
	if apiRow.State != store.DeliveryDelivered || apiRow.Active == nil || apiRow.Active.ID != api.Items[0].ID || apiRow.ConfigurationID != "cfg-api" ||
		apiRow.CertificateID != cert1 || apiRow.LastItemID != api.Items[0].ID || apiRow.LastDeliveredAt != nil || apiRow.Active.Hostname != "web-1" {
		t.Fatalf("queued-only row: %+v", apiRow)
	}
	if mailRow.State != store.DeliveryFailed || mailRow.Reason != store.ReasonWriteFailed || mailRow.Active != nil {
		t.Fatalf("failed row: %+v", mailRow)
	}
	if www.State != store.DeliveryInstalled || www.CommonName != "www.example.com" || www.Active == nil || www.Active.ID != queued.Items[0].ID ||
		www.Active.Delivery.ConfigurationID != "cfg-1" || www.LastDeliveredAt == nil {
		t.Fatalf("installed row with a queued renewal: %+v", www)
	}

	// Store errors.
	for _, method := range []string{"ListHostCertificates", "ListCertItemsPage", "HostnamesByID"} {
		f.mem.FailNext(method)
		if _, err := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{}, listquery.Request{}); err == nil {
			t.Fatalf("%s error swallowed", method)
		}
	}

	// Sorting by each field, both directions; paging.
	for _, c := range []struct {
		sort  string
		order listquery.Dir
		first string
	}{
		{"name", listquery.Desc, "www"},
		{"state", listquery.Asc, "api"}, // delivered < failed < installed
		{"state", listquery.Desc, "www"},
		{"not_after", listquery.Asc, "www"}, // the others have none (last)
		{"last_delivered_at", listquery.Desc, "www"},
		{"last_delivered_at", listquery.Asc, "www"},
	} {
		p, err := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{}, listquery.Request{Sort: c.sort, Order: c.order})
		if err != nil || p.Items[0].Name != c.first {
			t.Fatalf("sort %s %s: first %q %v", c.sort, c.order, p.Items[0].Name, err)
		}
	}
	p2, _ := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{}, listquery.Request{Page: 2, PageSize: 2})
	if p2.Total != 3 || len(p2.Items) != 1 || p2.Items[0].Name != "www" || p2.Page != 2 {
		t.Fatalf("page 2: %+v", p2)
	}
	// A page beyond the end answers the last page.
	if p9, _ := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{}, listquery.Request{Page: 9, PageSize: 2}); p9.Page != 2 {
		t.Fatalf("clamp: %+v", p9)
	}

	// Filters: state; revoked (T105).
	if p, _ := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{State: store.DeliveryFailed}, listquery.Request{}); p.Total != 1 || p.Items[0].Name != "mail" {
		t.Fatalf("state filter: %+v", p)
	}
	yes, no := true, false
	if p, _ := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{Revoked: &yes}, listquery.Request{}); p.Total != 0 {
		t.Fatalf("revoked before revocation: %+v", p)
	}
	if _, _, err := f.svc.MarkRevoked(ctx, tenant, Actor{Kind: "service", ID: "deployer"}, cert1); err != nil {
		t.Fatal(err)
	}
	p, _ := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{Revoked: &yes}, listquery.Request{})
	if p.Total != 2 || p.Items[0].Name != "mail" || p.Items[0].RevokedAt == nil || p.Items[1].Name != "www" {
		t.Fatalf("revoked filter: %+v", p)
	}
	// The revocation cancelled the queued items: no active item is left.
	for _, row := range p.Items {
		if row.Active != nil {
			t.Fatalf("active item after revocation: %+v", row)
		}
	}
	if p, _ := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{Revoked: &no}, listquery.Request{}); p.Total != 0 {
		t.Fatalf("not revoked: %+v", p)
	}

	// Other tenants, unknown hosts and a disabled relay.
	if p, err := f.svc.HostCertificatePage(ctx, other, host, HostCertFilter{}, listquery.Request{}); err != nil || p.Total != 0 || p.Items == nil {
		t.Fatalf("other tenant: %+v %v", p, err)
	}
	f.svc.cfg.Enabled = false
	if p, err := f.svc.HostCertificatePage(ctx, tenant, host, HostCertFilter{}, listquery.Request{}); err != nil || p.Total != 2 {
		t.Fatalf("disabled read: %+v %v", p, err)
	}
	f.svc.cfg.Enabled = true

}

// TestHostActiveItemsPaged: the active items of a host are read page by
// page until every one is seen.
func TestHostActiveItemsPaged(t *testing.T) {
	f := newFix(t)
	web1, _ := f.webHost(t, "web-1", false)
	for i, name := range []string{"a", "b", "c"} {
		r := f.req("job-"+name, web1.ID)
		r.Name = name
		f.now = t0.Add(time.Duration(i) * time.Second)
		if _, err := f.svc.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	defer func(n int) { activePageSize = n }(activePageSize)
	activePageSize = 2
	p, err := f.svc.HostCertificatePage(ctx, tenant, web1.ID, HostCertFilter{}, listquery.Request{})
	if err != nil || p.Total != 3 {
		t.Fatalf("paged active items: %+v %v", p, err)
	}
	for _, row := range p.Items {
		if row.Active == nil || row.State != store.DeliveryPending {
			t.Fatalf("row: %+v", row)
		}
	}
}
