package repotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func teleportItem(tenant, enrollmentID, hostID, agentID, clusterID, state string, at time.Time) store.TeleportJoinItem {
	return store.TeleportJoinItem{ID: store.NewID(), TenantID: tenant, Source: "teleport", EnrollmentID: enrollmentID, HostID: hostID,
		AgentID: agentID, ClusterID: clusterID, State: state, Attempt: 1, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}
}

func teleportAudit(tenant, action, subject string) store.AuditRow {
	return store.AuditRow{ID: store.NewID(), TenantID: tenant, At: base, ActorKind: "service", ActorID: "teleport",
		Action: action, SubjectKind: "teleport_join", SubjectID: subject, Outcome: "ok", Detail: map[string]any{"state": "pending"}}
}

func teleportSupersedeAudit(old store.TeleportJoinItem, newID string) store.AuditRow {
	r := teleportAudit(old.TenantID, "teleport_join_superseded", old.ID)
	r.Detail = map[string]any{"superseded_by": newID}
	return r
}

// TeleportJoinContract runs the Teleport join storage contract (feature
// 038) against a fresh store from open.
func TeleportJoinContract(t *testing.T, open func(t *testing.T) repo.Store, audits AuditCounter) {
	ctx := context.Background()

	t.Run("create, get, idempotency key, isolation", func(t *testing.T) {
		st := open(t)
		host, agent, enr, cluster := store.NewID(), store.NewID(), store.NewID(), store.NewID()
		i := teleportItem(TenantA, enr, host, agent, cluster, store.DeliveryPending, base)
		sup, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: i, Audit: []store.AuditRow{teleportAudit(TenantA, "teleport_join_created", i.ID)},
			SupersedeAudit: teleportSupersedeAudit})
		if err != nil || len(sup) != 0 {
			t.Fatalf("create: %v %v", sup, err)
		}
		if n := audits(t, TenantA, "teleport_join_created", i.ID); n != 1 {
			t.Fatalf("created audit rows = %d", n)
		}
		g, err := st.GetTeleportItem(ctx, TenantA, i.ID)
		if err != nil || g.EnrollmentID != enr || g.HostID != host || g.AgentID != agent || g.ClusterID != cluster ||
			g.State != store.DeliveryPending || g.NodeHostUUID != "" || g.Attempt != 1 || g.Fetches != 0 || g.Source != "teleport" ||
			!g.CreatedAt.Equal(base) || !g.ExpiresAt.Equal(i.ExpiresAt) || g.DeliveredAt != nil || g.FetchedAt != nil || g.ReportedAt != nil {
			t.Fatalf("get: %+v %v", g, err)
		}
		if g, err := st.GetTeleportItemByEnrollment(ctx, TenantA, "teleport", enr); err != nil || g.ID != i.ID {
			t.Fatalf("by enrollment: %v %v", g.ID, err)
		}
		for name, err := range map[string]error{
			"other source": func() error { _, e := st.GetTeleportItemByEnrollment(ctx, TenantA, "other", enr); return e }(),
			"tenant B key": func() error { _, e := st.GetTeleportItemByEnrollment(ctx, TenantB, "teleport", enr); return e }(),
			"tenant B id":  func() error { _, e := st.GetTeleportItem(ctx, TenantB, i.ID); return e }(),
			"unknown id":   func() error { _, e := st.GetTeleportItem(ctx, TenantA, store.NewID()); return e }(),
		} {
			if !errors.Is(err, repo.ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		// Same enrollment id from the same source: conflict, nothing written.
		dup := teleportItem(TenantA, enr, store.NewID(), "", store.NewID(), store.DeliveryPending, base)
		if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: dup}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("duplicate enrollment id: %v", err)
		}
		if _, err := st.GetTeleportItem(ctx, TenantA, dup.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("conflicting create left an item: %v", err)
		}
		// The same id reused: conflict.
		again := i
		again.EnrollmentID = store.NewID()
		if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: again}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("reused item id: %v", err)
		}
		// The same enrollment id in another tenant is another item.
		if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: teleportItem(TenantB, enr, host, "", cluster, store.DeliveryPending, base)}); err != nil {
			t.Fatalf("tenant B same enrollment id: %v", err)
		}
	})

	t.Run("supersede the active item of the same host and cluster", func(t *testing.T) {
		st := open(t)
		host, agent, cluster := store.NewID(), store.NewID(), store.NewID()
		old := teleportItem(TenantA, store.NewID(), host, agent, cluster, store.TeleportInstalling, base)
		if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: old}); err != nil {
			t.Fatal(err)
		}
		other := teleportItem(TenantA, store.NewID(), host, agent, store.NewID(), store.DeliveryPending, base)
		if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: other}); err != nil {
			t.Fatal(err)
		}
		term := teleportItem(TenantA, store.NewID(), host, agent, cluster, store.DeliveryFailed, base)
		if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: term}); err != nil {
			t.Fatalf("terminal item next to an active one: %v", err)
		}
		at := base.Add(time.Minute)
		next := teleportItem(TenantA, store.NewID(), host, agent, cluster, store.DeliveryPending, at)
		sup, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: next, SupersedeAudit: teleportSupersedeAudit})
		if err != nil || len(sup) != 1 || sup[0].ID != old.ID || sup[0].State != store.DeliverySuperseded {
			t.Fatalf("supersede: %+v %v", sup, err)
		}
		if g, _ := st.GetTeleportItem(ctx, TenantA, old.ID); g.State != store.DeliverySuperseded || !g.UpdatedAt.Equal(at) {
			t.Fatalf("old item = %+v", g)
		}
		if g, _ := st.GetTeleportItem(ctx, TenantA, other.ID); g.State != store.DeliveryPending {
			t.Fatalf("other cluster touched: %+v", g)
		}
		if n := audits(t, TenantA, "teleport_join_superseded", old.ID); n != 1 {
			t.Fatalf("superseded audit rows = %d", n)
		}
		// Re-activating a terminal item next to the active one conflicts.
		if _, err := st.UpdateTeleportItem(ctx, TenantA, term.ID, func(i *store.TeleportJoinItem) ([]store.AuditRow, error) {
			i.State = store.TeleportInstalling
			return nil, nil
		}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("second active item: %v", err)
		}
	})

	t.Run("update, agent replay, stale, purge", func(t *testing.T) {
		st := open(t)
		agent := store.NewID()
		a1 := teleportItem(TenantA, store.NewID(), store.NewID(), agent, store.NewID(), store.DeliveryPending, base)
		a2 := teleportItem(TenantA, store.NewID(), store.NewID(), agent, store.NewID(), store.DeliveryFetched, base.Add(time.Second))
		a3 := teleportItem(TenantA, store.NewID(), store.NewID(), agent, store.NewID(), store.TeleportJoined, base.Add(2*time.Second))
		a4 := teleportItem(TenantA, store.NewID(), store.NewID(), store.NewID(), store.NewID(), store.DeliveryPending, base)
		a5 := teleportItem(TenantA, store.NewID(), store.NewID(), agent, store.NewID(), store.TeleportInstalling, base.Add(3*time.Second))
		for _, i := range []store.TeleportJoinItem{a1, a2, a3, a4, a5} {
			if _, err := st.CreateTeleportItem(ctx, repo.NewTeleportItem{Item: i}); err != nil {
				t.Fatal(err)
			}
		}
		list, err := st.ListActiveTeleportItemsForAgent(ctx, TenantA, agent, 0)
		if err != nil || len(list) != 3 || list[0].ID != a1.ID || list[1].ID != a2.ID || list[2].ID != a5.ID {
			t.Fatalf("agent active = %+v %v", list, err)
		}
		if list, _ := st.ListActiveTeleportItemsForAgent(ctx, TenantA, agent, 1); len(list) != 1 || list[0].ID != a1.ID {
			t.Fatalf("limit = %+v", list)
		}
		if list, _ := st.ListActiveTeleportItemsForAgent(ctx, TenantB, agent, 0); len(list) != 0 {
			t.Fatalf("tenant B sees %+v", list)
		}
		// Update with audit; a failing fn changes nothing.
		when := base.Add(time.Hour)
		uuid := "5b0c7a4e-2f61-4c1e-9d3a-0e4f6a7b8c9d"
		got, err := st.UpdateTeleportItem(ctx, TenantA, a1.ID, func(i *store.TeleportJoinItem) ([]store.AuditRow, error) {
			i.State, i.Fetches, i.UpdatedAt, i.FetchedAt, i.NodeHostUUID = store.DeliveryFetched, 1, when, &when, uuid
			return []store.AuditRow{teleportAudit(TenantA, "teleport_join_fetched", i.ID)}, nil
		})
		if err != nil || got.State != store.DeliveryFetched || got.Fetches != 1 || got.FetchedAt == nil || !got.FetchedAt.Equal(when) ||
			got.NodeHostUUID != uuid {
			t.Fatalf("update: %+v %v", got, err)
		}
		if n := audits(t, TenantA, "teleport_join_fetched", a1.ID); n != 1 {
			t.Fatalf("fetched audit rows = %d", n)
		}
		boom := errors.New("boom")
		if _, err := st.UpdateTeleportItem(ctx, TenantA, a1.ID, func(i *store.TeleportJoinItem) ([]store.AuditRow, error) {
			i.State = store.DeliveryFailed
			return nil, boom
		}); !errors.Is(err, boom) {
			t.Fatalf("fn error: %v", err)
		}
		if g, _ := st.GetTeleportItem(ctx, TenantA, a1.ID); g.State != store.DeliveryFetched {
			t.Fatalf("failed update changed the item: %+v", g)
		}
		if _, err := st.UpdateTeleportItem(ctx, TenantB, a1.ID, func(*store.TeleportJoinItem) ([]store.AuditRow, error) { return nil, nil }); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("tenant B update: %v", err)
		}
		// Stale by report: a2 (fetched) and a5 (installing) not updated since.
		stale, err := st.ListStaleTeleportItems(ctx, base.Add(2*time.Minute), base.Add(time.Minute), 0)
		if err != nil || len(stale) != 2 || stale[0].ID != a2.ID || stale[1].ID != a5.ID {
			t.Fatalf("stale by report = %+v %v", stale, err)
		}
		// Stale by expiry: a1 and a2 (fetched) and a4 (pending); an
		// installing item is only swept by its report timeout.
		now := base.Add(25 * time.Hour)
		stale, err = st.ListStaleTeleportItems(ctx, now, base, 0)
		if err != nil || len(stale) != 3 {
			t.Fatalf("stale by expiry = %+v %v", stale, err)
		}
		for _, s := range stale {
			if s.ID == a5.ID {
				t.Fatal("installing item expired")
			}
		}
		if stale, _ := st.ListStaleTeleportItems(ctx, now, base, 1); len(stale) != 1 {
			t.Fatalf("stale limit = %d", len(stale))
		}
		// Purge: only terminal items updated before the cut.
		n, err := st.PurgeTeleportItems(ctx, base.Add(time.Hour))
		if err != nil || n != 1 {
			t.Fatalf("purge = %d %v", n, err)
		}
		if _, err := st.GetTeleportItem(ctx, TenantA, a3.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("terminal item survived the purge: %v", err)
		}
		if _, err := st.GetTeleportItem(ctx, TenantA, a4.ID); err != nil {
			t.Fatalf("active item purged: %v", err)
		}
	})
}
