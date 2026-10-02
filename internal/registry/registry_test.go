package registry

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMemoryRegisterDeliver(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()

	ch, unregister, err := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1", HostID: "h1", Version: "1.0"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer unregister()

	delivered, err := r.Deliver(ctx, "a1", Command{ID: "c1", Type: "refresh"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !delivered {
		t.Fatal("expected delivered=true")
	}

	select {
	case cmd := <-ch:
		if cmd.ID != "c1" || cmd.Type != "refresh" {
			t.Fatalf("unexpected command: %+v", cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("command not received on channel")
	}
}

func TestMemoryDeliverUnknownAgent(t *testing.T) {
	r := NewMemory()
	delivered, err := r.Deliver(context.Background(), "ghost", Command{ID: "c", Type: "refresh"})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if delivered {
		t.Fatal("expected delivered=false for unknown agent")
	}
}

func TestMemoryDeliverBufferFull(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	_, unregister, err := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer unregister()

	// Fill the buffer without reading, then the next Deliver must report false.
	for i := 0; i < deliverBuffer; i++ {
		if ok, _ := r.Deliver(ctx, "a1", Command{ID: "x", Type: "refresh"}); !ok {
			t.Fatalf("Deliver %d: expected true while buffer has room", i)
		}
	}
	if ok, _ := r.Deliver(ctx, "a1", Command{ID: "overflow", Type: "refresh"}); ok {
		t.Fatal("expected delivered=false when buffer is full")
	}
}

func TestMemoryUnregister(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	ch, unregister, err := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	online, _ := r.IsOnline(ctx, "a1")
	if !online {
		t.Fatal("expected online after Register")
	}

	unregister()
	unregister() // idempotent, must not panic or double-close

	if online, _ := r.IsOnline(ctx, "a1"); online {
		t.Fatal("expected offline after unregister")
	}
	if _, ok := <-ch; ok {
		t.Fatal("expected channel closed after unregister")
	}
	if delivered, _ := r.Deliver(ctx, "a1", Command{ID: "c"}); delivered {
		t.Fatal("expected delivered=false after unregister")
	}
}

func TestMemoryReconnectSupersedes(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	oldCh, oldUnreg, err := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Second Register for the same agent supersedes the first (channel closed).
	newCh, newUnreg, err := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1"})
	if err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	defer newUnreg()

	if _, ok := <-oldCh; ok {
		t.Fatal("expected superseded connection channel to be closed")
	}
	// Old unregister must not remove the new connection.
	oldUnreg()
	if online, _ := r.IsOnline(ctx, "a1"); !online {
		t.Fatal("new connection should remain online after stale unregister")
	}
	if ok, _ := r.Deliver(ctx, "a1", Command{ID: "c", Type: "refresh"}); !ok {
		t.Fatal("expected delivery to the new connection")
	}
	if cmd := <-newCh; cmd.ID != "c" {
		t.Fatalf("unexpected command on new channel: %+v", cmd)
	}
}

func TestMemoryListConnectedTenantIsolation(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	_, u1, _ := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1", HostID: "h1"})
	_, u2, _ := r.Register(ctx, ConnectedAgent{AgentID: "a2", TenantID: "t1", HostID: "h2"})
	_, u3, _ := r.Register(ctx, ConnectedAgent{AgentID: "b1", TenantID: "t2", HostID: "h3"})
	defer u1()
	defer u2()
	defer u3()

	t1, err := r.ListConnected(ctx, "t1")
	if err != nil {
		t.Fatalf("ListConnected: %v", err)
	}
	if len(t1) != 2 {
		t.Fatalf("tenant t1: want 2 agents, got %d (%+v)", len(t1), t1)
	}
	if t1[0].AgentID != "a1" || t1[1].AgentID != "a2" {
		t.Fatalf("expected sorted a1,a2; got %+v", t1)
	}
	if t1[0].ConnectedAt.IsZero() {
		t.Fatal("ConnectedAt should be defaulted on Register")
	}

	t2, _ := r.ListConnected(ctx, "t2")
	if len(t2) != 1 || t2[0].AgentID != "b1" {
		t.Fatalf("tenant t2 isolation broken: %+v", t2)
	}

	if got, _ := r.ListConnected(ctx, "none"); len(got) != 0 {
		t.Fatalf("unknown tenant: want empty, got %+v", got)
	}
}

func TestMemoryRegisterRejectsEmptyID(t *testing.T) {
	r := NewMemory()
	if _, _, err := r.Register(context.Background(), ConnectedAgent{TenantID: "t1"}); err != ErrInvalid {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestMemoryConnectedAtPreserved(t *testing.T) {
	ctx := context.Background()
	r := NewMemory()
	when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	_, u, _ := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1", ConnectedAt: when})
	defer u()
	list, _ := r.ListConnected(ctx, "t1")
	if len(list) != 1 || !list[0].ConnectedAt.Equal(when) {
		t.Fatalf("ConnectedAt not preserved: %+v", list)
	}
}

// TestCertificateCommandEncoding (feature 033): a certificate command carries
// ids only. It round-trips through the in-process registry and through the
// JSON the Valkey registry publishes, and that JSON has exactly the keys
// id, type, certificate.item_id and certificate.name (no material).
func TestCertificateCommandEncoding(t *testing.T) {
	cmd := Command{ID: "c1", Type: CommandCertificate, Certificate: &CertificatePayload{ItemID: "item-1", Name: "www"}}

	ctx := context.Background()
	r := NewMemory()
	ch, unregister, err := r.Register(ctx, ConnectedAgent{AgentID: "a1", TenantID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	if ok, err := r.Deliver(ctx, "a1", cmd); !ok || err != nil {
		t.Fatalf("deliver: %v %v", ok, err)
	}
	if got := <-ch; got.Type != CommandCertificate || got.Certificate == nil || *got.Certificate != *cmd.Certificate {
		t.Fatalf("memory round trip: %+v", got)
	}

	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var back Command
	if err := json.Unmarshal(raw, &back); err != nil || back.Certificate == nil || *back.Certificate != *cmd.Certificate || back.Upgrade != nil {
		t.Fatalf("json round trip: %+v %v", back, err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	var flat []string
	for k, v := range keys {
		if m, ok := v.(map[string]any); ok {
			for kk := range m {
				flat = append(flat, k+"."+kk)
			}
			continue
		}
		flat = append(flat, k)
	}
	sort.Strings(flat)
	if strings.Join(flat, ",") != "certificate.item_id,certificate.name,id,type" {
		t.Fatalf("certificate command JSON keys = %v (only ids may travel through Valkey)", flat)
	}
}
