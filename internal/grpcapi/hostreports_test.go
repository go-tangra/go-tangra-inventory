package grpcapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	tenantA  = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	tenantB  = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	ipamID   = "spiffe://example.org/svc/ipam"
	gwID     = "spiffe://example.org/svc/gateway"
	assetID  = "spiffe://example.org/svc/asset"
	noSnapID = "0190f7c2-6a3e-7c1a-9b2e-000000000000"
)

type reportKit struct {
	srv   *HostReportServer
	mem   *memstore.Mem
	snaps *snapshots.Service
	clock time.Time
}

func newReportKit(t *testing.T) *reportKit {
	t.Helper()
	mem := memstore.New()
	k := &reportKit{mem: mem, clock: time.UnixMilli(1_700_000_000_000).UTC()}
	k.snaps = snapshots.New(mem, hosts.New(mem), events.HubPublisher{})
	k.snaps.SetClock(func() time.Time { return k.clock })
	k.srv = &HostReportServer{Store: mem, Consumers: []string{"ipam"}, MaxPageBytes: 3 << 20}
	return k
}

// ingest submits a snapshot for hostname in tenant with n interfaces and
// returns the host id; the clock advances one second per call.
func (k *reportKit) ingest(t *testing.T, tenant, hostname string, inv store.Inventory) string {
	t.Helper()
	k.clock = k.clock.Add(time.Second)
	inv.Identity.Hostname = hostname
	inv.CollectedAt = k.clock
	s, err := k.snaps.Ingest(context.Background(), tenant, inv, store.SourceAgent)
	if err != nil {
		t.Fatal(err)
	}
	return s.HostID
}

func reportInv(addr string) store.Inventory {
	return store.Inventory{
		OS: store.OSInfo{Name: "Ubuntu", Family: "linux"},
		Networks: []store.NetIface{{Name: "eth0", MAC: "00:11:22:33:44:55", Type: store.IfaceEthernet,
			Addresses: []store.IfAddress{{Address: addr, PrefixLength: 24, Family: "ipv4"}}}},
		Programs:         []store.Program{{Name: "openssl", Version: "1", AvailableVersion: "2"}, {Name: "curl", Version: "8"}},
		HypervisorGuests: []store.HypervisorGuest{{ID: "100", Name: "g"}},
		Bmc:              &store.Bmc{Address: "10.0.0.9"},
	}
}

func TestListReportTenants(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	ctx := context.Background()
	k.ingest(t, tenantA, "a1", reportInv("10.0.0.1"))
	firstAt := k.clock
	k.ingest(t, tenantB, "b1", reportInv("10.0.1.1"))

	resp, err := k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{})
	if err != nil || len(resp.GetTenantIds()) != 2 || resp.GetMaxChangedAt() != k.clock.UnixMilli() {
		t.Fatalf("all = %v %v", resp, err)
	}
	resp, _ = k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{ChangedSince: firstAt.UnixMilli()})
	if len(resp.GetTenantIds()) != 1 || resp.GetTenantIds()[0] != tenantB {
		t.Fatalf("since = %v", resp)
	}
	resp, _ = k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{ChangedSince: k.clock.UnixMilli()})
	if len(resp.GetTenantIds()) != 0 || resp.GetMaxChangedAt() != k.clock.UnixMilli() {
		t.Fatalf("watermark must not regress: %v", resp)
	}
	if _, err := k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{ChangedSince: -1}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("negative since: %v", err)
	}
	k.mem.FailNext("ListReportTenants")
	if _, err := k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("store failure: %v", err)
	}
}

// Negative: the cross-tenant RPC is refused to every non-consumer service and
// to callers without a SPIFFE identity; the same holds for the tenant RPCs.
func TestHostReportCallerChecks(t *testing.T) {
	k := newReportKit(t)
	ctx := context.Background()
	for _, id := range []string{gwID, assetID, "spiffe://example.org/svc/ipam/extra", "spiffe://example.org/ipam",
		"spiffe://example.org/svc/", "not-a-spiffe-id"} {
		withFakeCaller(t, id, true)
		if _, err := k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Errorf("ListReportTenants from %q: %v", id, err)
		}
		if _, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA}); status.Code(err) != codes.PermissionDenied {
			t.Errorf("ListHostReports from %q: %v", id, err)
		}
		if _, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantA, HostId: noSnapID}); status.Code(err) != codes.PermissionDenied {
			t.Errorf("GetHostReport from %q: %v", id, err)
		}
	}
	withFakeCaller(t, "", false)
	if _, err := k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("no peer: %v", err)
	}
	if _, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("no peer: %v", err)
	}
	if _, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantA, HostId: noSnapID}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("no peer: %v", err)
	}
	// An empty consumer list closes the service.
	withFakeCaller(t, ipamID, true)
	k.srv.Consumers = nil
	if _, err := k.srv.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("no consumers configured: %v", err)
	}
}

func TestListHostReportsViewsAndPaging(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	ctx := context.Background()
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, k.ingest(t, tenantA, fmt.Sprint("a", i), reportInv(fmt.Sprint("10.0.0.", i+1))))
	}
	k.ingest(t, tenantB, "b-secret", reportInv("10.9.9.9"))

	// Full view, 2 per page, ordered by change time: exactly the 5 tenant A hosts.
	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		resp, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range resp.GetReports() {
			if r.GetTenantId() != tenantA || r.GetHost().GetHostname() == "b-secret" {
				t.Fatalf("cross-tenant report: %v", r)
			}
			if len(r.GetNetworkInterfaces()) != 1 || len(r.GetPendingUpdates()) != 1 || len(r.GetReportDigest()) != 64 {
				t.Fatalf("full report incomplete: %v", r)
			}
			got = append(got, r.GetHost().GetId())
		}
		cursor = resp.GetNextCursor()
		if cursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("paging does not terminate")
		}
	}
	if strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Fatalf("order/paging:\n got %v\nwant %v", got, ids)
	}

	// Digest view never carries interface, package or guest data.
	resp, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, View: invv1.HostReportView_HOST_REPORT_VIEW_DIGEST})
	if err != nil || len(resp.GetReports()) != 5 || resp.GetNextCursor() != "" {
		t.Fatalf("digest view: %v %v", resp, err)
	}
	for _, r := range resp.GetReports() {
		if len(r.GetNetworkInterfaces()) != 0 || len(r.GetPendingUpdates()) != 0 || len(r.GetHypervisorGuests()) != 0 ||
			r.GetBmc() != nil || r.GetSnapshotId() != "" || len(r.GetReportDigest()) != 64 || r.GetReportChangedAt() == 0 {
			t.Fatalf("digest row leaks data: %v", r)
		}
	}

	// changed_since: only hosts changed after the third one.
	h2, _ := k.mem.GetHost(ctx, tenantA, ids[2])
	resp, _ = k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, ChangedSince: h2.ReportChangedAt.UnixMilli()})
	if len(resp.GetReports()) != 2 {
		t.Fatalf("since = %d reports", len(resp.GetReports()))
	}
}

func TestListHostReportsRetiredAndLegacy(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	ctx := context.Background()
	id := k.ingest(t, tenantA, "r", reportInv("10.0.0.1"))
	before, _ := k.mem.GetHost(ctx, tenantA, id)
	k.mem.Now = func() time.Time { return k.clock.Add(time.Minute) }
	if err := k.mem.RetireHost(ctx, tenantA, id); err != nil {
		t.Fatal(err)
	}
	// A host without any snapshot (e.g. purged) is skipped, not an error.
	if _, err := k.mem.ResolveHost(ctx, tenantA, store.Host{Hostname: "empty"}); err != nil {
		t.Fatal(err)
	}

	for _, view := range []invv1.HostReportView{invv1.HostReportView_HOST_REPORT_VIEW_FULL, invv1.HostReportView_HOST_REPORT_VIEW_DIGEST} {
		resp, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, View: view})
		if err != nil || len(resp.GetReports()) != 1 {
			t.Fatalf("%v: %v %v", view, resp, err)
		}
		r := resp.GetReports()[0]
		if r.GetHost().GetStatus() != invv1.HostStatus_HOST_STATUS_RETIRED || r.GetReportDigest() == before.ReportDigest ||
			len(r.GetReportDigest()) != 64 {
			t.Fatalf("%v retired report = %v", view, r)
		}
	}
	// Retirement is picked up by a changed-since poll.
	resp, _ := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, ChangedSince: before.ReportChangedAt.UnixMilli()})
	if len(resp.GetReports()) != 1 {
		t.Fatalf("retire not visible to a poll: %v", resp)
	}
}

func TestListHostReportsPageBytes(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	ctx := context.Background()
	big := reportInv("10.0.0.1")
	for i := 0; i < 2000; i++ {
		big.Programs = append(big.Programs, store.Program{Name: fmt.Sprint("package-with-a-long-name-", i), Version: "1.0.0", AvailableVersion: "1.0.1"})
	}
	for i := 0; i < 4; i++ {
		k.ingest(t, tenantA, fmt.Sprint("big", i), big)
	}
	one, _ := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, Limit: 1})
	size := proto.Size(one.GetReports()[0])
	k.srv.MaxPageBytes = size*2 + size/2 // room for two reports
	resp, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA})
	if err != nil || len(resp.GetReports()) != 2 || resp.GetNextCursor() == "" {
		t.Fatalf("byte-bounded page: %d reports, cursor %q, %v", len(resp.GetReports()), resp.GetNextCursor(), err)
	}
	total := 0
	for _, r := range resp.GetReports() {
		total += proto.Size(r)
	}
	if total > k.srv.MaxPageBytes {
		t.Fatalf("page %d bytes > %d", total, k.srv.MaxPageBytes)
	}
	rest, _ := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA, Cursor: resp.GetNextCursor()})
	if len(rest.GetReports()) != 2 || rest.GetNextCursor() != "" {
		t.Fatalf("rest = %d %q", len(rest.GetReports()), rest.GetNextCursor())
	}
	// A single report larger than the budget is still returned alone (progress).
	k.srv.MaxPageBytes = size / 2
	resp, _ = k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA})
	if len(resp.GetReports()) != 1 || resp.GetNextCursor() == "" {
		t.Fatalf("oversized single report: %d %q", len(resp.GetReports()), resp.GetNextCursor())
	}
}

func TestListHostReportsInvalidArguments(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	ctx := context.Background()
	b64 := base64.RawURLEncoding.EncodeToString
	for name, req := range map[string]*invv1.ListHostReportsRequest{
		"non-uuid tenant": {TenantId: "tenant-a"},
		"empty tenant":    {},
		"limit negative":  {TenantId: tenantA, Limit: -1},
		"limit too large": {TenantId: tenantA, Limit: 201},
		"since negative":  {TenantId: tenantA, ChangedSince: -5},
		"unknown view":    {TenantId: tenantA, View: invv1.HostReportView(9)},
		"garbage cursor":  {TenantId: tenantA, Cursor: "%%%"},
		"cursor no sep":   {TenantId: tenantA, Cursor: b64([]byte("v1"))},
		"cursor bad ver":  {TenantId: tenantA, Cursor: b64([]byte("v2|0|" + tenantA))},
		"cursor bad time": {TenantId: tenantA, Cursor: b64([]byte("v1|x|" + tenantA))},
		"cursor neg time": {TenantId: tenantA, Cursor: b64([]byte("v1|-1|" + tenantA))},
		"cursor bad id":   {TenantId: tenantA, Cursor: b64([]byte("v1|0|x' OR 1=1"))},
		"cursor too long": {TenantId: tenantA, Cursor: strings.Repeat("A", 300)},
	} {
		if _, err := k.srv.ListHostReports(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	k.mem.FailNext("ListHostReportRows")
	if _, err := k.srv.ListHostReports(ctx, &invv1.ListHostReportsRequest{TenantId: tenantA}); status.Code(err) != codes.Unavailable {
		t.Fatalf("store failure: %v", err)
	}
}

func TestGetHostReport(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	ctx := context.Background()
	id := k.ingest(t, tenantA, "g", reportInv("10.0.0.1"))
	r, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantA, HostId: id})
	if err != nil || r.GetHost().GetId() != id || len(r.GetNetworkInterfaces()) != 1 {
		t.Fatalf("get = %v %v", r, err)
	}
	stored, _ := k.mem.GetHost(ctx, tenantA, id)
	if r.GetReportDigest() != stored.ReportDigest {
		t.Fatalf("digest %q != stored %q", r.GetReportDigest(), stored.ReportDigest)
	}
	// Negative: another tenant cannot read it.
	if _, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantB, HostId: id}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if _, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantA, HostId: noSnapID}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing host: %v", err)
	}
	h, _ := k.mem.ResolveHost(ctx, tenantA, store.Host{Hostname: "nosnap"})
	if _, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantA, HostId: h.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("host without snapshot: %v", err)
	}
	for _, req := range []*invv1.GetHostReportRequest{{TenantId: "x", HostId: id}, {TenantId: tenantA, HostId: "x"}} {
		if _, err := k.srv.GetHostReport(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid %v: %v", req, err)
		}
	}
	k.mem.FailNext("GetLatestForHost")
	if _, err := k.srv.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantA, HostId: id}); status.Code(err) != codes.Unavailable {
		t.Fatalf("store failure: %v", err)
	}
}

func TestListHostReportsSnapshotError(t *testing.T) {
	k := newReportKit(t)
	withFakeCaller(t, ipamID, true)
	k.ingest(t, tenantA, "s", reportInv("10.0.0.1"))
	k.mem.FailNext("GetLatestForHost")
	if _, err := k.srv.ListHostReports(context.Background(), &invv1.ListHostReportsRequest{TenantId: tenantA}); status.Code(err) != codes.Unavailable {
		t.Fatalf("snapshot read failure: %v", err)
	}
}

func TestRegisterHostReports(t *testing.T) {
	if s := serviceName("spiffe://example.org/svc/ipam"); s != "ipam" {
		t.Fatalf("serviceName = %q", s)
	}
	reg := &stubRegistrar{}
	Register(reg, Deps{Reports: memstore.New(), ReportConsumers: []string{"ipam"}, MaxReportPageBytes: 1 << 20})
	if reg.n != 1 {
		t.Fatalf("Register wired %d services, want HostReportService only", reg.n)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	for _, c := range []store.ReportCursor{
		{ID: tenantA},
		{ChangedAt: time.UnixMicro(1_700_000_000_123_456).UTC(), ID: tenantB},
	} {
		got, err := decodeCursor(encodeCursor(c))
		if err != nil || got != c {
			t.Fatalf("round trip %v -> %v %v", c, got, err)
		}
	}
}
