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
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/teleportjoin"
)

const (
	teleportID    = "spiffe://example.org/svc/teleport"
	tpEnrollment  = "0190f7c2-6a3e-7c1a-9b2e-000000000038"
	tpClusterUUID = "0190f7c2-6a3e-7c1a-9b2e-000000000c1a"
)

type teleportKit struct {
	srv  *TeleportJoinServer
	mem  *memstore.Mem
	host store.Host
}

func newTeleportKit(t *testing.T, enabled bool, os string, caps ...string) teleportKit {
	t.Helper()
	mem := memstore.New()
	svc := teleportjoin.New(mem, nil, registry.NewMemory(), teleportjoin.Config{Enabled: enabled, ReportTimeout: 30 * time.Minute})
	ctx := context.Background()
	h, _ := mem.ResolveHost(ctx, tenantA, store.Host{Hostname: "node-1", MachineID: "m1", OSName: "Debian", OSVersion: "12", OSArch: "amd64",
		Manufacturer: "Dell Inc.", Model: "R650", Tags: map[string]string{"env": "prod"}})
	a := store.Agent{ID: store.NewID(), TenantID: tenantA, HostID: h.ID, AgentVersion: "4.9.0"}
	_ = mem.CreateAgent(ctx, a)
	_ = mem.SetAgentPlatform(ctx, a.ID, os, "amd64", "deb", caps, time.Now())
	return teleportKit{srv: &TeleportJoinServer{Svc: svc, Sources: []string{"teleport"}}, mem: mem, host: h}
}

func teleportReq(hostID string) *invv1.CreateTeleportJoinRequest {
	return &invv1.CreateTeleportJoinRequest{TenantId: tenantA, EnrollmentId: tpEnrollment, HostId: hostID, ClusterId: tpClusterUUID,
		ExpiresAt: time.Now().Add(time.Hour).Unix()}
}

// TestTeleportJoinMapping: create, get, cancel and the target list map
// their messages; a replay says created false.
func TestTeleportJoinMapping(t *testing.T) {
	k := newTeleportKit(t, true, "linux", store.CapTeleportV1)
	ctx := context.Background()
	withFakeCaller(t, teleportID, true)
	j, err := k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !j.GetCreated() || j.GetItemId() == "" || j.GetEnrollmentId() != tpEnrollment || j.GetTenantId() != tenantA ||
		j.GetHostId() != k.host.ID || j.GetHostname() != "node-1" || j.GetClusterId() != tpClusterUUID ||
		j.GetState() != invv1.TeleportJoinState_TELEPORT_JOIN_STATE_PENDING || j.GetAgentOnline() || j.GetNodeHostUuid() != "" ||
		j.GetCreatedAt() == 0 || j.GetUpdatedAt() == 0 || j.GetExpiresAt() <= j.GetCreatedAt() {
		t.Fatalf("join = %v", j)
	}
	again, err := k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID))
	if err != nil || again.GetCreated() || again.GetItemId() != j.GetItemId() {
		t.Fatalf("replay = %v %v", again, err)
	}
	got, err := k.srv.GetTeleportJoins(ctx, &invv1.GetTeleportJoinsRequest{TenantId: tenantA, EnrollmentIds: []string{tpEnrollment, store.NewID()}})
	if err != nil || len(got.GetItems()) != 1 || got.GetItems()[0].GetItemId() != j.GetItemId() || got.GetItems()[0].GetCreated() {
		t.Fatalf("get = %v %v", got, err)
	}
	if _, err := k.srv.GetTeleportJoins(ctx, &invv1.GetTeleportJoinsRequest{TenantId: tenantA}); code(err) != codes.InvalidArgument {
		t.Fatalf("empty batch: %v", err)
	}
	ts, err := k.srv.ListTeleportJoinTargets(ctx, &invv1.ListTeleportJoinTargetsRequest{TenantId: strings.ToUpper(tenantA), Query: "node"})
	if err != nil || len(ts.GetHosts()) != 1 || ts.GetTruncated() {
		t.Fatalf("targets = %v %v", ts, err)
	}
	h := ts.GetHosts()[0]
	if h.GetHostId() != k.host.ID || h.GetHostname() != "node-1" || h.GetOsName() != "Debian" || h.GetOsVersion() != "12" ||
		h.GetOsArch() != "amd64" || h.GetManufacturer() != "Dell Inc." || h.GetModel() != "R650" || h.GetTags()["env"] != "prod" ||
		h.GetCapability() != "enabled" || h.GetAgentOnline() {
		t.Fatalf("target = %v", h)
	}
	ts, err = k.srv.ListTeleportJoinTargets(ctx, &invv1.ListTeleportJoinTargetsRequest{TenantId: tenantA, HostIds: []string{k.host.ID}})
	if err != nil || len(ts.GetHosts()) != 1 {
		t.Fatalf("targets by id = %v %v", ts, err)
	}
	if _, err := k.srv.ListTeleportJoinTargets(ctx, &invv1.ListTeleportJoinTargetsRequest{TenantId: tenantA, Limit: 500}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad limit: %v", err)
	}
	c, err := k.srv.CancelTeleportJoin(ctx, &invv1.CancelTeleportJoinRequest{TenantId: tenantA, EnrollmentId: tpEnrollment})
	if err != nil || c.GetState() != invv1.TeleportJoinState_TELEPORT_JOIN_STATE_CANCELLED {
		t.Fatalf("cancel = %v %v", c, err)
	}
	if _, err := k.srv.CancelTeleportJoin(ctx, &invv1.CancelTeleportJoinRequest{TenantId: tenantA, EnrollmentId: store.NewID()}); code(err) != codes.NotFound {
		t.Fatalf("cancel unknown: %v", err)
	}
	for state := range teleportStates {
		if teleportStates[state] == invv1.TeleportJoinState_TELEPORT_JOIN_STATE_UNSPECIFIED {
			t.Errorf("state %s unmapped", state)
		}
	}
	if len(teleportStates) != len(invv1.TeleportJoinState_name)-1 {
		t.Fatalf("states mapped = %d", len(teleportStates))
	}
}

// TestTeleportJoinErrors: disabled and ineligible hosts are
// FailedPrecondition with exactly the reason token; validation is
// InvalidArgument with the field; unknown hosts NotFound.
func TestTeleportJoinErrors(t *testing.T) {
	ctx := context.Background()
	withFakeCaller(t, teleportID, true)
	k := newTeleportKit(t, false, "linux", store.CapTeleportV1)
	_, err := k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID))
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "disabled_on_server" {
		t.Fatalf("disabled: %v", err)
	}
	for _, m := range []func() error{
		func() error {
			_, e := k.srv.GetTeleportJoins(ctx, &invv1.GetTeleportJoinsRequest{TenantId: tenantA, EnrollmentIds: []string{tpEnrollment}})
			return e
		},
		func() error {
			_, e := k.srv.CancelTeleportJoin(ctx, &invv1.CancelTeleportJoinRequest{TenantId: tenantA, EnrollmentId: tpEnrollment})
			return e
		},
		func() error {
			_, e := k.srv.ListTeleportJoinTargets(ctx, &invv1.ListTeleportJoinTargetsRequest{TenantId: tenantA})
			return e
		},
	} {
		if st, _ := status.FromError(m()); st.Code() != codes.FailedPrecondition || st.Message() != "disabled_on_server" {
			t.Fatalf("disabled: %v", st)
		}
	}
	k = newTeleportKit(t, true, "windows", store.CapTeleportV1)
	_, err = k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID))
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "not_supported_platform" {
		t.Fatalf("windows: %v", err)
	}
	k = newTeleportKit(t, true, "linux", store.CapModuleV1) // agent older than 038
	_, err = k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID))
	if st, _ := status.FromError(err); st.Code() != codes.FailedPrecondition || st.Message() != "upgrade_required" {
		t.Fatalf("ineligible: %v", err)
	}
	req := teleportReq(k.host.ID)
	req.ClusterId = "prod"
	_, err = k.srv.CreateTeleportJoin(ctx, req)
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument || st.Message() != "invalid cluster_id" {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := k.srv.CreateTeleportJoin(ctx, teleportReq(store.NewID())); code(err) != codes.NotFound {
		t.Fatalf("unknown host: %v", err)
	}
	k.mem.FailNext("GetTeleportItemByEnrollment")
	if _, err := k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID)); code(err) != codes.Unavailable {
		t.Fatalf("store failure: %v", err)
	}
}

// TestTeleportJoinSourceCheck: only configured SPIFFE services; a refused
// peer is audited; the tenant must be a uuid.
func TestTeleportJoinSourceCheck(t *testing.T) {
	k := newTeleportKit(t, true, "linux", store.CapTeleportV1)
	ctx := context.Background()
	withFakeCaller(t, gatewayID, true)
	if _, err := k.srv.CreateTeleportJoin(ctx, teleportReq(k.host.ID)); code(err) != codes.PermissionDenied {
		t.Fatalf("gateway: %v", err)
	}
	rows := k.mem.AuditRows()
	if len(rows) != 1 || rows[0].Action != "teleport_join_refused" || rows[0].ActorID != "gateway" || rows[0].Reason != "source_not_allowed" {
		t.Fatalf("refusal audit = %+v", rows)
	}
	withFakeCaller(t, "spiffe://example.org/node/x", true)
	if _, err := k.srv.GetTeleportJoins(ctx, &invv1.GetTeleportJoinsRequest{TenantId: tenantB}); code(err) != codes.PermissionDenied {
		t.Fatalf("non-service: %v", err)
	}
	if rows := k.mem.AuditRows(); rows[len(rows)-1].ActorID != "unknown" || rows[len(rows)-1].TenantID != tenantB {
		t.Fatalf("unknown actor row = %+v", rows[len(rows)-1])
	}
	n := len(k.mem.AuditRows())
	if _, err := k.srv.ListTeleportJoinTargets(ctx, &invv1.ListTeleportJoinTargetsRequest{TenantId: "t"}); code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := k.srv.CancelTeleportJoin(ctx, &invv1.CancelTeleportJoinRequest{TenantId: "t"}); code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if len(k.mem.AuditRows()) != n {
		t.Fatal("audited without a tenant")
	}
	withFakeCaller(t, "", false)
	if _, err := k.srv.ListTeleportJoinTargets(ctx, &invv1.ListTeleportJoinTargetsRequest{TenantId: tenantA}); code(err) != codes.Unauthenticated {
		t.Fatalf("no identity: %v", err)
	}
	withFakeCaller(t, teleportID, true)
	if _, err := k.srv.GetTeleportJoins(ctx, &invv1.GetTeleportJoinsRequest{TenantId: "nope"}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad tenant: %v", err)
	}
	if _, err := k.srv.CreateTeleportJoin(ctx, &invv1.CreateTeleportJoinRequest{TenantId: "nope"}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad tenant: %v", err)
	}
}

func TestRegisterTeleportJoin(t *testing.T) {
	k := newTeleportKit(t, false, "linux")
	gs := grpc.NewServer()
	Register(gs, Deps{TeleportJoin: k.srv.Svc, TeleportSources: []string{"teleport"}})
	if _, ok := gs.GetServiceInfo()["inventory.v1.TeleportJoinDeliveryService"]; !ok {
		t.Fatal("service not registered")
	}
	gs = grpc.NewServer()
	Register(gs, Deps{})
	if _, ok := gs.GetServiceInfo()["inventory.v1.TeleportJoinDeliveryService"]; ok {
		t.Fatal("registered without a service")
	}
}
