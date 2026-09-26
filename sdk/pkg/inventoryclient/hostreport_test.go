package inventoryclient_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/inventoryclient"
)

// fakeReports is an in-process HostReportService recording what it was asked.
type fakeReports struct {
	invv1.UnimplementedHostReportServiceServer
	lastTenants *invv1.ListReportTenantsRequest
	lastList    *invv1.ListHostReportsRequest
	lastGet     *invv1.GetHostReportRequest
	fail        error
}

func (f *fakeReports) ListReportTenants(_ context.Context, req *invv1.ListReportTenantsRequest) (*invv1.ListReportTenantsResponse, error) {
	f.lastTenants = req
	if f.fail != nil {
		return nil, f.fail
	}
	return &invv1.ListReportTenantsResponse{TenantIds: []string{"t-1", "t-2"}, MaxChangedAt: 1_700_000_000_123}, nil
}

func (f *fakeReports) ListHostReports(_ context.Context, req *invv1.ListHostReportsRequest) (*invv1.ListHostReportsResponse, error) {
	f.lastList = req
	if f.fail != nil {
		return nil, f.fail
	}
	if req.GetCursor() == "" {
		return &invv1.ListHostReportsResponse{Reports: []*invv1.HostReport{fullReport("h-1")}, NextCursor: "c-2"}, nil
	}
	return &invv1.ListHostReportsResponse{Reports: []*invv1.HostReport{{
		TenantId: req.GetTenantId(), Host: &invv1.Host{Id: "h-2", Status: invv1.HostStatus_HOST_STATUS_RETIRED},
		ReportDigest: "ab", ReportChangedAt: 5,
	}}}, nil
}

func (f *fakeReports) GetHostReport(_ context.Context, req *invv1.GetHostReportRequest) (*invv1.HostReport, error) {
	f.lastGet = req
	if f.fail != nil {
		return nil, f.fail
	}
	return fullReport(req.GetHostId()), nil
}

func fullReport(hostID string) *invv1.HostReport {
	return &invv1.HostReport{
		TenantId:        "t-1",
		Host:            &invv1.Host{Id: hostID, Hostname: "srv1", SystemSerial: "SN1", Status: invv1.HostStatus_HOST_STATUS_ACTIVE, LastSeen: 100},
		SnapshotId:      "s-1",
		CollectedAt:     1_700_000_000,
		ReportChangedAt: 1_700_000_000_500,
		ReportDigest:    "d1",
		AgentVersion:    "4.3.0",
		OsFamily:        "linux",
		NetworkInterfaces: []*invv1.NetworkInterface{{
			Name: "eth0", Mac: "aa:bb:cc:dd:ee:ff", Type: "ethernet", SpeedBps: 1_000_000_000, Up: true,
			Gateway: "192.0.2.1", Dhcp: true, DefaultRoute: true, Master: "br0", VlanId: 10,
			IpAddresses: []string{"192.0.2.10/24"},
			Addresses: []*invv1.InterfaceAddress{{
				Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", Dhcp: true, Scope: "global",
			}, {
				Address: "2001:db8::1", PrefixLength: 64, Family: "ipv6", Temporary: true, Deprecated: true, Scope: "global",
			}},
		}},
		PrimaryIpv4:    "192.0.2.10",
		PrimaryIpv6:    "2001:db8::10",
		Virtualization: &invv1.Virtualization{Role: "vm", Kind: "kvm", Source: "dmi"},
		Bmc: &invv1.Bmc{Address: "10.0.0.5", PrefixLength: 24, Gateway: "10.0.0.1", IpSource: "static", VlanId: 7,
			Ports: []*invv1.BmcPort{{Channel: 1, Mac: "00:11:22:33:44:55", Address: "10.0.0.5"}}},
		HypervisorGuests: []*invv1.HypervisorGuest{{Id: "100", Name: "vm1", Kind: "vm", Platform: "proxmox", Macs: []string{"bc:24:11:00:00:01"}}},
		UpdateState: &invv1.UpdateState{PackageManager: "apt", Status: "updates_available", RebootRequired: "true",
			AutomaticUpdates: "false", SecurityClassified: true, CheckedAt: 1_700_000_000, PendingCount: 2, SecurityCount: 1},
		PendingUpdates: []*invv1.PendingUpdate{{Name: "openssl", InstalledVersion: "3.0.1", AvailableVersion: "3.0.2", Security: true}},
		Truncated:      &invv1.CollectionLimits{Interfaces: 1, Addresses: 2, Guests: 3, Packages: 4, BmcPorts: 5},
	}
}

func dialReports(t *testing.T, rs invv1.HostReportServiceServer, ss invv1.InventorySnapshotServiceServer) *inventoryclient.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	invv1.RegisterHostReportServiceServer(gs, rs)
	if ss != nil {
		invv1.RegisterInventorySnapshotServiceServer(gs, ss)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return inventoryclient.New(conn)
}

func TestListReportTenants(t *testing.T) {
	f := &fakeReports{}
	c := dialReports(t, f, nil)
	since := time.UnixMilli(1_600_000_000_250)
	ids, maxChanged, err := c.ListReportTenants(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "t-1" {
		t.Fatalf("ids = %v", ids)
	}
	if got := f.lastTenants.GetChangedSince(); got != 1_600_000_000_250 {
		t.Fatalf("changed_since = %d, want ms", got)
	}
	if maxChanged.UnixMilli() != 1_700_000_000_123 || maxChanged.Location() != time.UTC {
		t.Fatalf("maxChanged = %v", maxChanged)
	}
	// zero time → 0 (every tenant with hosts)
	if _, _, err := c.ListReportTenants(context.Background(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if f.lastTenants.GetChangedSince() != 0 {
		t.Fatalf("zero time must map to 0, got %d", f.lastTenants.GetChangedSince())
	}
}

func TestListHostReportsFilterAndCursor(t *testing.T) {
	f := &fakeReports{}
	c := dialReports(t, f, nil)
	ctx := context.Background()

	reps, next, err := c.ListHostReports(ctx, "t-1", inventoryclient.ReportFilter{
		ChangedSince: time.UnixMilli(42), Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastList.GetTenantId() != "t-1" || f.lastList.GetChangedSince() != 42 || f.lastList.GetLimit() != 50 ||
		f.lastList.GetView() != invv1.HostReportView_HOST_REPORT_VIEW_FULL || f.lastList.GetCursor() != "" {
		t.Fatalf("request = %v", f.lastList)
	}
	if next != "c-2" || len(reps) != 1 {
		t.Fatalf("next=%q reps=%d", next, len(reps))
	}
	r := reps[0]
	if r.TenantID != "t-1" || r.Host.ID != "h-1" || r.Host.Hostname != "srv1" || r.Host.Status != "active" ||
		r.SnapshotID != "s-1" || r.Digest != "d1" || r.AgentVersion != "4.3.0" || r.OSFamily != "linux" ||
		r.PrimaryIPv4 != "192.0.2.10" || r.PrimaryIPv6 != "2001:db8::10" {
		t.Fatalf("report = %+v", r)
	}
	if r.CollectedAt.Unix() != 1_700_000_000 || r.ChangedAt.UnixMilli() != 1_700_000_000_500 {
		t.Fatalf("times = %v %v", r.CollectedAt, r.ChangedAt)
	}
	if len(r.Interfaces) != 1 {
		t.Fatalf("interfaces = %v", r.Interfaces)
	}
	ifc := r.Interfaces[0]
	if ifc.Type != "ethernet" || ifc.SpeedBps != 1_000_000_000 || !ifc.DHCP || !ifc.DefaultRoute || ifc.Master != "br0" ||
		ifc.VLANID != 10 || ifc.Gateway != "192.0.2.1" || len(ifc.Addresses) != 2 {
		t.Fatalf("iface = %+v", ifc)
	}
	a6 := ifc.Addresses[1]
	if a6.Address != "2001:db8::1" || a6.PrefixLength != 64 || a6.Family != "ipv6" || !a6.Temporary || !a6.Deprecated || a6.Scope != "global" {
		t.Fatalf("addr = %+v", a6)
	}
	if !ifc.Addresses[0].DHCP {
		t.Fatal("dhcp flag lost")
	}
	if r.Virtualization != (inventoryclient.Virtualization{Role: "vm", Kind: "kvm", Source: "dmi"}) {
		t.Fatalf("virt = %+v", r.Virtualization)
	}
	if r.BMC == nil || r.BMC.Address != "10.0.0.5" || r.BMC.PrefixLength != 24 || r.BMC.Gateway != "10.0.0.1" ||
		r.BMC.IPSource != "static" || r.BMC.VLANID != 7 || len(r.BMC.Ports) != 1 || r.BMC.Ports[0].MAC != "00:11:22:33:44:55" ||
		r.BMC.Ports[0].Channel != 1 || r.BMC.Ports[0].Address != "10.0.0.5" {
		t.Fatalf("bmc = %+v", r.BMC)
	}
	if len(r.Guests) != 1 || r.Guests[0].ID != "100" || r.Guests[0].Kind != "vm" || r.Guests[0].Platform != "proxmox" ||
		r.Guests[0].Name != "vm1" || len(r.Guests[0].MACs) != 1 {
		t.Fatalf("guests = %+v", r.Guests)
	}
	u := r.Updates
	if u.PackageManager != "apt" || u.Status != "updates_available" || u.RebootRequired != "true" || u.AutomaticUpdates != "false" ||
		!u.SecurityClassified || u.CheckedAt.Unix() != 1_700_000_000 || u.PendingCount != 2 || u.SecurityCount != 1 {
		t.Fatalf("updates = %+v", u)
	}
	if len(r.PendingUpdates) != 1 || r.PendingUpdates[0] != (inventoryclient.PendingUpdate{Name: "openssl", InstalledVersion: "3.0.1", AvailableVersion: "3.0.2", Security: true}) {
		t.Fatalf("pending = %+v", r.PendingUpdates)
	}
	if r.Truncated != (inventoryclient.CollectionLimits{Interfaces: 1, Addresses: 2, Guests: 3, Packages: 4, BMCPorts: 5}) {
		t.Fatalf("truncated = %+v", r.Truncated)
	}

	// Second page: digest view + cursor passthrough; digest row with no BMC.
	reps, next, err = c.ListHostReports(ctx, "t-1", inventoryclient.ReportFilter{Digest: true, Cursor: "c-2"})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastList.GetView() != invv1.HostReportView_HOST_REPORT_VIEW_DIGEST || f.lastList.GetCursor() != "c-2" ||
		f.lastList.GetChangedSince() != 0 || f.lastList.GetLimit() != 0 {
		t.Fatalf("request = %v", f.lastList)
	}
	if next != "" || len(reps) != 1 || reps[0].Host.Status != "retired" || reps[0].BMC != nil || reps[0].Digest != "ab" ||
		reps[0].ChangedAt.UnixMilli() != 5 || !reps[0].CollectedAt.IsZero() || reps[0].Updates.Status != "" {
		t.Fatalf("digest page = %+v next=%q", reps, next)
	}
}

func TestGetHostReport(t *testing.T) {
	f := &fakeReports{}
	c := dialReports(t, f, nil)
	r, err := c.GetHostReport(context.Background(), "t-1", "h-9")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastGet.GetTenantId() != "t-1" || f.lastGet.GetHostId() != "h-9" || r.Host.ID != "h-9" {
		t.Fatalf("get = %v / %+v", f.lastGet, r.Host)
	}
}

func TestHostReportErrorsPropagate(t *testing.T) {
	f := &fakeReports{fail: status.Error(codes.PermissionDenied, "not a consumer")}
	c := dialReports(t, f, nil)
	ctx := context.Background()
	if _, _, err := c.ListReportTenants(ctx, time.Time{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ListReportTenants err = %v", err)
	}
	if _, _, err := c.ListHostReports(ctx, "t", inventoryclient.ReportFilter{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ListHostReports err = %v", err)
	}
	if _, err := c.GetHostReport(ctx, "t", "h"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetHostReport err = %v", err)
	}
}

// newFieldsSnapshots serves a snapshot carrying every 020 Inventory field.
type newFieldsSnapshots struct {
	invv1.UnimplementedInventorySnapshotServiceServer
}

func (newFieldsSnapshots) GetLatestByHost(context.Context, *invv1.GetLatestByHostRequest) (*invv1.Snapshot, error) {
	fr := fullReport("h-1")
	return &invv1.Snapshot{
		Summary: &invv1.SnapshotSummary{Id: "s-1"},
		Payload: &invv1.Inventory{
			Os:                &invv1.OSInfo{Name: "Ubuntu", Family: "linux"},
			InstalledPrograms: []*invv1.Program{{Name: "openssl", Version: "3.0.1", AvailableVersion: "3.0.2", SecurityUpdate: true}},
			NetworkInterfaces: fr.GetNetworkInterfaces(),
			PrimaryIpv4:       fr.GetPrimaryIpv4(),
			PrimaryIpv6:       fr.GetPrimaryIpv6(),
			Virtualization:    fr.GetVirtualization(),
			Bmc:               fr.GetBmc(),
			HypervisorGuests:  fr.GetHypervisorGuests(),
			UpdateState:       fr.GetUpdateState(),
			Truncated:         fr.GetTruncated(),
		},
	}, nil
}

func TestToInventoryNewFields(t *testing.T) {
	c := dialReports(t, &fakeReports{}, newFieldsSnapshots{})
	s, err := c.GetLatestSnapshot(context.Background(), "t-1", "h-1")
	if err != nil {
		t.Fatal(err)
	}
	inv := s.Payload
	if inv.OS.Family != "linux" || len(inv.Programs) != 1 || inv.Programs[0].AvailableVersion != "3.0.2" || !inv.Programs[0].SecurityUpdate {
		t.Fatalf("os/programs = %+v %+v", inv.OS, inv.Programs)
	}
	if inv.PrimaryIPv4 != "192.0.2.10" || inv.PrimaryIPv6 != "2001:db8::10" || inv.Virtualization.Kind != "kvm" ||
		inv.BMC == nil || inv.BMC.Address != "10.0.0.5" || len(inv.Guests) != 1 || inv.Updates.Status != "updates_available" ||
		inv.Truncated.Packages != 4 || len(inv.Networks) != 1 || len(inv.Networks[0].Addresses) != 2 || inv.Networks[0].VLANID != 10 {
		t.Fatalf("inventory = %+v", inv)
	}
}
