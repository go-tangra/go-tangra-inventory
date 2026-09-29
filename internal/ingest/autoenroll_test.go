package ingest

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	proof "github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func peerCtx(addr net.Addr) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: addr})
}

// An auto-enrolled agent submits inventory into the key's tenant.
func TestAutoEnroll_EndToEnd(t *testing.T) {
	h := newHarness(t, 0)
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	svc := autoenroll.New(h.mem, env)
	h.srv.WithAutoEnroll(svc)
	actor := autoenroll.Actor{Kind: "user", ID: "u1"}
	const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	if _, err := svc.SetEnabled(context.Background(), tenant, actor, true); err != nil {
		t.Fatal(err)
	}
	k, secret, err := svc.CreateKey(context.Background(), tenant, actor, autoenroll.KeyInput{Name: "lab", AllowedCIDRs: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	id := &inventoryv1.Identity{Hostname: "host-a", HardwareUuid: "uuid-host-a"}
	p, _ := proof.NewProof(secret, k.KeyID, proof.Identity{HardwareUUID: "uuid-host-a", Hostname: "host-a"}, time.Now())
	req := &inventoryv1.EnrollRequest{Identity: id, AgentVersion: "1.0.0", AutoEnroll: p}

	inside := peerCtx(&net.TCPAddr{IP: net.ParseIP("10.9.8.7"), Port: 5555})
	resp, err := h.srv.Enroll(inside, req)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	sub, err := h.srv.SubmitInventory(h.authedCtx(t, resp.GetAgentId(), resp.GetAgentCredential()),
		&inventoryv1.SubmitRequest{Inventory: sampleInventory("host-a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.mem.GetHost(context.Background(), tenant, sub.GetHostId()); err != nil {
		t.Fatalf("host not in the key's tenant: %v", err)
	}

	// Outside the networks, replayed, or with a token too: refused.
	p2, _ := proof.NewProof(secret, k.KeyID, proof.Identity{HardwareUUID: "uuid-host-a", Hostname: "host-a"}, time.Now())
	_, err = h.srv.Enroll(peerCtx(&net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1}), &inventoryv1.EnrollRequest{Identity: id, AutoEnroll: p2})
	requireCode(t, err, codes.PermissionDenied)
	_, err = h.srv.Enroll(inside, req)
	requireCode(t, err, codes.PermissionDenied)
	_, err = h.srv.Enroll(inside, &inventoryv1.EnrollRequest{Identity: id, AutoEnroll: p2, EnrollmentToken: "x"})
	requireCode(t, err, codes.InvalidArgument)
	// Oversized identity.
	_, err = h.srv.Enroll(inside, &inventoryv1.EnrollRequest{Identity: &inventoryv1.Identity{Hostname: strings.Repeat("h", 254)}, AutoEnroll: p2})
	requireCode(t, err, codes.InvalidArgument)
	// No peer address at all.
	_, err = h.srv.Enroll(context.Background(), &inventoryv1.EnrollRequest{Identity: id, AutoEnroll: p2})
	requireCode(t, err, codes.PermissionDenied)
}

func TestAutoEnroll_NotWired(t *testing.T) {
	h := newHarness(t, 0)
	_, err := h.srv.Enroll(context.Background(), &inventoryv1.EnrollRequest{Identity: &inventoryv1.Identity{Hostname: "h"}, AutoEnroll: &inventoryv1.AutoEnrollProof{}})
	requireCode(t, err, codes.PermissionDenied)
}

type failingAuto struct{}

func (failingAuto) Enroll(context.Context, *inventoryv1.AutoEnrollProof, store.Identity, string, netip.Addr) (string, string, error) {
	return "", "", errors.New("db down")
}

func TestAutoEnroll_Transient(t *testing.T) {
	h := newHarness(t, 0)
	h.srv.WithAutoEnroll(failingAuto{})
	_, err := h.srv.Enroll(context.Background(), &inventoryv1.EnrollRequest{Identity: &inventoryv1.Identity{Hostname: "h"}, AutoEnroll: &inventoryv1.AutoEnrollProof{}})
	requireCode(t, err, codes.Unavailable)
}

type strAddr string

func (a strAddr) Network() string { return "tcp" }
func (a strAddr) String() string  { return string(a) }

func TestPeerAddr(t *testing.T) {
	cases := map[string]struct {
		ctx  context.Context
		want string
	}{
		"tcp v4":     {peerCtx(&net.TCPAddr{IP: net.ParseIP("10.1.1.1"), Port: 1}), "10.1.1.1"},
		"tcp mapped": {peerCtx(&net.TCPAddr{IP: net.ParseIP("::ffff:10.1.1.2"), Port: 1}), "10.1.1.2"},
		"tcp v6":     {peerCtx(&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1}), "2001:db8::1"},
		"string":     {peerCtx(strAddr("10.2.2.2:9")), "10.2.2.2"},
		"unix":       {peerCtx(&net.UnixAddr{Name: "/tmp/s", Net: "unix"}), "invalid IP"},
		"nil addr":   {peerCtx(nil), "invalid IP"},
		"no peer":    {context.Background(), "invalid IP"},
		"bad tcp":    {peerCtx(&net.TCPAddr{IP: net.IP{1, 2, 3}}), "invalid IP"},
	}
	for name, c := range cases {
		if got := peerAddr(c.ctx).String(); got != c.want {
			t.Errorf("%s: %s", name, got)
		}
	}
}
