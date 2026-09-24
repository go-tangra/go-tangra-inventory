package hosts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/authz"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestForbidden_AllMethods exercises the RequireTenant guard on every method.
func TestForbidden_AllMethods(t *testing.T) {
	m := memstore.New()
	svc := hosts.New(m)
	ctx := context.Background()
	empty := authz.Subjects{} // no tenant => forbidden

	if _, err := svc.List(ctx, empty, store.HostFilter{}); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("List: %v", err)
	}
	if _, err := svc.SetTags(ctx, empty, "x", nil); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("SetTags: %v", err)
	}
	if _, err := svc.Retire(ctx, empty, "x"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("Retire: %v", err)
	}
	if err := svc.Delete(ctx, empty, "x"); !errors.Is(err, authz.ErrForbidden) {
		t.Errorf("Delete: %v", err)
	}
}

// TestStoreErrorsPropagate ensures injected store failures surface from each path.
func TestStoreErrorsPropagate(t *testing.T) {
	m := memstore.New()
	tenant := store.NewID()
	h := seedHost(t, m, tenant, "h", "uuid-h")
	svc := hosts.New(m)
	ctx := context.Background()
	subj := adminSubj(tenant)

	m.FailNext("SetHostTags")
	if _, err := svc.SetTags(ctx, subj, h.ID, nil); err == nil {
		t.Errorf("SetTags: expected injected error")
	}
	m.FailNext("RetireHost")
	if _, err := svc.Retire(ctx, subj, h.ID); err == nil {
		t.Errorf("Retire: expected injected error")
	}
	m.FailNext("DeleteHost")
	if err := svc.Delete(ctx, subj, h.ID); err == nil {
		t.Errorf("Delete: expected injected error")
	}
	m.FailNext("ResolveHost")
	if _, err := svc.Resolve(ctx, tenant, store.Host{Hostname: "h2", HardwareUUID: "uuid-h2"}); err == nil {
		t.Errorf("Resolve: expected injected error")
	}
}
