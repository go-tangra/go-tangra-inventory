package stats_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/stats"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

func seedHost(t *testing.T, m *memstore.Mem, tenant, hostname, hwUUID string, lastSeen time.Time) store.Host {
	t.Helper()
	h, err := m.ResolveHost(context.Background(), tenant, store.Host{
		Hostname: hostname, HardwareUUID: hwUUID, Manufacturer: "Acme", OSName: "Linux",
		OSVersion: "42", LastSeen: lastSeen,
	})
	if err != nil {
		t.Fatalf("seed host: %v", err)
	}
	return h
}

func TestTenant_Rollup(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	now := time.Unix(1_700_000_000, 0).UTC()
	seedHost(t, m, tenant, "a", "uuid-a", now)
	seedHost(t, m, tenant, "b", "uuid-b", now)

	svc := stats.New(m)
	svc.SetClock(func() time.Time { return now })
	subj := authz.Subjects{TenantID: tenant, Roles: []string{"admin"}, ActorKind: authz.ActorUser}

	st, err := svc.Tenant(context.Background(), subj)
	if err != nil {
		t.Fatalf("Tenant: %v", err)
	}
	if st.HostsTotal != 2 {
		t.Errorf("HostsTotal = %d, want 2", st.HostsTotal)
	}
	if st.HostsByOS["Linux"] != 2 {
		t.Errorf("HostsByOS[Linux] = %d, want 2", st.HostsByOS["Linux"])
	}
}

func TestTenant_StaleCutoffApplied(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	now := time.Unix(1_700_000_000, 0).UTC()
	// One fresh, one seen long ago.
	seedHost(t, m, tenant, "fresh", "uuid-fresh", now)
	seedHost(t, m, tenant, "old", "uuid-old", now.Add(-30*24*time.Hour))

	svc := stats.New(m)
	svc.SetStaleAfter(7 * 24 * time.Hour)
	svc.SetClock(func() time.Time { return now })
	subj := authz.Subjects{TenantID: tenant, Roles: []string{"admin"}, ActorKind: authz.ActorUser}

	st, err := svc.Tenant(context.Background(), subj)
	if err != nil {
		t.Fatalf("Tenant: %v", err)
	}
	if st.StaleHosts != 1 {
		t.Errorf("StaleHosts = %d, want 1", st.StaleHosts)
	}
}

func TestTenant_Forbidden(t *testing.T) {
	m := memstore.New()
	svc := stats.New(m)
	_, err := svc.Tenant(context.Background(), authz.Subjects{})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want authz.ErrForbidden", err)
	}
}

func TestSystem_RequiresAdmin(t *testing.T) {
	m := memstore.New()
	svc := stats.New(m)
	// Non-admin caller.
	_, err := svc.System(context.Background(), authz.Subjects{TenantID: "t1", ActorKind: authz.ActorUser})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want authz.ErrForbidden", err)
	}
}

func TestSystem_PerTenantBreakdown(t *testing.T) {
	m := memstore.New()
	now := time.Unix(1_700_000_000, 0).UTC()
	t1, t2 := store.NewID(), store.NewID()
	seedHost(t, m, t1, "a", "uuid-a", now)
	seedHost(t, m, t2, "b", "uuid-b", now)
	seedHost(t, m, t2, "c", "uuid-c", now)

	svc := stats.New(m)
	svc.SetClock(func() time.Time { return now })
	admin := authz.Subjects{TenantID: t1, Roles: []string{"admin"}, ActorKind: authz.ActorUser}

	all, err := svc.System(context.Background(), admin)
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("tenants in system stats = %d, want 2", len(all))
	}
	if all[t1].HostsTotal != 1 {
		t.Errorf("t1 HostsTotal = %d, want 1", all[t1].HostsTotal)
	}
	if all[t2].HostsTotal != 2 {
		t.Errorf("t2 HostsTotal = %d, want 2", all[t2].HostsTotal)
	}
}
