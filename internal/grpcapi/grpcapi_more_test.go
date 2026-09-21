package grpcapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/backup"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

func TestGrpcErrorMappings(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"hosts not found", hosts.ErrNotFound, codes.NotFound},
		{"snapshots not found", snapshots.ErrNotFound, codes.NotFound},
		{"repo not found", repo.ErrNotFound, codes.NotFound},
		{"forbidden", authz.ErrForbidden, codes.PermissionDenied},
		{"bad schema", backup.ErrBadSchema, codes.InvalidArgument},
		{"token invalid", enroll.ErrTokenInvalid, codes.InvalidArgument},
		{"conflict", repo.ErrConflict, codes.FailedPrecondition},
		{"unauthenticated", enroll.ErrUnauthenticated, codes.Unauthenticated},
		{"unknown", errors.New("boom"), codes.Unavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := status.Code(grpcError(tc.err)); got != tc.want {
				t.Fatalf("grpcError(%v) code = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestEnumMappersRoundTrip(t *testing.T) {
	for _, s := range []string{store.HostActive, store.HostStale, store.HostRetired} {
		if got := hostStatusFromPB(hostStatusToPB(s)); got != s {
			t.Fatalf("host status round trip %q -> %q", s, got)
		}
	}
	if hostStatusToPB("bogus") != invv1.HostStatus_HOST_STATUS_UNSPECIFIED {
		t.Fatal("unknown host status should map to UNSPECIFIED")
	}
	if hostStatusFromPB(invv1.HostStatus_HOST_STATUS_UNSPECIFIED) != "" {
		t.Fatal("UNSPECIFIED host status should map to empty")
	}
	for _, s := range []string{store.SourceAgent, store.SourceManual, store.SourceImport} {
		if snapshotSourceToPB(s) == invv1.SnapshotSource_SNAPSHOT_SOURCE_UNSPECIFIED {
			t.Fatalf("source %q mapped to UNSPECIFIED", s)
		}
	}
	if snapshotSourceToPB("bogus") != invv1.SnapshotSource_SNAPSHOT_SOURCE_UNSPECIFIED {
		t.Fatal("unknown source should map to UNSPECIFIED")
	}
	for _, s := range []string{store.ChangeAdded, store.ChangeRemoved, store.ChangeModified} {
		if changeTypeToPB(s) == invv1.ChangeType_CHANGE_TYPE_UNSPECIFIED {
			t.Fatalf("change type %q mapped to UNSPECIFIED", s)
		}
	}
	if changeTypeToPB("bogus") != invv1.ChangeType_CHANGE_TYPE_UNSPECIFIED {
		t.Fatal("unknown change type should map to UNSPECIFIED")
	}
}

func TestInventoryToPBFull(t *testing.T) {
	now := time.Now().UTC()
	inv := store.Inventory{
		Identity:     store.Identity{HardwareUUID: "hw", MachineID: "mid", Hostname: "h"},
		CollectedAt:  now,
		AgentVersion: "9.9",
		OS:           store.OSInfo{Name: "OS", Arch: "arm64", UptimeSec: 42, InstallDate: now, LastBoot: now},
		BIOS:         store.BIOSInfo{Vendor: "v"},
		System:       store.SystemInfo{Manufacturer: "m", SerialNumber: "sn"},
		Baseboard:    store.BaseboardInfo{Manufacturer: "bm"},
		Chassis:      store.ChassisInfo{Manufacturer: "cm"},
		Memory: store.MemoryInfo{
			TotalPhysicalBytes: 1024,
			Array:              store.MemoryArray{Location: "sys", NumberOfDevices: 2},
			Modules:            []store.MemoryModule{{DeviceLocator: "DIMM0", CapacityBytes: 512, SpeedMTs: 3200}},
		},
		Ports:        []string{"USB"},
		Slots:        []string{"PCIe"},
		OEMStrings:   []string{"oem"},
		BIOSLanguage: "en",
		Environment:  store.Environment{Domain: "d", Timezone: "UTC"},
		Processors:   []store.Processor{{SocketDesignation: "CPU0", CoreCount: 8, SocketPopulated: true}},
		Cache:        []store.CacheInfo{{SocketDesignation: "L1"}},
		Monitors:     []store.Monitor{{Manufacturer: "Dell", Model: "U2720"}},
		Programs:     []store.Program{{Name: "prog", Version: "1"}},
		Services:     []store.Service{{Name: "svc", State: "running"}},
		Users:        []store.UserAccount{{Name: "alice", IsAdmin: true, LastLogon: now}},
		Patches:      []store.Patch{{ID: "KB1", InstalledOn: "2021"}},
		Networks:     []store.NetIface{{Name: "eth0", MAC: "mac", IPAddresses: []string{"1.2.3.4"}, DNS: []string{"8.8.8.8"}, DHCP: true, Up: true}},
		Disks:        []store.Disk{{Model: "SSD", SizeBytes: 999, Partitions: []store.Partition{{Mount: "/", FS: "ext4"}}}},
	}
	pb := inventoryToPB(inv)
	if pb.GetIdentity().GetHostname() != "h" || pb.GetAgentVersion() != "9.9" {
		t.Fatalf("identity/version: %+v", pb.GetIdentity())
	}
	if pb.GetOs().GetArch() != "arm64" || pb.GetOs().GetInstallDate() == 0 || pb.GetOs().GetLastBoot() == 0 {
		t.Fatalf("os: %+v", pb.GetOs())
	}
	if len(pb.GetProcessors()) != 1 || pb.GetProcessors()[0].GetCoreCount() != 8 || !pb.GetProcessors()[0].GetSocketPopulated() {
		t.Fatalf("processors: %+v", pb.GetProcessors())
	}
	if pb.GetMemory().GetTotalPhysicalBytes() != 1024 || len(pb.GetMemory().GetModules()) != 1 || pb.GetMemory().GetModules()[0].GetSpeedMtS() != 3200 {
		t.Fatalf("memory: %+v", pb.GetMemory())
	}
	if len(pb.GetNetworkInterfaces()) != 1 || !pb.GetNetworkInterfaces()[0].GetDhcp() {
		t.Fatalf("networks: %+v", pb.GetNetworkInterfaces())
	}
	if len(pb.GetDisks()) != 1 || len(pb.GetDisks()[0].GetPartitions()) != 1 {
		t.Fatalf("disks: %+v", pb.GetDisks())
	}
	if len(pb.GetUsers()) != 1 || !pb.GetUsers()[0].GetIsAdmin() || pb.GetUsers()[0].GetLastLogon() == 0 {
		t.Fatalf("users: %+v", pb.GetUsers())
	}
	if len(pb.GetCache()) != 1 || len(pb.GetMonitors()) != 1 || len(pb.GetInstalledPrograms()) != 1 ||
		len(pb.GetServices()) != 1 || len(pb.GetPatches()) != 1 {
		t.Fatal("repeated components not mapped")
	}
	// unix() zero-time path.
	if unix(time.Time{}) != 0 {
		t.Fatal("unix(zero) should be 0")
	}
}

func TestRefreshInventoryDelivered(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()

	// Register a live connection whose HostID matches the refresh target.
	commands, unregister, err := k.agents.Registry.Register(ctx, registry.ConnectedAgent{
		AgentID: "agent-1", TenantID: tenant, HostID: "host-9", Version: "1.0", ConnectedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer unregister()

	ref, err := k.agents.RefreshInventory(ctx, &invv1.RefreshInventoryRequest{TenantId: tenant, HostId: "host-9"})
	if err != nil || !ref.GetDelivered() || ref.GetCommandId() == "" {
		t.Fatalf("refresh delivered: %v %+v", err, ref)
	}
	select {
	case cmd := <-commands:
		if cmd.Type != "refresh" {
			t.Fatalf("unexpected command: %+v", cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("command not delivered to connection")
	}

	// ListConnectedAgents surfaces the registered agent (exercises connectedAgentToPB).
	list, err := k.agents.ListConnectedAgents(ctx, &invv1.ListConnectedAgentsRequest{TenantId: tenant})
	if err != nil || len(list.GetAgents()) != 1 {
		t.Fatalf("list connected: %v %+v", err, list)
	}
	if a := list.GetAgents()[0]; a.GetAgentId() != "agent-1" || a.GetHostId() != "host-9" || !a.GetOnline() {
		t.Fatalf("connected agent mapping: %+v", a)
	}
}

func TestAgentServerNilDeps(t *testing.T) {
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()
	// No registry / no enroll wired: RPCs should degrade to Unavailable.
	s := &AgentServer{}
	if _, err := s.ListConnectedAgents(ctx, &invv1.ListConnectedAgentsRequest{TenantId: tenant}); status.Code(err) != codes.Unavailable {
		t.Fatalf("ListConnectedAgents nil registry: %v", err)
	}
	if _, err := s.RefreshInventory(ctx, &invv1.RefreshInventoryRequest{TenantId: tenant, HostId: "x"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("RefreshInventory nil registry: %v", err)
	}
	if _, err := s.MintEnrollmentToken(ctx, &invv1.MintEnrollmentTokenRequest{TenantId: tenant}); status.Code(err) != codes.Unavailable {
		t.Fatalf("Mint nil enroll: %v", err)
	}
	if _, err := s.RevokeAgent(ctx, &invv1.RevokeAgentRequest{TenantId: tenant, Id: "x"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("Revoke nil enroll: %v", err)
	}
}

func TestErrorAndAuthPaths(t *testing.T) {
	k := newKit(t)
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	ctx := context.Background()

	// GetHostByIdentity with nil identity -> InvalidArgument.
	if _, err := k.hosts.GetHostByIdentity(ctx, &invv1.GetHostByIdentityRequest{TenantId: tenant}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil identity: %v", err)
	}
	// GetHostByIdentity no match -> NotFound.
	if _, err := k.hosts.GetHostByIdentity(ctx, &invv1.GetHostByIdentityRequest{TenantId: tenant, Identity: &invv1.Identity{HardwareUuid: "none"}}); status.Code(err) != codes.NotFound {
		t.Fatalf("no match identity: %v", err)
	}
	// Missing host on Get/Tag/Retire/Delete -> NotFound.
	if _, err := k.hosts.TagHost(ctx, &invv1.TagHostRequest{TenantId: tenant, Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("tag missing: %v", err)
	}
	if _, err := k.hosts.RetireHost(ctx, &invv1.RetireHostRequest{TenantId: tenant, Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("retire missing: %v", err)
	}
	if _, err := k.hosts.DeleteHost(ctx, &invv1.DeleteHostRequest{TenantId: tenant, Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := k.snapshots.GetSnapshot(ctx, &invv1.GetSnapshotRequest{TenantId: tenant, Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("get snapshot missing: %v", err)
	}
	if _, err := k.snapshots.DeleteSnapshot(ctx, &invv1.DeleteSnapshotRequest{TenantId: tenant, Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("delete snapshot missing: %v", err)
	}
	if _, err := k.snapshots.GetLatestByHost(ctx, &invv1.GetLatestByHostRequest{TenantId: tenant, HostId: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("latest missing host: %v", err)
	}

	// Unauthenticated across every server.
	withFakeCaller(t, "", false)
	if _, err := k.snapshots.ListSnapshots(ctx, &invv1.ListSnapshotsRequest{TenantId: tenant, HostId: "h"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("snapshots unauth: %v", err)
	}
	if _, err := k.stats.GetStatistics(ctx, &invv1.GetStatisticsRequest{TenantId: tenant}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stats unauth: %v", err)
	}
	if _, err := k.agents.MintEnrollmentToken(ctx, &invv1.MintEnrollmentTokenRequest{TenantId: tenant}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("mint unauth: %v", err)
	}

	// Invalid tenant across servers.
	withFakeCaller(t, "spiffe://example.org/svc/deployer", true)
	if _, err := k.hosts.ListHosts(ctx, &invv1.ListHostsRequest{TenantId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("list bad tenant: %v", err)
	}
}

// stubRegistrar records which inventory.v1 servers Register wires up.
type stubRegistrar struct{ n int }

func (s *stubRegistrar) RegisterService(_ *grpc.ServiceDesc, _ any) { s.n++ }

func TestRegister(t *testing.T) {
	k := newKit(t)
	reg := &stubRegistrar{}
	Register(reg, Deps{
		Hosts:     k.hosts.Hosts,
		Snapshots: k.snapshots.Snapshots,
		Stats:     k.stats.Stats,
		Enroll:    k.agents.Enroll,
		Registry:  k.agents.Registry,
	})
	// host + snapshot + stats + agent services = 4.
	if reg.n != 4 {
		t.Fatalf("Register wired %d services, want 4", reg.n)
	}
	// Empty deps wire nothing.
	empty := &stubRegistrar{}
	Register(empty, Deps{})
	if empty.n != 0 {
		t.Fatalf("empty Register wired %d services, want 0", empty.n)
	}
}
