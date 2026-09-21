package hosts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

func adminSubj(tenant string) authz.Subjects {
	return authz.Subjects{TenantID: tenant, UserID: "u1", Roles: []string{"admin"}, ActorKind: authz.ActorUser}
}

// seedHost resolves a host with the given synthetic identity into the store.
func seedHost(t *testing.T, m *memstore.Mem, tenant, hostname, hwUUID string) store.Host {
	t.Helper()
	h, err := m.ResolveHost(context.Background(), tenant, store.Host{
		Hostname: hostname, HardwareUUID: hwUUID, Manufacturer: "Acme", Model: "Box", OSName: "Linux",
	})
	if err != nil {
		t.Fatalf("seed host: %v", err)
	}
	return h
}

func TestGet_ReturnsHost(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	h := seedHost(t, m, tenant, "web01", "uuid-web01")
	svc := hosts.New(m)

	got, err := svc.Get(context.Background(), adminSubj(tenant), h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != h.ID || got.Hostname != "web01" {
		t.Errorf("Get returned %+v", got)
	}
	if got.IdentityKey != store.IdentityHardwareUUID {
		t.Errorf("identity_key = %q, want %q", got.IdentityKey, store.IdentityHardwareUUID)
	}
}

func TestGet_NotFoundMapped(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	_, err := svc.Get(context.Background(), adminSubj(store.NewID()), "missing")
	if !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want hosts.ErrNotFound", err)
	}
}

func TestGet_TenantRequired(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	_, err := svc.Get(context.Background(), authz.Subjects{}, "x")
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want authz.ErrForbidden", err)
	}
}

func TestList_FiltersByTenant(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	other := store.NewID()
	seedHost(t, m, tenant, "a", "uuid-a")
	seedHost(t, m, tenant, "b", "uuid-b")
	seedHost(t, m, other, "c", "uuid-c")
	svc := hosts.New(m)

	list, err := svc.List(context.Background(), adminSubj(tenant), store.HostFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("List returned %d hosts, want 2", len(list))
	}
}

func TestSetTags_ReplacesTags(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	h := seedHost(t, m, tenant, "tagme", "uuid-tag")
	svc := hosts.New(m)

	got, err := svc.SetTags(context.Background(), adminSubj(tenant), h.ID, map[string]string{"env": "prod"})
	if err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	if got.Tags["env"] != "prod" {
		t.Errorf("tags = %+v, want env=prod", got.Tags)
	}
}

func TestSetTags_NotFound(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	_, err := svc.SetTags(context.Background(), adminSubj(store.NewID()), "nope", nil)
	if !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want hosts.ErrNotFound", err)
	}
}

func TestRetire_SetsStatus(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	h := seedHost(t, m, tenant, "old", "uuid-old")
	svc := hosts.New(m)

	got, err := svc.Retire(context.Background(), adminSubj(tenant), h.ID)
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if got.Status != store.HostRetired {
		t.Errorf("status = %q, want %q", got.Status, store.HostRetired)
	}
}

func TestRetire_NotFound(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	if _, err := svc.Retire(context.Background(), adminSubj(store.NewID()), "x"); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want hosts.ErrNotFound", err)
	}
}

func TestDelete_RemovesHost(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	h := seedHost(t, m, tenant, "gone", "uuid-gone")
	svc := hosts.New(m)

	if err := svc.Delete(context.Background(), adminSubj(tenant), h.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(context.Background(), adminSubj(tenant), h.ID); !errors.Is(err, hosts.ErrNotFound) {
		t.Errorf("host still present after delete: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	if err := svc.Delete(context.Background(), adminSubj(store.NewID()), "x"); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("err = %v, want hosts.ErrNotFound", err)
	}
}

func TestResolve_UpsertsByIdentity(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	svc := hosts.New(m)
	svc.SetClock(func() time.Time { return time.Unix(1700000000, 0).UTC() })

	first, err := svc.Resolve(context.Background(), tenant, store.Host{Hostname: "h1", HardwareUUID: "uuid-1", OSName: "Linux"})
	if err != nil {
		t.Fatalf("Resolve first: %v", err)
	}
	// Same identity resolves to the same host (no duplicate).
	second, err := svc.Resolve(context.Background(), tenant, store.Host{Hostname: "h1-renamed", HardwareUUID: "uuid-1", OSVersion: "42"})
	if err != nil {
		t.Fatalf("Resolve second: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("identity upsert created a second host: %s vs %s", first.ID, second.ID)
	}
	if second.Hostname != "h1-renamed" {
		t.Errorf("merged hostname = %q, want h1-renamed", second.Hostname)
	}
}

func TestResolve_TenantRequired(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	if _, err := svc.Resolve(context.Background(), "", store.Host{Hostname: "x"}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want authz.ErrForbidden", err)
	}
}
