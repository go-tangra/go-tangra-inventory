package app

import (
	"context"
	"os"
	"testing"

	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1" // registers inventory.v1
	"github.com/go-tangra/go-tangra/v4/authz"
	"github.com/go-tangra/go-tangra/v4/identity"
)

// inventoryOps lists every inventory.v1 RPC as a policy operation.
func inventoryOps(t *testing.T) []string {
	t.Helper()
	fd, err := protoregistry.GlobalFiles.FindFileByPath("inventory/v1/inventory.proto")
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	svcs := fd.Services()
	for i := 0; i < svcs.Len(); i++ {
		s := svcs.Get(i)
		ms := s.Methods()
		for j := 0; j < ms.Len(); j++ {
			ops = append(ops, "/"+string(s.FullName())+"/"+string(ms.Get(j).Name()))
		}
	}
	return ops
}

// The ipam-hostsync rule admits svc/ipam to the three HostReportService RPCs
// and health, and to nothing else in inventory.
func TestPolicyIPAMHostSync(t *testing.T) {
	f, err := os.Open("../../deploy/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	pol, err := authz.Load(f)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	ipam := identity.ForService("example.org", "ipam")
	allowed := map[string]bool{
		"/inventory.v1.HostReportService/ListReportTenants": true,
		"/inventory.v1.HostReportService/ListHostReports":   true,
		"/inventory.v1.HostReportService/GetHostReport":     true,
		"/grpc.health.v1.Health/Check":                      true,
	}
	ops := append(inventoryOps(t), "/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch")
	seen := 0
	for _, op := range ops {
		d := pol.Authorize(context.Background(), ipam, "inventory", op)
		if d.Allowed != allowed[op] {
			t.Errorf("ipam %s: allowed=%v (rule %q)", op, d.Allowed, d.RuleID)
		}
		if d.Allowed {
			seen++
			if d.RuleID != "ipam-hostsync" {
				t.Errorf("ipam %s allowed by %q", op, d.RuleID)
			}
		}
	}
	if seen != len(allowed) {
		t.Fatalf("ipam allowed %d operations, want %d", seen, len(allowed))
	}
	// The report RPCs are not granted to asset by any rule.
	asset := identity.ForService("example.org", "asset")
	if pol.Authorize(context.Background(), asset, "inventory", "/inventory.v1.HostReportService/ListReportTenants").Allowed {
		t.Error("asset may list report tenants")
	}
}
