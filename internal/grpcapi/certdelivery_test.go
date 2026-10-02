package grpcapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/pkg/inventorymanifest"
)

const deployerID = "spiffe://example.org/svc/deployer"

// certLCM serves the certmaterial RSA fixture as cert-1.
type certLCM struct{ err error }

func (l certLCM) Download(_ context.Context, _, certID string, includeKey bool) (lcmclient.Bundle, error) {
	if l.err != nil {
		return lcmclient.Bundle{}, l.err
	}
	if certID != "cert-1" {
		return lcmclient.Bundle{}, lcmclient.ErrNotFound
	}
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join("..", "certmaterial", "testdata", n)) // #nosec G304 -- test fixture
		return string(b)
	}
	b := lcmclient.Bundle{CertPEM: read("rsa.crt"), ChainPEM: read("chain.pem"), NotAfter: time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)}
	if includeKey {
		b.KeyPEM = []byte(read("rsa.pkcs8.key"))
	}
	return b, nil
}

type certKit struct {
	srv  *CertDeliveryServer
	mem  *memstore.Mem
	reg  *registry.Memory
	host store.Host
	agnt store.Agent
}

func newCertKit(t *testing.T, enabled bool, lcmErr error) certKit {
	t.Helper()
	mem := memstore.New()
	reg := registry.NewMemory()
	svc := certdelivery.New(mem, certLCM{err: lcmErr}, reg, events.HubPublisher{},
		certdelivery.Config{Enabled: enabled, PendingTTL: 168 * time.Hour, ReportTimeout: 15 * time.Minute})
	ctx := context.Background()
	h, _ := mem.ResolveHost(ctx, tenantA, store.Host{Hostname: "web-1", MachineID: "m1"})
	_ = mem.SetHostTags(ctx, tenantA, h.ID, map[string]string{"role": "web"})
	a := store.Agent{ID: store.NewID(), TenantID: tenantA, HostID: h.ID, AgentVersion: "4.7.0"}
	_ = mem.CreateAgent(ctx, a)
	_ = mem.SetAgentPlatform(ctx, a.ID, "linux", "amd64", "deb", []string{store.CapCertV1}, time.Now())
	return certKit{srv: &CertDeliveryServer{Svc: svc, Sources: []string{"deployer"}}, mem: mem, reg: reg, host: h, agnt: a}
}

func createReq(hostID string) *invv1.CreateCertificateDeliveryRequest {
	return &invv1.CreateCertificateDeliveryRequest{TenantId: tenantA, IdempotencyKey: "job-1", ConfigurationId: "cfg-1", TargetId: "tgt-1",
		Trigger: "manual", CertificateId: "cert-1", Name: "www", KeyPolicy: "require", Selector: &invv1.HostSelector{HostIds: []string{hostID}}}
}

func code(err error) codes.Code { return status.Code(err) }

// TestCertDeliverySourceCheck (T034): only configured SPIFFE services; a
// refused peer is audited; tenant must be a uuid.
func TestCertDeliverySourceCheck(t *testing.T) {
	k := newCertKit(t, true, nil)
	ctx := context.Background()
	withFakeCaller(t, ipamID, true)
	if _, err := k.srv.CreateCertificateDelivery(ctx, createReq(k.host.ID)); code(err) != codes.PermissionDenied {
		t.Fatalf("ipam: %v", err)
	}
	rows := k.mem.AuditRows()
	if len(rows) != 1 || rows[0].Action != "cert_delivery_refused" || rows[0].ActorID != "ipam" || rows[0].Reason != "source_not_allowed" {
		t.Fatalf("refusal audit = %+v", rows)
	}
	// Not a SPIFFE service id: refused, audited as unknown.
	withFakeCaller(t, "spiffe://example.org/node/x", true)
	if _, err := k.srv.GetCertificateDelivery(ctx, &invv1.GetCertificateDeliveryRequest{TenantId: tenantB, Id: "x"}); code(err) != codes.PermissionDenied {
		t.Fatalf("non-service: %v", err)
	}
	if rows := k.mem.AuditRows(); rows[len(rows)-1].ActorID != "unknown" || rows[len(rows)-1].TenantID != tenantB {
		t.Fatalf("unknown actor row = %+v", rows[len(rows)-1])
	}
	// Bad tenant with a refused peer: refused, nothing audited.
	n := len(k.mem.AuditRows())
	if _, err := k.srv.PreviewCertificateTargets(ctx, &invv1.PreviewCertificateTargetsRequest{TenantId: "t"}); code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if len(k.mem.AuditRows()) != n {
		t.Fatal("audited without a tenant")
	}
	withFakeCaller(t, "", false)
	if _, err := k.srv.VerifyHostCertificates(ctx, &invv1.VerifyHostCertificatesRequest{TenantId: tenantA}); code(err) != codes.Unauthenticated {
		t.Fatalf("no identity: %v", err)
	}
	withFakeCaller(t, deployerID, true)
	if _, err := k.srv.MarkCertificateRevoked(ctx, &invv1.MarkCertificateRevokedRequest{TenantId: "nope"}); code(err) != codes.InvalidArgument {
		t.Fatalf("bad tenant: %v", err)
	}
}

// TestCertDeliveryMapping (T034): every RPC maps its messages; no response
// carries PEM.
func TestCertDeliveryMapping(t *testing.T) {
	k := newCertKit(t, true, nil)
	ctx := context.Background()
	withFakeCaller(t, deployerID, true)
	unknown := "0190f7c2-6a3e-7c1a-9b2e-000000000001"
	req := createReq(k.host.ID)
	req.Selector.HostIds = append(req.Selector.HostIds, unknown)
	d, err := k.srv.CreateCertificateDelivery(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !d.GetCreated() || d.GetTenantId() != tenantA || d.GetCertificateId() != "cert-1" || d.GetName() != "www" || d.GetKeyPolicy() != "require" ||
		d.GetCreatedAt() == 0 || d.GetExpiresAt() <= d.GetCreatedAt() || len(d.GetItems()) != 1 || len(d.GetUnknownHostIds()) != 1 {
		t.Fatalf("delivery = %v", d)
	}
	it := d.GetItems()[0]
	if it.GetHostId() != k.host.ID || it.GetHostname() != "web-1" || it.GetAgentOnline() || it.GetState() != invv1.DeliveryState_DELIVERY_STATE_PENDING ||
		it.GetHookExitCode() != -1 || it.GetAttempts() != 1 || it.GetUpdatedAt() == 0 {
		t.Fatalf("item = %v", it)
	}
	g, err := k.srv.GetCertificateDelivery(ctx, &invv1.GetCertificateDeliveryRequest{TenantId: tenantA, Id: d.GetId()})
	if err != nil || g.GetId() != d.GetId() || g.GetCreated() {
		t.Fatalf("get = %v %v", g, err)
	}
	// Report an install so the item carries serial, fingerprint and exit code.
	m, err := k.srv.Svc.Fetch(ctx, k.agnt, it.GetId())
	if err != nil {
		t.Fatal(err)
	}
	fp := m.Bundle.Fingerprint
	m.Wipe()
	if ok, err := k.srv.Svc.Report(ctx, k.agnt, certdelivery.Report{ItemID: it.GetId(), State: "installed", Fingerprint: fp, HookExitCode: 0}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	g, _ = k.srv.GetCertificateDelivery(ctx, &invv1.GetCertificateDeliveryRequest{TenantId: tenantA, Id: d.GetId()})
	gi := g.GetItems()[0]
	if gi.GetState() != invv1.DeliveryState_DELIVERY_STATE_INSTALLED || gi.GetFingerprintSha256() != fp || gi.GetSerial() == "" || gi.GetHookExitCode() != 0 {
		t.Fatalf("installed item = %v", gi)
	}
	p, err := k.srv.PreviewCertificateTargets(ctx, &invv1.PreviewCertificateTargetsRequest{TenantId: tenantA, Selector: &invv1.HostSelector{HostTags: []string{"role=web"}}})
	if err != nil || len(p.GetHosts()) != 1 || p.GetHosts()[0].GetCapability() != "enabled" || p.GetHosts()[0].GetTags()["role"] != "web" ||
		p.GetHosts()[0].GetHostname() != "web-1" || p.GetTruncated() {
		t.Fatalf("preview = %v %v", p, err)
	}
	v, err := k.srv.VerifyHostCertificates(ctx, &invv1.VerifyHostCertificatesRequest{TenantId: tenantA,
		Selector: &invv1.HostSelector{HostIds: []string{k.host.ID}}, Name: "www", ExpectedFingerprintSha256: fp})
	if err != nil || v.GetMatched() != 1 || v.GetTotal() != 1 || v.GetHosts()[0].GetStatus() != "match" || v.GetHosts()[0].GetLastDeliveredAt() == 0 {
		t.Fatalf("verify = %v %v", v, err)
	}
	// A never-delivered host verifies as missing with last_delivered_at 0.
	h2, _ := k.mem.ResolveHost(ctx, tenantA, store.Host{Hostname: "web-2", MachineID: "m2"})
	v, _ = k.srv.VerifyHostCertificates(ctx, &invv1.VerifyHostCertificatesRequest{TenantId: tenantA,
		Selector: &invv1.HostSelector{HostIds: []string{h2.ID}}, Name: "www", ExpectedFingerprintSha256: fp})
	if v.GetHosts()[0].GetStatus() != "missing" || v.GetHosts()[0].GetLastDeliveredAt() != 0 {
		t.Fatalf("missing = %v", v)
	}
	r, err := k.srv.MarkCertificateRevoked(ctx, &invv1.MarkCertificateRevokedRequest{TenantId: tenantA, CertificateId: "cert-1"})
	if err != nil || r.GetFlaggedHosts() != 1 || r.GetCancelledItems() != 0 {
		t.Fatalf("revoke = %v %v", r, err)
	}
	for _, out := range []string{protojson.Format(d), protojson.Format(g), protojson.Format(p), protojson.Format(v), protojson.Format(r)} {
		if strings.Contains(out, "BEGIN") || strings.Contains(out, "PRIVATE") {
			t.Fatalf("material in a response: %s", out)
		}
	}
	// Every state maps to its enum.
	for _, st := range store.DeliveryStates {
		if deliveryStates[st] == invv1.DeliveryState_DELIVERY_STATE_UNSPECIFIED {
			t.Errorf("state %s unmapped", st)
		}
	}
}

// TestCertDeliveryErrors maps the relay errors to gRPC codes.
func TestCertDeliveryErrors(t *testing.T) {
	ctx := context.Background()
	withFakeCaller(t, deployerID, true)
	k := newCertKit(t, false, nil)
	if _, err := k.srv.CreateCertificateDelivery(ctx, createReq(k.host.ID)); code(err) != codes.FailedPrecondition ||
		status.Convert(err).Message() != "certificate delivery is disabled" {
		t.Fatalf("disabled: %v", err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := k.srv.GetCertificateDelivery(ctx, &invv1.GetCertificateDeliveryRequest{TenantId: tenantA, Id: "x"})
			return err
		},
		func() error {
			_, err := k.srv.PreviewCertificateTargets(ctx, &invv1.PreviewCertificateTargetsRequest{TenantId: tenantA})
			return err
		},
		func() error {
			_, err := k.srv.VerifyHostCertificates(ctx, &invv1.VerifyHostCertificatesRequest{TenantId: tenantA})
			return err
		},
		func() error {
			_, err := k.srv.MarkCertificateRevoked(ctx, &invv1.MarkCertificateRevokedRequest{TenantId: tenantA})
			return err
		},
	} {
		if code(call()) != codes.FailedPrecondition {
			t.Fatal("disabled not FailedPrecondition")
		}
	}
	k = newCertKit(t, true, nil)
	bad := createReq(k.host.ID)
	bad.Name = "../x"
	if _, err := k.srv.CreateCertificateDelivery(ctx, bad); code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "name") {
		t.Fatalf("invalid: %v", err)
	}
	nf := createReq(k.host.ID)
	nf.CertificateId = "cert-x"
	if _, err := k.srv.CreateCertificateDelivery(ctx, nf); code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "certificate_not_found") {
		t.Fatalf("not found: %v", err)
	}
	if _, err := k.srv.GetCertificateDelivery(ctx, &invv1.GetCertificateDeliveryRequest{TenantId: tenantA, Id: "0190f7c2-6a3e-7c1a-9b2e-000000000009"}); code(err) != codes.NotFound {
		t.Fatalf("get missing: %v", err)
	}
	if _, err := k.srv.PreviewCertificateTargets(ctx, &invv1.PreviewCertificateTargetsRequest{TenantId: tenantA}); code(err) != codes.InvalidArgument {
		t.Fatalf("preview without selector: %v", err)
	}
	if _, err := k.srv.VerifyHostCertificates(ctx, &invv1.VerifyHostCertificatesRequest{TenantId: tenantA}); code(err) != codes.InvalidArgument {
		t.Fatalf("verify without selector: %v", err)
	}
	if _, err := k.srv.MarkCertificateRevoked(ctx, &invv1.MarkCertificateRevokedRequest{TenantId: tenantA}); code(err) != codes.InvalidArgument {
		t.Fatalf("revoke without id: %v", err)
	}
	k = newCertKit(t, true, lcmclient.ErrUnavailable)
	if _, err := k.srv.CreateCertificateDelivery(ctx, createReq(k.host.ID)); code(err) != codes.Unavailable {
		t.Fatalf("lcm down: %v", err)
	}
	for _, e := range []error{certdelivery.ErrCertificateRevoked, certdelivery.ErrCertificateExpired} {
		if code(certError(e)) != codes.FailedPrecondition {
			t.Errorf("%v", e)
		}
	}
	if code(certError(repo.ErrNotFound)) != codes.NotFound || code(certError(errors.New("x"))) != codes.Unavailable {
		t.Fatal("mapping")
	}
}

// TestCertDeliveryNotGatewayExposed (T034, mesh-policy.md): the module
// manifest declares no gRPC method for the gateway, so browser traffic
// cannot reach CertificateDeliveryService.
func TestCertDeliveryNotGatewayExposed(t *testing.T) {
	for _, m := range inventorymanifest.Methods {
		if strings.Contains(m.FullMethod, "CertificateDeliveryService") {
			t.Fatalf("gateway method %v", m)
		}
	}
	man, err := inventorymanifest.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range man.Routes {
		if strings.Contains(r.Path, "CertificateDelivery") || strings.Contains(r.Path, "inventory.v1.") {
			t.Fatalf("route %v", r)
		}
	}
}

func TestRegisterCertDelivery(t *testing.T) {
	k := newCertKit(t, false, nil)
	gs := grpc.NewServer()
	Register(gs, Deps{CertDelivery: k.srv.Svc, CertSources: []string{"deployer"}})
	if _, ok := gs.GetServiceInfo()["inventory.v1.CertificateDeliveryService"]; !ok {
		t.Fatal("service not registered")
	}
	gs = grpc.NewServer()
	Register(gs, Deps{})
	if _, ok := gs.GetServiceInfo()["inventory.v1.CertificateDeliveryService"]; ok {
		t.Fatal("registered without a service")
	}
}
