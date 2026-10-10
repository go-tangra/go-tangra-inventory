package grpcapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/moduledelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const gatewayID = "spiffe://example.org/svc/gateway"

type moduleKit struct {
	srv  *ModuleDeliveryServer
	mem  *memstore.Mem
	host store.Host
}

func newModuleKit(t *testing.T, enabled bool, caps ...string) moduleKit {
	t.Helper()
	mem := memstore.New()
	svc := moduledelivery.New(mem, nil, registry.NewMemory(), moduledelivery.Config{Enabled: enabled, ReportTimeout: 30 * time.Minute})
	ctx := context.Background()
	h, _ := mem.ResolveHost(ctx, tenantA, store.Host{Hostname: "gw-1", MachineID: "m1", OSName: "Debian"})
	a := store.Agent{ID: store.NewID(), TenantID: tenantA, HostID: h.ID, AgentVersion: "4.8.0"}
	_ = mem.CreateAgent(ctx, a)
	_ = mem.SetAgentPlatform(ctx, a.ID, "linux", "amd64", "deb", caps, time.Now())
	return moduleKit{srv: &ModuleDeliveryServer{Svc: svc, Sources: []string{"gateway"}}, mem: mem, host: h}
}

func moduleReq(hostID string) *invv1.CreateModuleDeliveryRequest {
	return &invv1.CreateModuleDeliveryRequest{TenantId: tenantA, DeliveryId: "0190f7c2-6a3e-7c1a-9b2e-000000000037", HostId: hostID,
		Module: "sms-gw", Version: "1.2.0", ExpiresAt: time.Now().Add(time.Hour).Unix()}
}

// TestModuleDeliveryMapping (T010): create, get and the target list map
// their messages; a replay says created false.
func TestModuleDeliveryMapping(t *testing.T) {
	k := newModuleKit(t, true, store.CapModuleV1)
	ctx := context.Background()
	withFakeCaller(t, gatewayID, true)
	d, err := k.srv.CreateModuleDelivery(ctx, moduleReq(k.host.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !d.GetCreated() || d.GetItemId() == "" || d.GetDeliveryId() != moduleReq("").GetDeliveryId() || d.GetTenantId() != tenantA ||
		d.GetHostId() != k.host.ID || d.GetHostname() != "gw-1" || d.GetModule() != "sms-gw" || d.GetVersion() != "1.2.0" ||
		d.GetState() != invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_PENDING || d.GetHookExitCode() != -1 || d.GetAgentOnline() ||
		d.GetCreatedAt() == 0 || d.GetUpdatedAt() == 0 || d.GetExpiresAt() <= d.GetCreatedAt() {
		t.Fatalf("delivery = %v", d)
	}
	again, err := k.srv.CreateModuleDelivery(ctx, moduleReq(k.host.ID))
	if err != nil || again.GetCreated() || again.GetItemId() != d.GetItemId() {
		t.Fatalf("replay = %v %v", again, err)
	}
	got, err := k.srv.GetModuleDelivery(ctx, &invv1.GetModuleDeliveryRequest{TenantId: tenantA, DeliveryId: d.GetDeliveryId()})
	if err != nil || got.GetItemId() != d.GetItemId() || got.GetCreated() {
		t.Fatalf("get = %v %v", got, err)
	}
	if _, err := k.srv.GetModuleDelivery(ctx, &invv1.GetModuleDeliveryRequest{TenantId: tenantA, DeliveryId: store.NewID()}); code(err) != codes.NotFound {
		t.Fatalf("unknown: %v", err)
	}
	ts, err := k.srv.ListModuleTargets(ctx, &invv1.ListModuleTargetsRequest{TenantId: strings.ToUpper(tenantA), Query: "gw"})
	if err != nil || len(ts.GetHosts()) != 1 || ts.GetTruncated() {
		t.Fatalf("targets = %v %v", ts, err)
	}
	h := ts.GetHosts()[0]
	if h.GetHostId() != k.host.ID || h.GetHostname() != "gw-1" || h.GetOsName() != "Debian" || h.GetCapability() != "enabled" || h.GetAgentOnline() {
		t.Fatalf("target = %v", h)
	}
	if _, err := k.srv.ListModuleTargets(ctx, &invv1.ListModuleTargetsRequest{TenantId: tenantA, Limit: 500}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad limit: %v", err)
	}
}

// TestModuleDeliveryErrors (T005/T010): disabled and ineligible hosts are
// FailedPrecondition with exactly the reason token; validation is
// InvalidArgument with the field; unknown hosts NotFound.
func TestModuleDeliveryErrors(t *testing.T) {
	ctx := context.Background()
	withFakeCaller(t, gatewayID, true)
	k := newModuleKit(t, false, store.CapModuleV1)
	_, err := k.srv.CreateModuleDelivery(ctx, moduleReq(k.host.ID))
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "disabled_on_server" {
		t.Fatalf("disabled: %v", err)
	}
	k = newModuleKit(t, true) // agent without module.v1, version 4.8.0
	_, err = k.srv.CreateModuleDelivery(ctx, moduleReq(k.host.ID))
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "disabled_on_host" {
		t.Fatalf("ineligible: %v", err)
	}
	req := moduleReq(k.host.ID)
	req.Module = "Bad"
	_, err = k.srv.CreateModuleDelivery(ctx, req)
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument || st.Message() != "invalid module" {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := k.srv.CreateModuleDelivery(ctx, moduleReq(store.NewID())); code(err) != codes.NotFound {
		t.Fatalf("unknown host: %v", err)
	}
	k.mem.FailNext("GetModuleItemByDelivery")
	if _, err := k.srv.CreateModuleDelivery(ctx, moduleReq(k.host.ID)); code(err) != codes.Unavailable {
		t.Fatalf("store failure: %v", err)
	}
}

// TestModuleDeliverySourceCheck (T005): only configured SPIFFE services; a
// refused peer is audited; the tenant must be a uuid.
func TestModuleDeliverySourceCheck(t *testing.T) {
	k := newModuleKit(t, true, store.CapModuleV1)
	ctx := context.Background()
	withFakeCaller(t, deployerID, true)
	if _, err := k.srv.CreateModuleDelivery(ctx, moduleReq(k.host.ID)); code(err) != codes.PermissionDenied {
		t.Fatalf("deployer: %v", err)
	}
	rows := k.mem.AuditRows()
	if len(rows) != 1 || rows[0].Action != "module_delivery_refused" || rows[0].ActorID != "deployer" || rows[0].Reason != "source_not_allowed" {
		t.Fatalf("refusal audit = %+v", rows)
	}
	withFakeCaller(t, "spiffe://example.org/node/x", true)
	if _, err := k.srv.GetModuleDelivery(ctx, &invv1.GetModuleDeliveryRequest{TenantId: tenantB}); code(err) != codes.PermissionDenied {
		t.Fatalf("non-service: %v", err)
	}
	if rows := k.mem.AuditRows(); rows[len(rows)-1].ActorID != "unknown" || rows[len(rows)-1].TenantID != tenantB {
		t.Fatalf("unknown actor row = %+v", rows[len(rows)-1])
	}
	n := len(k.mem.AuditRows())
	if _, err := k.srv.ListModuleTargets(ctx, &invv1.ListModuleTargetsRequest{TenantId: "t"}); code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if len(k.mem.AuditRows()) != n {
		t.Fatal("audited without a tenant")
	}
	withFakeCaller(t, "", false)
	if _, err := k.srv.ListModuleTargets(ctx, &invv1.ListModuleTargetsRequest{TenantId: tenantA}); code(err) != codes.Unauthenticated {
		t.Fatalf("no identity: %v", err)
	}
	withFakeCaller(t, gatewayID, true)
	if _, err := k.srv.GetModuleDelivery(ctx, &invv1.GetModuleDeliveryRequest{TenantId: "nope"}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad tenant: %v", err)
	}
	if _, err := k.srv.CreateModuleDelivery(ctx, &invv1.CreateModuleDeliveryRequest{TenantId: "nope"}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad tenant: %v", err)
	}
}

func TestRegisterModuleDelivery(t *testing.T) {
	k := newModuleKit(t, false)
	gs := grpc.NewServer()
	Register(gs, Deps{ModuleDelivery: k.srv.Svc, ModuleSources: []string{"gateway"}})
	if _, ok := gs.GetServiceInfo()["inventory.v1.ModuleDeliveryService"]; !ok {
		t.Fatal("service not registered")
	}
	gs = grpc.NewServer()
	Register(gs, Deps{})
	if _, ok := gs.GetServiceInfo()["inventory.v1.ModuleDeliveryService"]; ok {
		t.Fatal("registered without a service")
	}
}
