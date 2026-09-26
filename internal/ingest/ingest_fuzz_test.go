package ingest

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// fuzzServer builds an ingest Server plus a pre-enrolled agent and returns a
// context already carrying the verified agent. It uses testing.TB so it can be
// called from a Fuzz function.
func fuzzServer(tb testing.TB, maxBytes int64) (*Server, context.Context) {
	tb.Helper()
	mem := memstore.New()
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		tb.Fatalf("NewEnvelope: %v", err)
	}
	enr := enroll.New(mem, env)
	snaps := snapshots.New(mem, hosts.New(mem), events.HubPublisher{})
	srv := New(enr, snaps, registry.NewMemory(), mem, maxBytes, "fuzz")

	ctx := context.Background()
	secret, _, err := enr.MintToken(ctx, "tenant-fuzz", "admin@example.com", "lab", time.Hour)
	if err != nil {
		tb.Fatalf("MintToken: %v", err)
	}
	agentID, cred, err := enr.Enroll(ctx, secret, store.Identity{Hostname: "fuzz-host", HardwareUUID: "hw-fuzz"}, "1.0.0")
	if err != nil {
		tb.Fatalf("Enroll: %v", err)
	}
	agent, err := enr.Verify(ctx, agentID, cred)
	if err != nil {
		tb.Fatalf("Verify: %v", err)
	}
	return srv, withAgent(context.Background(), agent)
}

// FuzzSubmitMapper feeds arbitrary proto Inventory bytes through the ingest
// proto->store mapper and the authenticated SubmitInventory path, asserting the
// service never panics and only ever returns clean gRPC status errors (never a
// bare/unknown error), including on the oversized path.
func FuzzSubmitMapper(f *testing.F) {
	seed, _ := proto.Marshal(sampleInventory("seed"))
	f.Add(seed)
	full, _ := proto.Marshal(&inventoryv1.Inventory{
		Identity:   &inventoryv1.Identity{Hostname: "h", MachineId: "m"},
		Memory:     &inventoryv1.MemoryInfo{Modules: []*inventoryv1.MemoryModule{{CapacityBytes: 1}}},
		Processors: []*inventoryv1.Processor{{CoreCount: 4}},
		Disks:      []*inventoryv1.Disk{{Partitions: []*inventoryv1.Partition{{Mount: "/"}}}},
	})
	f.Add(full)
	rich, _ := proto.Marshal(richInventory())
	f.Add(rich)
	f.Add([]byte{})
	f.Add([]byte{0xff, 0x00, 0x01, 0x02})
	f.Add([]byte("\x08\x96\x01"))

	srv, ctx := fuzzServer(f, 0)
	tiny, tinyCtx := fuzzServer(f, 8) // ceiling smaller than any real payload

	f.Fuzz(func(t *testing.T, data []byte) {
		pb := &inventoryv1.Inventory{}
		if err := proto.Unmarshal(data, pb); err != nil {
			return // not a valid Inventory encoding; nothing to map
		}
		// The pure mapper must never panic on any decoded message, and the
		// edge validation must leave every list within its bound.
		inv := inventoryFromProto(pb)
		validateExtended(&inv)
		assertBounds(t, inv)
		validateExtended(&inv) // idempotent: a validated payload is a fixed point
		assertBounds(t, inv)

		// The authenticated submit path must never panic and must only return
		// clean gRPC status codes.
		_, err := srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: pb})
		assertCleanErr(t, err)

		// Oversized path: a tiny ceiling must reject cleanly with InvalidArgument
		// (or accept a message that genuinely fits under 8 bytes).
		_, err = tiny.SubmitInventory(tinyCtx, &inventoryv1.SubmitRequest{Inventory: pb})
		if err != nil {
			if code := status.Code(err); code != codes.InvalidArgument && code != codes.Unavailable {
				t.Fatalf("oversized path returned unexpected code %v: %v", code, err)
			}
		}
	})
}

// assertCleanErr fails if err is set but is not a recognized gRPC status code
// (which would indicate an unhandled/leaked error rather than a clean rejection).
func assertCleanErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	switch status.Code(err) {
	case codes.OK, codes.InvalidArgument, codes.Unauthenticated, codes.Unavailable, codes.PermissionDenied:
		return
	default:
		t.Fatalf("unexpected error code %v: %v", status.Code(err), err)
	}
}

// richInventory carries every feature-020 field with valid values.
func richInventory() *inventoryv1.Inventory {
	inv := sampleInventory("rich")
	inv.Os.Family = "linux"
	inv.InstalledPrograms = []*inventoryv1.Program{{Name: "openssl", Version: "3.0.1", AvailableVersion: "3.0.2", SecurityUpdate: true}}
	inv.NetworkInterfaces = []*inventoryv1.NetworkInterface{{
		Name: "eth0", Mac: "00:11:22:33:44:55", Type: "ethernet", SpeedBps: 1e9, Up: true, Gateway: "192.0.2.1",
		Dhcp: true, DefaultRoute: true, VlanId: 10, Master: "br0", IpAddresses: []string{"192.0.2.10/24"},
		Addresses: []*inventoryv1.InterfaceAddress{{Address: "192.0.2.10", PrefixLength: 24, Family: "ipv4", Dhcp: true, Scope: "global"}},
	}}
	inv.PrimaryIpv4 = "192.0.2.10"
	inv.PrimaryIpv6 = "2001:db8::10"
	inv.Virtualization = &inventoryv1.Virtualization{Role: "vm", Kind: "kvm", Source: "dmi"}
	inv.Bmc = &inventoryv1.Bmc{Address: "10.0.0.5", PrefixLength: 24, Gateway: "10.0.0.1", IpSource: "static", VlanId: 7,
		Ports: []*inventoryv1.BmcPort{{Channel: 1, Mac: "00:aa:bb:cc:dd:ee", Address: "10.0.0.5"}}}
	inv.HypervisorGuests = []*inventoryv1.HypervisorGuest{{Id: "100", Name: "vm1", Kind: "vm", Platform: "proxmox", Macs: []string{"bc:24:11:00:00:01"}}}
	inv.UpdateState = &inventoryv1.UpdateState{PackageManager: "apt", Status: "updates_available", RebootRequired: "true",
		AutomaticUpdates: "false", SecurityClassified: true, CheckedAt: 1_700_000_000, PendingCount: 1, SecurityCount: 1}
	inv.Truncated = &inventoryv1.CollectionLimits{Packages: 1}
	return inv
}

func assertBounds(t *testing.T, inv store.Inventory) {
	t.Helper()
	if len(inv.Networks) > store.MaxInterfaces || len(inv.HypervisorGuests) > store.MaxGuests {
		t.Fatalf("bounds exceeded: %d interfaces, %d guests", len(inv.Networks), len(inv.HypervisorGuests))
	}
	for _, n := range inv.Networks {
		if len(n.Addresses) > store.MaxIfaceAddresses || len(n.IPAddresses) > store.MaxIfaceAddresses || n.Name == "" {
			t.Fatalf("interface out of bounds: %q %d/%d", n.Name, len(n.Addresses), len(n.IPAddresses))
		}
	}
	for _, g := range inv.HypervisorGuests {
		if len(g.MACs) > store.MaxGuestMACs || len(g.Name) > maxNameLen {
			t.Fatalf("guest out of bounds: %+v", g)
		}
	}
	if inv.Bmc != nil && len(inv.Bmc.Ports) > store.MaxBmcPorts {
		t.Fatalf("bmc ports = %d", len(inv.Bmc.Ports))
	}
	pending := 0
	for _, p := range inv.Programs {
		if p.AvailableVersion != "" {
			pending++
		}
	}
	if pending > store.MaxPendingUpdates {
		t.Fatalf("pending = %d", pending)
	}
}

func TestRichInventoryRoundTrip(t *testing.T) {
	inv := inventoryFromProto(richInventory())
	before := inv
	validateExtended(&inv)
	if inv.PrimaryIPv4 != before.PrimaryIPv4 || inv.Virtualization != before.Virtualization ||
		inv.Bmc == nil || inv.Bmc.Ports[0].MAC != "00:aa:bb:cc:dd:ee" || len(inv.HypervisorGuests) != 1 ||
		inv.UpdateState != before.UpdateState || inv.Truncated != before.Truncated ||
		inv.Networks[0].Addresses[0] != before.Networks[0].Addresses[0] || inv.Networks[0].Gateway != "192.0.2.1" ||
		inv.Programs[0].AvailableVersion != "3.0.2" || !inv.Programs[0].SecurityUpdate || inv.OS.Family != "linux" {
		t.Fatalf("valid values must survive validation:\n%+v", inv)
	}
}
