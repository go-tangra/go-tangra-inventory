package hostreport

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func sampleHost() store.Host {
	return store.Host{
		ID: "0190f7c2-6a3e-7c1a-9b2e-aaaaaaaaaaaa", TenantID: tenant, Hostname: "srv1", SystemSerial: "SN1",
		Manufacturer: "Acme", Model: "X1", OSName: "Ubuntu", OSVersion: "24.04", OSArch: "amd64",
		MachineID: "mid", HardwareUUID: "hw", AgentVersion: "4.3.0", Status: store.HostActive,
		Tags:     map[string]string{"env": "prod"},
		LastSeen: time.Unix(1_700_000_100, 0).UTC(), ReportChangedAt: time.UnixMilli(1_700_000_000_123).UTC(),
	}
}

func sampleSnap() store.Snapshot {
	return store.Snapshot{
		ID: "snap-1", TenantID: tenant, HostID: "h", CollectedAt: time.Unix(1_700_000_000, 0).UTC(), AgentVersion: "4.3.0",
		Payload: store.Inventory{
			OS: store.OSInfo{Name: "Ubuntu", Family: "linux"},
			Programs: []store.Program{
				{Name: "curl", Version: "8.0"},
				{Name: "openssl", Version: "3.0.1", AvailableVersion: "3.0.2", SecurityUpdate: true},
				{Name: "vim", Version: "9.0", AvailableVersion: "9.1"},
			},
			Users:    []store.UserAccount{{Name: "root", IsAdmin: true}},
			Services: []store.Service{{Name: "sshd"}},
			Networks: []store.NetIface{{
				Name: "eth0", MAC: "00:11:22:33:44:55", Type: store.IfaceEthernet, SpeedBps: 1e9, Up: true, DHCP: true,
				Gateway: "192.0.2.1", DefaultRoute: true, IPAddresses: []string{"192.0.2.10/24"},
				Addresses: []store.IfAddress{{Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", DHCP: true, Scope: "global"}},
			}},
			PrimaryIPv4:    "192.0.2.10",
			PrimaryIPv6:    "2001:db8::10",
			Virtualization: store.Virtualization{Role: store.RoleVM, Kind: "kvm", Source: "dmi"},
			Bmc:            &store.Bmc{Address: "10.0.0.5", PrefixLength: 24, Ports: []store.BmcPort{{Channel: 1, MAC: "00:aa:bb:cc:dd:ee"}}},
			HypervisorGuests: []store.HypervisorGuest{{ID: "100", Name: "g", Kind: "vm", Platform: "proxmox",
				MACs: []string{"bc:24:11:00:00:01"}}},
			UpdateState: store.UpdateState{PackageManager: "apt", Status: store.UpdateAvailable, RebootRequired: store.TriTrue,
				AutomaticUpdates: store.TriFalse, SecurityClassified: true, CheckedAt: time.Unix(1_700_000_000, 0).UTC(),
				PendingCount: 2, SecurityCount: 1},
			Truncated: store.CollectionLimits{Packages: 3},
		},
	}
}

func TestProjectFieldMapping(t *testing.T) {
	r := Project(sampleHost(), sampleSnap())
	h := r.GetHost()
	if r.GetTenantId() != tenant || h.GetId() != sampleHost().ID || h.GetTenantId() != tenant || h.GetHostname() != "srv1" ||
		h.GetSystemSerial() != "SN1" || h.GetManufacturer() != "Acme" || h.GetModel() != "X1" || h.GetOsName() != "Ubuntu" ||
		h.GetOsVersion() != "24.04" || h.GetStatus() != invv1.HostStatus_HOST_STATUS_ACTIVE || h.GetLastSeen() != 1_700_000_100 {
		t.Fatalf("host = %v", h)
	}
	// Only the documented host fields are projected.
	if h.GetMachineId() != "" || h.GetHardwareUuid() != "" || len(h.GetTags()) != 0 || h.GetOsArch() != "" || h.GetAgentVersion() != "" {
		t.Fatalf("host carries non-projected fields: %v", h)
	}
	if r.GetSnapshotId() != "snap-1" || r.GetCollectedAt() != 1_700_000_000 || r.GetAgentVersion() != "4.3.0" ||
		r.GetOsFamily() != "linux" || r.GetReportChangedAt() != 1_700_000_000_123 {
		t.Fatalf("summary = %v", r)
	}
	if len(r.GetNetworkInterfaces()) != 1 || r.GetNetworkInterfaces()[0].GetAddresses()[0].GetAddress() != "192.0.2.10" ||
		r.GetPrimaryIpv4() != "192.0.2.10" || r.GetPrimaryIpv6() != "2001:db8::10" || r.GetVirtualization().GetKind() != "kvm" ||
		r.GetBmc().GetAddress() != "10.0.0.5" || len(r.GetHypervisorGuests()) != 1 || r.GetUpdateState().GetStatus() != store.UpdateAvailable ||
		r.GetTruncated().GetPackages() != 3 {
		t.Fatalf("report = %v", r)
	}
	pu := r.GetPendingUpdates()
	if len(pu) != 2 || pu[0].GetName() != "openssl" || pu[0].GetInstalledVersion() != "3.0.1" || pu[0].GetAvailableVersion() != "3.0.2" ||
		!pu[0].GetSecurity() || pu[1].GetName() != "vim" || pu[1].GetSecurity() {
		t.Fatalf("pending = %v", pu)
	}
	if len(r.GetReportDigest()) != 64 || r.GetReportDigest() != Digest(r) {
		t.Fatalf("digest = %q", r.GetReportDigest())
	}
}

func TestDigestIgnoresVolatileFields(t *testing.T) {
	base := Project(sampleHost(), sampleSnap())
	h := sampleHost()
	h.LastSeen = h.LastSeen.Add(time.Hour)
	h.ReportChangedAt = time.Time{}
	h.Tags = map[string]string{"other": "x"}
	s := sampleSnap()
	s.ID = "snap-2"
	s.CollectedAt = s.CollectedAt.Add(time.Hour)
	s.Payload.UpdateState.CheckedAt = s.Payload.UpdateState.CheckedAt.Add(time.Hour)
	s.Payload.Programs = append(s.Payload.Programs, store.Program{Name: "new-installed"}) // no update → not projected
	s.Payload.Users = nil
	if got := Project(h, s).GetReportDigest(); got != base.GetReportDigest() {
		t.Fatal("digest depends on last_seen/snapshot_id/collected_at/checked_at or non-projected data")
	}
	if Digest(Project(sampleHost(), sampleSnap())) != base.GetReportDigest() {
		t.Fatal("digest not deterministic")
	}
}

func TestDigestChangesWithProjectedFields(t *testing.T) {
	base := Project(sampleHost(), sampleSnap()).GetReportDigest()
	mut := []func(*store.Host, *store.Snapshot){
		func(_ *store.Host, s *store.Snapshot) { s.Payload.Networks[0].Addresses[0].Address = "192.0.2.11" },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.Networks[0].Type = store.IfaceBond },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.PrimaryIPv4 = "" },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.Virtualization.Role = store.RolePhysical },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.Bmc = nil },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.HypervisorGuests[0].MACs = nil },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.UpdateState.RebootRequired = store.TriFalse },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.Programs[1].AvailableVersion = "3.0.3" },
		func(_ *store.Host, s *store.Snapshot) { s.Payload.Truncated.Interfaces = 1 },
		func(_ *store.Host, s *store.Snapshot) { s.AgentVersion = "4.3.1" },
		func(h *store.Host, _ *store.Snapshot) { h.Hostname = "srv2" },
		func(h *store.Host, _ *store.Snapshot) { h.Status = store.HostRetired },
		func(h *store.Host, _ *store.Snapshot) { h.SystemSerial = "SN2" },
	}
	for i, m := range mut {
		h, s := sampleHost(), sampleSnap()
		m(&h, &s)
		if Project(h, s).GetReportDigest() == base {
			t.Errorf("mutation %d did not change the digest", i)
		}
	}
}

func TestOldAgentSnapshot(t *testing.T) {
	s := store.Snapshot{ID: "s", Payload: store.Inventory{
		OS:       store.OSInfo{Name: "Microsoft Windows 11 Pro"},
		Networks: []store.NetIface{{Name: "Ethernet", MAC: "00:11:22:33:44:55", IPAddresses: []string{"10.0.0.2/24"}}},
	}}
	r := Project(sampleHost(), s)
	if r.GetOsFamily() != "windows" || r.GetVirtualization() != nil || r.GetBmc() != nil || r.GetUpdateState() != nil ||
		r.GetTruncated() != nil || len(r.GetPendingUpdates()) != 0 || len(r.GetNetworkInterfaces()) != 1 {
		t.Fatalf("old agent report = %v", r)
	}
	s.Payload.OS.Name = "Debian GNU/Linux"
	if Project(sampleHost(), s).GetOsFamily() != "linux" {
		t.Fatal("linux fallback")
	}
	s.Payload.OS.Name = ""
	if Project(sampleHost(), s).GetOsFamily() != "linux" {
		t.Fatal("empty OS name falls back to linux")
	}
}

func TestPendingUpdatesBounded(t *testing.T) {
	s := sampleSnap()
	s.Payload.Programs = nil
	for i := 0; i < store.MaxPendingUpdates+10; i++ {
		s.Payload.Programs = append(s.Payload.Programs, store.Program{Name: fmt.Sprint("p", i), AvailableVersion: "2"})
	}
	if n := len(Project(sampleHost(), s).GetPendingUpdates()); n != store.MaxPendingUpdates {
		t.Fatalf("pending = %d", n)
	}
}

func TestDigestView(t *testing.T) {
	h := sampleHost()
	h.ReportDigest = "ab"
	h.Status = store.HostRetired
	d := DigestView(h)
	if d.GetTenantId() != tenant || d.GetHost().GetId() != h.ID || d.GetHost().GetStatus() != invv1.HostStatus_HOST_STATUS_RETIRED ||
		d.GetReportDigest() != "ab" || d.GetReportChangedAt() != h.ReportChangedAt.UnixMilli() {
		t.Fatalf("digest view = %v", d)
	}
	// A digest row never carries interface, package or guest data.
	full := Project(h, sampleSnap())
	full.NetworkInterfaces, full.PendingUpdates, full.HypervisorGuests = nil, nil, nil
	if len(d.GetNetworkInterfaces()) != 0 || len(d.GetPendingUpdates()) != 0 || len(d.GetHypervisorGuests()) != 0 ||
		d.GetBmc() != nil || d.GetUpdateState() != nil || d.GetSnapshotId() != "" {
		t.Fatalf("digest view leaks report data: %v", d)
	}
	h.ReportChangedAt = time.Time{}
	if DigestView(h).GetReportChangedAt() != 0 {
		t.Fatal("zero changed_at must map to 0")
	}
}

func TestStatusMapping(t *testing.T) {
	for st, want := range map[string]invv1.HostStatus{
		store.HostActive: invv1.HostStatus_HOST_STATUS_ACTIVE, store.HostStale: invv1.HostStatus_HOST_STATUS_STALE,
		store.HostRetired: invv1.HostStatus_HOST_STATUS_RETIRED, "": invv1.HostStatus_HOST_STATUS_UNSPECIFIED,
	} {
		h := sampleHost()
		h.Status = st
		h.LastSeen = time.Time{}
		got := Project(h, store.Snapshot{}).GetHost()
		if got.GetStatus() != want || got.GetLastSeen() != 0 {
			t.Errorf("status %q -> %v", st, got.GetStatus())
		}
	}
	if !proto.Equal(Project(sampleHost(), sampleSnap()), Project(sampleHost(), sampleSnap())) {
		t.Fatal("projection not deterministic")
	}
}
