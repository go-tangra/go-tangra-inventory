package sender_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const aeTenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

// An agent enrolls over TLS with an auto-enrollment key from 127.0.0.1 and
// submits inventory; a key limited to another network is refused.
func TestTLSIngest_AutoEnroll(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.issueServer(t, "localhost")
	mem := memstore.New()
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	auto := autoenroll.New(mem, env)
	srv := ingest.New(enroll.New(mem, env), snapshots.New(mem, hosts.New(mem), events.HubPublisher{}), registry.NewMemory(), mem, 0, "ae").
		WithAutoEnroll(auto)
	loader, err := ingest.NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	gs := ingest.NewGRPCServer(srv, loader.TransportOption())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	ctx := context.Background()
	actor := autoenroll.Actor{Kind: "user", ID: "u1"}
	if _, err := auto.SetEnabled(ctx, aeTenant, actor, true); err != nil {
		t.Fatal(err)
	}
	loop, loopSecret, err := auto.CreateKey(ctx, aeTenant, actor, autoenroll.KeyInput{Name: "loopback", AllowedCIDRs: []string{"127.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	far, farSecret, err := auto.CreateKey(ctx, aeTenant, actor, autoenroll.KeyInput{Name: "far", AllowedCIDRs: []string{"192.0.2.0/24"}})
	if err != nil {
		t.Fatal(err)
	}

	s := sender.New(lis.Addr().String(), sender.Options{CAFile: ca.writeCA(t)})
	ident := store.Identity{Hostname: "auto-host", HardwareUUID: "uuid-auto"}
	agentID, cred, err := s.EnrollAuto(ctx, loopSecret, loop.KeyID, ident, "1.0.0")
	if err != nil {
		t.Fatalf("EnrollAuto: %v", err)
	}
	if _, err := s.Submit(ctx, agentID, cred, store.Inventory{Identity: ident, CollectedAt: time.Now(), AgentVersion: "1.0.0"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if a, err := mem.GetAgent(ctx, aeTenant, agentID); err != nil || a.EnrolledVia != store.EnrolledViaAuto {
		t.Fatalf("agent %+v %v", a, err)
	}
	_, _, err = s.EnrollAuto(ctx, farSecret, far.KeyID, ident, "1.0.0")
	if status.Code(unwrap(err)) != codes.PermissionDenied {
		t.Fatalf("outside the key's networks: %v", err)
	}
	// A wrong secret is refused the same way.
	_, _, err = s.EnrollAuto(ctx, "aks_wrong", loop.KeyID, ident, "1.0.0")
	if status.Code(unwrap(err)) != codes.PermissionDenied {
		t.Fatalf("wrong secret: %v", err)
	}
	// Unreachable endpoint: a dial/transport error.
	if _, _, err := sender.New("127.0.0.1:1", sender.Options{CAFile: ca.writeCA(t)}).EnrollAuto(ctxShort(t), loopSecret, loop.KeyID, ident, "1.0.0"); err == nil {
		t.Fatal("unreachable endpoint accepted")
	}
}

func ctxShort(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// unwrap returns the innermost error carrying a gRPC status.
func unwrap(err error) error {
	for e := err; e != nil; {
		if _, ok := status.FromError(e); ok {
			return e
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return err
}
