package ingest

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/events"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/sealed"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/store"
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
		// The pure mapper must never panic on any decoded message.
		_ = inventoryFromProto(pb)

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
