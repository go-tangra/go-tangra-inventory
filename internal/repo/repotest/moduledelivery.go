package repotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func moduleItem(tenant, deliveryID, hostID, agentID, module, state string, at time.Time) store.ModuleDeliveryItem {
	return store.ModuleDeliveryItem{ID: store.NewID(), TenantID: tenant, Source: "gateway", DeliveryID: deliveryID, HostID: hostID,
		AgentID: agentID, Module: module, Version: "1.2.0", State: state, HookExitCode: -1, Attempt: 1,
		CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
}

func moduleAudit(tenant, action, subject string) store.AuditRow {
	return store.AuditRow{ID: store.NewID(), TenantID: tenant, At: base, ActorKind: "service", ActorID: "gateway",
		Action: action, SubjectKind: "module_delivery", SubjectID: subject, Outcome: "ok", Detail: map[string]any{"module": "sms-gw"}}
}

func moduleSupersedeAudit(old store.ModuleDeliveryItem, newID string) store.AuditRow {
	r := moduleAudit(old.TenantID, "module_delivery_superseded", old.ID)
	r.Detail = map[string]any{"superseded_by": newID}
	return r
}

// ModuleDeliveryContract runs the module delivery storage contract
// (feature 037, T004) against a fresh store from open.
func ModuleDeliveryContract(t *testing.T, open func(t *testing.T) repo.Store, audits AuditCounter) {
	ctx := context.Background()

	t.Run("create, get, idempotency key, isolation", func(t *testing.T) {
		st := open(t)
		host, agent, del := store.NewID(), store.NewID(), store.NewID()
		i := moduleItem(TenantA, del, host, agent, "sms-gw", store.DeliveryPending, base)
		sup, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: i, Audit: []store.AuditRow{moduleAudit(TenantA, "module_delivery_created", i.ID)},
			SupersedeAudit: moduleSupersedeAudit})
		if err != nil || len(sup) != 0 {
			t.Fatalf("create: %v %v", sup, err)
		}
		if n := audits(t, TenantA, "module_delivery_created", i.ID); n != 1 {
			t.Fatalf("created audit rows = %d", n)
		}
		g, err := st.GetModuleItem(ctx, TenantA, i.ID)
		if err != nil || g.DeliveryID != del || g.HostID != host || g.AgentID != agent || g.Module != "sms-gw" || g.Version != "1.2.0" ||
			g.State != store.DeliveryPending || g.HookExitCode != -1 || g.Attempt != 1 || g.Fetches != 0 || g.Source != "gateway" ||
			!g.CreatedAt.Equal(base) || !g.ExpiresAt.Equal(i.ExpiresAt) || g.DeliveredAt != nil || g.FetchedAt != nil || g.ReportedAt != nil {
			t.Fatalf("get: %+v %v", g, err)
		}
		if g, err := st.GetModuleItemByDelivery(ctx, TenantA, "gateway", del); err != nil || g.ID != i.ID {
			t.Fatalf("by delivery: %v %v", g.ID, err)
		}
		for name, err := range map[string]error{
			"other source": func() error { _, e := st.GetModuleItemByDelivery(ctx, TenantA, "other", del); return e }(),
			"tenant B key": func() error { _, e := st.GetModuleItemByDelivery(ctx, TenantB, "gateway", del); return e }(),
			"tenant B id":  func() error { _, e := st.GetModuleItem(ctx, TenantB, i.ID); return e }(),
			"unknown id":   func() error { _, e := st.GetModuleItem(ctx, TenantA, store.NewID()); return e }(),
		} {
			if !errors.Is(err, repo.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		// Same delivery id from the same source: conflict, nothing written.
		dup := moduleItem(TenantA, del, store.NewID(), "", "other-mod", store.DeliveryPending, base)
		if _, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: dup}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("duplicate delivery id: %v", err)
		}
		if _, err := st.GetModuleItem(ctx, TenantA, dup.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("conflicting create left an item: %v", err)
		}
		// The same delivery id in another tenant is another item.
		if _, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: moduleItem(TenantB, del, host, "", "sms-gw", store.DeliveryPending, base)}); err != nil {
			t.Fatalf("tenant B same delivery id: %v", err)
		}
	})

	t.Run("supersede the active item of the same host and module", func(t *testing.T) {
		st := open(t)
		host, agent := store.NewID(), store.NewID()
		old := moduleItem(TenantA, store.NewID(), host, agent, "sms-gw", store.DeliveryDelivered, base)
		if _, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: old}); err != nil {
			t.Fatal(err)
		}
		other := moduleItem(TenantA, store.NewID(), host, agent, "mail", store.DeliveryPending, base)
		if _, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: other}); err != nil {
			t.Fatal(err)
		}
		term := moduleItem(TenantA, store.NewID(), host, agent, "sms-gw", store.DeliveryFailed, base)
		if _, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: term}); err != nil {
			t.Fatalf("terminal item next to an active one: %v", err)
		}
		at := base.Add(time.Minute)
		next := moduleItem(TenantA, store.NewID(), host, agent, "sms-gw", store.DeliveryPending, at)
		sup, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: next, SupersedeAudit: moduleSupersedeAudit})
		if err != nil || len(sup) != 1 || sup[0].ID != old.ID || sup[0].State != store.DeliverySuperseded {
			t.Fatalf("supersede: %+v %v", sup, err)
		}
		if g, _ := st.GetModuleItem(ctx, TenantA, old.ID); g.State != store.DeliverySuperseded || !g.UpdatedAt.Equal(at) {
			t.Fatalf("old item = %+v", g)
		}
		if g, _ := st.GetModuleItem(ctx, TenantA, other.ID); g.State != store.DeliveryPending {
			t.Fatalf("other module touched: %+v", g)
		}
		if n := audits(t, TenantA, "module_delivery_superseded", old.ID); n != 1 {
			t.Fatalf("superseded audit rows = %d", n)
		}
		// Re-activating a terminal item next to the active one conflicts.
		if _, err := st.UpdateModuleItem(ctx, TenantA, term.ID, func(i *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
			i.State = store.DeliveryPending
			return nil, nil
		}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("second active item: %v", err)
		}
	})

	t.Run("update, agent replay, stale, purge", func(t *testing.T) {
		st := open(t)
		agent := store.NewID()
		a1 := moduleItem(TenantA, store.NewID(), store.NewID(), agent, "sms-gw", store.DeliveryPending, base)
		a2 := moduleItem(TenantA, store.NewID(), store.NewID(), agent, "sms-gw", store.DeliveryFetched, base.Add(time.Second))
		a3 := moduleItem(TenantA, store.NewID(), store.NewID(), agent, "sms-gw", store.DeliveryInstalled, base.Add(2*time.Second))
		a4 := moduleItem(TenantA, store.NewID(), store.NewID(), store.NewID(), "sms-gw", store.DeliveryPending, base)
		for _, i := range []store.ModuleDeliveryItem{a1, a2, a3, a4} {
			if _, err := st.CreateModuleItem(ctx, repo.NewModuleItem{Item: i}); err != nil {
				t.Fatal(err)
			}
		}
		list, err := st.ListActiveModuleItemsForAgent(ctx, TenantA, agent, 0)
		if err != nil || len(list) != 2 || list[0].ID != a1.ID || list[1].ID != a2.ID {
			t.Fatalf("agent active = %+v %v", list, err)
		}
		if list, _ := st.ListActiveModuleItemsForAgent(ctx, TenantA, agent, 1); len(list) != 1 || list[0].ID != a1.ID {
			t.Fatalf("limit = %+v", list)
		}
		if list, _ := st.ListActiveModuleItemsForAgent(ctx, TenantB, agent, 0); len(list) != 0 {
			t.Fatalf("tenant B sees %+v", list)
		}
		// Update with audit; a failing fn changes nothing.
		when := base.Add(time.Hour)
		got, err := st.UpdateModuleItem(ctx, TenantA, a1.ID, func(i *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
			i.State, i.Fetches, i.UpdatedAt, i.FetchedAt, i.HookExitCode = store.DeliveryFetched, 1, when, &when, 0
			return []store.AuditRow{moduleAudit(TenantA, "module_delivery_fetched", i.ID)}, nil
		})
		if err != nil || got.State != store.DeliveryFetched || got.Fetches != 1 || got.FetchedAt == nil || !got.FetchedAt.Equal(when) {
			t.Fatalf("update: %+v %v", got, err)
		}
		if n := audits(t, TenantA, "module_delivery_fetched", a1.ID); n != 1 {
			t.Fatalf("fetched audit rows = %d", n)
		}
		boom := errors.New("boom")
		if _, err := st.UpdateModuleItem(ctx, TenantA, a1.ID, func(i *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
			i.State = store.DeliveryFailed
			return nil, boom
		}); !errors.Is(err, boom) {
			t.Fatalf("fn error: %v", err)
		}
		if g, _ := st.GetModuleItem(ctx, TenantA, a1.ID); g.State != store.DeliveryFetched {
			t.Fatalf("failed update changed the item: %+v", g)
		}
		if _, err := st.UpdateModuleItem(ctx, TenantB, a1.ID, func(*store.ModuleDeliveryItem) ([]store.AuditRow, error) { return nil, nil }); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("tenant B update: %v", err)
		}
		// Stale: a2 fetched and not updated since; a4 expired.
		now := base.Add(25 * time.Hour)
		stale, err := st.ListStaleModuleItems(ctx, base.Add(2*time.Minute), base.Add(time.Minute), 0)
		if err != nil || len(stale) != 1 || stale[0].ID != a2.ID {
			t.Fatalf("stale by report = %+v %v", stale, err)
		}
		stale, err = st.ListStaleModuleItems(ctx, now, base, 0)
		if err != nil || len(stale) != 3 {
			t.Fatalf("stale by expiry = %+v %v", stale, err)
		}
		if stale, _ := st.ListStaleModuleItems(ctx, now, base, 2); len(stale) != 2 {
			t.Fatalf("stale limit = %d", len(stale))
		}
		// Purge: only terminal items updated before the cut.
		n, err := st.PurgeModuleItems(ctx, base.Add(time.Hour))
		if err != nil || n != 1 {
			t.Fatalf("purge = %d %v", n, err)
		}
		if _, err := st.GetModuleItem(ctx, TenantA, a3.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("terminal item survived the purge: %v", err)
		}
		if _, err := st.GetModuleItem(ctx, TenantA, a4.ID); err != nil {
			t.Fatalf("active item purged: %v", err)
		}
	})
}
