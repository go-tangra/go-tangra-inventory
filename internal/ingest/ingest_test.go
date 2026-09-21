package ingest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/events"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/sealed"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

// harness wires a real ingest Server over an in-memory store, registry, and the
// real enroll + snapshots services.
type harness struct {
	srv    *Server
	enroll *enroll.Service
	reg    *registry.Memory
	mem    *memstore.Mem
}

func newHarness(t *testing.T, maxBytes int64) *harness {
	t.Helper()
	mem := memstore.New()
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	enr := enroll.New(mem, env)
	snaps := snapshots.New(mem, hosts.New(mem), events.HubPublisher{})
	reg := registry.NewMemory()
	srv := New(enr, snaps, reg, mem, maxBytes, "test-instance")
	return &harness{srv: srv, enroll: enr, reg: reg, mem: mem}
}

// mintAndEnroll mints a token for tenantID and enrolls an agent, returning the
// agent id and plaintext credential.
func (h *harness) mintAndEnroll(t *testing.T, tenantID, hostname string) (agentID, credential string) {
	t.Helper()
	ctx := context.Background()
	secret, _, err := h.enroll.MintToken(ctx, tenantID, "admin@example.com", "lab", time.Hour)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	resp, err := h.srv.Enroll(ctx, &inventoryv1.EnrollRequest{
		EnrollmentToken: secret,
		Identity:        &inventoryv1.Identity{Hostname: hostname, HardwareUuid: "uuid-" + hostname},
		AgentVersion:    "1.0.0",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.GetAgentId() == "" || resp.GetAgentCredential() == "" {
		t.Fatalf("Enroll returned empty id/credential")
	}
	return resp.GetAgentId(), resp.GetAgentCredential()
}

// authedCtx runs the unary interceptor's authentication against metadata and
// returns the context carrying the verified agent.
func (h *harness) authedCtx(t *testing.T, agentID, credential string) context.Context {
	t.Helper()
	md := metadata.Pairs(metaAgentIDKey, agentID, metaCredentialKey, credential)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	authed, err := h.srv.authenticate(ctx)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return authed
}

func sampleInventory(hostname string) *inventoryv1.Inventory {
	return &inventoryv1.Inventory{
		Identity:     &inventoryv1.Identity{Hostname: hostname, HardwareUuid: "uuid-" + hostname, MachineId: "mid-" + hostname},
		CollectedAt:  time.Now().Unix(),
		AgentVersion: "1.0.0",
		Os:           &inventoryv1.OSInfo{Name: "Ubuntu", Version: "24.04", Arch: "amd64"},
		System:       &inventoryv1.SystemInfo{Manufacturer: "Acme", ProductName: "Box", SerialNumber: "SN123"},
		NetworkInterfaces: []*inventoryv1.NetworkInterface{
			{Name: "eth0", Mac: "00:11:22:33:44:55", IpAddresses: []string{"10.0.0.5"}, Up: true},
		},
		Disks: []*inventoryv1.Disk{
			{Model: "SSD", SizeBytes: 512 << 30, Partitions: []*inventoryv1.Partition{{Mount: "/", Fs: "ext4"}}},
		},
	}
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("got err=%v (code %v), want code %v", err, status.Code(err), want)
	}
}

func TestEnroll_IssuesCredentialAndSubmitStores(t *testing.T) {
	h := newHarness(t, 0)
	agentID, cred := h.mintAndEnroll(t, "tenant-1", "host-a")

	ctx := h.authedCtx(t, agentID, cred)
	resp, err := h.srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: sampleInventory("host-a")})
	if err != nil {
		t.Fatalf("SubmitInventory: %v", err)
	}
	if resp.GetSnapshotId() == "" || resp.GetHostId() == "" || resp.GetReceivedAt() == 0 {
		t.Fatalf("incomplete SubmitResponse: %+v", resp)
	}

	// Snapshot + host must be stored under the TOKEN's tenant (tenant-1).
	if _, err := h.mem.GetSnapshot(context.Background(), "tenant-1", resp.GetSnapshotId()); err != nil {
		t.Fatalf("snapshot not stored under tenant-1: %v", err)
	}
	host, err := h.mem.GetHost(context.Background(), "tenant-1", resp.GetHostId())
	if err != nil {
		t.Fatalf("host not stored under tenant-1: %v", err)
	}
	if host.Hostname != "host-a" {
		t.Fatalf("host hostname = %q, want host-a", host.Hostname)
	}

	// Agent liveness/host binding is touched.
	ag, err := h.mem.GetAgentByID(context.Background(), agentID)
	if err != nil {
		t.Fatalf("GetAgentByID: %v", err)
	}
	if ag.HostID != resp.GetHostId() {
		t.Fatalf("agent HostID = %q, want %q", ag.HostID, resp.GetHostId())
	}

	// The credential must never surface in the submit response.
	if strings.Contains(resp.GetSnapshotId()+resp.GetHostId(), cred) {
		t.Fatalf("credential leaked into SubmitResponse")
	}
}

func TestSubmit_NoCredential_Unauthenticated(t *testing.T) {
	h := newHarness(t, 0)
	// Direct call with a context that carries no verified agent.
	_, err := h.srv.SubmitInventory(context.Background(), &inventoryv1.SubmitRequest{Inventory: sampleInventory("host-a")})
	requireCode(t, err, codes.Unauthenticated)

	// Same through the interceptor with no metadata at all.
	interceptor := h.srv.UnaryInterceptor()
	_, err = interceptor(context.Background(), &inventoryv1.SubmitRequest{Inventory: sampleInventory("host-a")},
		&grpc.UnaryServerInfo{FullMethod: inventoryv1.IngestService_SubmitInventory_FullMethodName},
		func(ctx context.Context, req any) (any, error) {
			return h.srv.SubmitInventory(ctx, req.(*inventoryv1.SubmitRequest))
		})
	requireCode(t, err, codes.Unauthenticated)
}

func TestSubmit_BadCredential_Unauthenticated(t *testing.T) {
	h := newHarness(t, 0)
	agentID, _ := h.mintAndEnroll(t, "tenant-1", "host-a")

	md := metadata.Pairs(metaAgentIDKey, agentID, metaCredentialKey, "wrong-credential")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := h.srv.authenticate(ctx)
	requireCode(t, err, codes.Unauthenticated)

	// Unknown agent id also rejects.
	md = metadata.Pairs(metaAgentIDKey, "no-such-agent", metaCredentialKey, "x")
	ctx = metadata.NewIncomingContext(context.Background(), md)
	_, err = h.srv.authenticate(ctx)
	requireCode(t, err, codes.Unauthenticated)
}

func TestSubmit_Oversized_InvalidArgument(t *testing.T) {
	h := newHarness(t, 16) // tiny ceiling: any real inventory exceeds it
	agentID, cred := h.mintAndEnroll(t, "tenant-1", "host-a")
	ctx := h.authedCtx(t, agentID, cred)

	_, err := h.srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: sampleInventory("host-a")})
	requireCode(t, err, codes.InvalidArgument)

	// No partial write: no snapshot exists for the tenant's host.
	if _, err := h.mem.GetHostByIdentity(context.Background(), "tenant-1", store.Identity{Hostname: "host-a", HardwareUUID: "uuid-host-a", MachineID: "mid-host-a"}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("expected no host written on oversized payload, got err=%v", err)
	}
}

func TestSubmit_MissingInventory_InvalidArgument(t *testing.T) {
	h := newHarness(t, 0)
	agentID, cred := h.mintAndEnroll(t, "tenant-1", "host-a")
	ctx := h.authedCtx(t, agentID, cred)
	_, err := h.srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

func TestEnroll_BadToken_PermissionDenied(t *testing.T) {
	h := newHarness(t, 0)
	_, err := h.srv.Enroll(context.Background(), &inventoryv1.EnrollRequest{
		EnrollmentToken: "not-a-real-token",
		Identity:        &inventoryv1.Identity{Hostname: "host-a"},
		AgentVersion:    "1.0.0",
	})
	requireCode(t, err, codes.PermissionDenied)
}

func TestEnroll_MissingIdentity_InvalidArgument(t *testing.T) {
	h := newHarness(t, 0)
	_, err := h.srv.Enroll(context.Background(), &inventoryv1.EnrollRequest{EnrollmentToken: "x"})
	requireCode(t, err, codes.InvalidArgument)
}

// A second agent in another tenant, reporting the SAME host identity, must
// write into ITS OWN tenant and can never touch the first tenant's host.
func TestTenantIsolation(t *testing.T) {
	h := newHarness(t, 0)

	a1, c1 := h.mintAndEnroll(t, "tenant-1", "shared-host")
	resp1, err := h.srv.SubmitInventory(h.authedCtx(t, a1, c1), &inventoryv1.SubmitRequest{Inventory: sampleInventory("shared-host")})
	if err != nil {
		t.Fatalf("submit tenant-1: %v", err)
	}

	a2, c2 := h.mintAndEnroll(t, "tenant-2", "shared-host")
	resp2, err := h.srv.SubmitInventory(h.authedCtx(t, a2, c2), &inventoryv1.SubmitRequest{Inventory: sampleInventory("shared-host")})
	if err != nil {
		t.Fatalf("submit tenant-2: %v", err)
	}

	if resp1.GetHostId() == resp2.GetHostId() {
		t.Fatalf("tenant-2 wrote tenant-1's host id %q", resp1.GetHostId())
	}
	// tenant-2's host is invisible under tenant-1 and vice versa.
	if _, err := h.mem.GetHost(context.Background(), "tenant-1", resp2.GetHostId()); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("tenant-2 host visible under tenant-1: %v", err)
	}
	if _, err := h.mem.GetHost(context.Background(), "tenant-2", resp1.GetHostId()); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("tenant-1 host visible under tenant-2: %v", err)
	}
	// Each host lives under its own tenant.
	if _, err := h.mem.GetHost(context.Background(), "tenant-1", resp1.GetHostId()); err != nil {
		t.Fatalf("tenant-1 host missing: %v", err)
	}
	if _, err := h.mem.GetHost(context.Background(), "tenant-2", resp2.GetHostId()); err != nil {
		t.Fatalf("tenant-2 host missing: %v", err)
	}
}

// fakeStream is a minimal grpc.ServerStreamingServer[Command] for tests.
type fakeStream struct {
	ctx  context.Context
	sent chan *inventoryv1.Command
}

func (f *fakeStream) Send(c *inventoryv1.Command) error { f.sent <- c; return nil }
func (f *fakeStream) Context() context.Context          { return f.ctx }
func (f *fakeStream) SetHeader(metadata.MD) error       { return nil }
func (f *fakeStream) SendHeader(metadata.MD) error      { return nil }
func (f *fakeStream) SetTrailer(metadata.MD)            {}
func (f *fakeStream) SendMsg(any) error                 { return nil }
func (f *fakeStream) RecvMsg(any) error                 { return nil }

func TestStreamCommands_ForwardsAndUnregisters(t *testing.T) {
	h := newHarness(t, 0)
	agentID, _ := h.mintAndEnroll(t, "tenant-1", "host-a")
	agent := store.Agent{ID: agentID, TenantID: "tenant-1", HostID: "host-x", AgentVersion: "1.0.0"}

	ctx, cancel := context.WithCancel(withAgent(context.Background(), agent))
	fs := &fakeStream{ctx: ctx, sent: make(chan *inventoryv1.Command, 4)}

	done := make(chan error, 1)
	go func() { done <- h.srv.StreamCommands(&inventoryv1.StreamRequest{AgentId: agentID}, fs) }()

	// Wait until the connection is registered, then deliver a command.
	waitOnline(t, h.reg, agentID, true)
	ok, err := h.reg.Deliver(context.Background(), agentID, registry.Command{ID: "cmd-1", Type: "refresh"})
	if err != nil || !ok {
		t.Fatalf("Deliver ok=%v err=%v", ok, err)
	}

	select {
	case c := <-fs.sent:
		if c.GetCommandId() != "cmd-1" || c.GetType() != inventoryv1.CommandType_COMMAND_TYPE_REFRESH {
			t.Fatalf("unexpected command: %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for streamed command")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamCommands did not return after cancel")
	}
	// Unregister on exit marks the agent offline.
	waitOnline(t, h.reg, agentID, false)
}

func TestStreamCommands_NoCredential_Unauthenticated(t *testing.T) {
	h := newHarness(t, 0)
	fs := &fakeStream{ctx: context.Background(), sent: make(chan *inventoryv1.Command, 1)}
	err := h.srv.StreamCommands(&inventoryv1.StreamRequest{AgentId: "x"}, fs)
	requireCode(t, err, codes.Unauthenticated)
}

func waitOnline(t *testing.T, reg registry.Registry, agentID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		online, err := reg.IsOnline(context.Background(), agentID)
		if err != nil {
			t.Fatalf("IsOnline: %v", err)
		}
		if online == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("agent %s online=%v not reached", agentID, want)
}

func TestInventoryFromProto_MapsAllComponents(t *testing.T) {
	now := time.Now().Unix()
	pb := &inventoryv1.Inventory{
		Identity:     &inventoryv1.Identity{HardwareUuid: "hw", MachineId: "mid", Hostname: "h"},
		CollectedAt:  now,
		AgentVersion: "9.9",
		Os:           &inventoryv1.OSInfo{Name: "OS", Version: "1", Build: "b", Arch: "arm64", Kernel: "k", InstallDate: now, LastBoot: now, UptimeSec: 42},
		Bios:         &inventoryv1.BIOSInfo{Vendor: "v", Version: "2", ReleaseDate: "2020"},
		System:       &inventoryv1.SystemInfo{Manufacturer: "m", ProductName: "p", Version: "v", SerialNumber: "sn", Uuid: "u", WakeUpType: "w", SkuNumber: "sku", Family: "f"},
		Baseboard:    &inventoryv1.BaseboardInfo{Manufacturer: "bm", Product: "bp", Version: "bv", SerialNumber: "bsn", AssetTag: "at", LocationInChassis: "loc", BoardType: "bt"},
		Chassis:      &inventoryv1.ChassisInfo{Manufacturer: "cm", Version: "cv", SerialNumber: "csn", AssetTag: "cat", SkuNumber: "csku", Type: "ct"},
		Processors:   []*inventoryv1.Processor{{SocketDesignation: "CPU0", Manufacturer: "Intel", MaxSpeedMhz: 3000, CoreCount: 8, ThreadCount: 16, SocketPopulated: true}},
		Cache:        []*inventoryv1.CacheInfo{{SocketDesignation: "L1"}},
		Memory: &inventoryv1.MemoryInfo{
			TotalPhysicalBytes: 1024,
			Array:              &inventoryv1.MemoryArray{Location: "sys", Use: "sysmem", ErrorCorrection: "ecc", MaximumCapacity: 4096, NumberOfDevices: 2},
			Modules:            []*inventoryv1.MemoryModule{{DeviceLocator: "DIMM0", CapacityBytes: 512, SpeedMtS: 3200, ConfiguredSpeedMtS: 3000, Manufacturer: "Sk"}},
		},
		Ports:             []string{"USB"},
		Slots:             []string{"PCIe"},
		OemStrings:        []string{"oem"},
		BiosLanguage:      "en",
		Monitors:          []*inventoryv1.Monitor{{Manufacturer: "Dell", Model: "U2720", SerialNumber: "ms"}},
		InstalledPrograms: []*inventoryv1.Program{{Name: "prog", Version: "1", Publisher: "pub", SizeBytes: 10}},
		Services:          []*inventoryv1.Service{{Name: "svc", DisplayName: "Svc", State: "running", StartMode: "auto", Account: "root"}},
		Users:             []*inventoryv1.UserAccount{{Name: "alice", IsAdmin: true, LastLogon: now}},
		Patches:           []*inventoryv1.Patch{{Id: "KB1", InstalledOn: "2021"}},
		Environment:       &inventoryv1.Environment{Domain: "d", Workgroup: "w", Timezone: "UTC", Locale: "en_US"},
		NetworkInterfaces: []*inventoryv1.NetworkInterface{{Name: "eth0", Mac: "mac", IpAddresses: []string{"1.2.3.4"}, Subnet: "s", Gateway: "g", Dns: []string{"8.8.8.8"}, Dhcp: true, SpeedBps: 1000, Type: "ethernet", Up: true}},
		Disks:             []*inventoryv1.Disk{{Model: "SSD", Serial: "ds", SizeBytes: 999, MediaType: "ssd", Interface: "nvme", Partitions: []*inventoryv1.Partition{{Mount: "/", Fs: "ext4", SizeBytes: 500, FreeBytes: 100}}}},
	}
	inv := inventoryFromProto(pb)
	if inv.Identity.Hostname != "h" || inv.AgentVersion != "9.9" {
		t.Fatalf("identity/version not mapped: %+v", inv.Identity)
	}
	if inv.OS.Arch != "arm64" || inv.OS.UptimeSec != 42 || inv.OS.InstallDate.IsZero() {
		t.Fatalf("os not mapped: %+v", inv.OS)
	}
	if len(inv.Processors) != 1 || inv.Processors[0].CoreCount != 8 || !inv.Processors[0].SocketPopulated {
		t.Fatalf("processors not mapped: %+v", inv.Processors)
	}
	if inv.Memory.TotalPhysicalBytes != 1024 || len(inv.Memory.Modules) != 1 || inv.Memory.Modules[0].SpeedMTs != 3200 {
		t.Fatalf("memory not mapped: %+v", inv.Memory)
	}
	if len(inv.Networks) != 1 || inv.Networks[0].MAC != "mac" || !inv.Networks[0].DHCP {
		t.Fatalf("networks not mapped: %+v", inv.Networks)
	}
	if len(inv.Disks) != 1 || len(inv.Disks[0].Partitions) != 1 || inv.Disks[0].Partitions[0].FS != "ext4" {
		t.Fatalf("disks not mapped: %+v", inv.Disks)
	}
	if len(inv.Users) != 1 || !inv.Users[0].IsAdmin || inv.Users[0].LastLogon.IsZero() {
		t.Fatalf("users not mapped: %+v", inv.Users)
	}
	if len(inv.Monitors) != 1 || len(inv.Programs) != 1 || len(inv.Services) != 1 || len(inv.Patches) != 1 || len(inv.Cache) != 1 {
		t.Fatalf("repeated components not mapped")
	}
	// A zero collected_at maps to the zero time (downstream defaults to now).
	if got := unixToTime(0); !got.IsZero() {
		t.Fatalf("unixToTime(0) = %v, want zero", got)
	}
}

func TestUnaryInterceptor_EnrollPassThrough(t *testing.T) {
	h := newHarness(t, 0)
	called := false
	_, err := h.srv.UnaryInterceptor()(context.Background(), &inventoryv1.EnrollRequest{},
		&grpc.UnaryServerInfo{FullMethod: inventoryv1.IngestService_Enroll_FullMethodName},
		func(ctx context.Context, req any) (any, error) {
			called = true
			return &inventoryv1.EnrollResponse{}, nil
		})
	if err != nil || !called {
		t.Fatalf("enroll should pass through unauthenticated: called=%v err=%v", called, err)
	}
}

func TestStreamInterceptor_RejectsMissingCredential(t *testing.T) {
	h := newHarness(t, 0)
	fs := &fakeStream{ctx: context.Background(), sent: make(chan *inventoryv1.Command, 1)}
	err := h.srv.StreamInterceptor()(nil, fs,
		&grpc.StreamServerInfo{FullMethod: inventoryv1.IngestService_StreamCommands_FullMethodName},
		func(srv any, ss grpc.ServerStream) error { return nil })
	requireCode(t, err, codes.Unauthenticated)
}

func TestServerBuildHelpers(t *testing.T) {
	h := newHarness(t, 0)
	if got := h.srv.MaxBytes(); got != defaultMaxBytes {
		t.Fatalf("MaxBytes = %d, want default %d", got, defaultMaxBytes)
	}
	if h.srv.InstanceID() != "test-instance" {
		t.Fatalf("InstanceID = %q", h.srv.InstanceID())
	}
	if opts := h.srv.ServerOptions(); len(opts) != 3 {
		t.Fatalf("ServerOptions len = %d, want 3", len(opts))
	}
	gs := NewGRPCServer(h.srv)
	if gs == nil {
		t.Fatal("NewGRPCServer returned nil")
	}
	gs.Stop()
}
