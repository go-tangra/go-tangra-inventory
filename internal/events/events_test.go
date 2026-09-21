package events

import (
	"context"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/stream"
)

func TestNilHubPublisherIsNoOp(t *testing.T) {
	// A zero-value publisher (nil hub) must not panic.
	var p HubPublisher
	p.Publish(context.Background(), "t1", SnapshotReceived, map[string]any{"x": 1})

	var pub Publisher = HubPublisher{}
	pub.Publish(context.Background(), "t1", AgentOnline, nil)
}

func TestHubPublisherPublishes(t *testing.T) {
	hub := stream.NewHub(stream.NewMemory(), stream.Config{}, nil)
	defer hub.Close()
	p := HubPublisher{Hub: hub}
	// Non-nil hub path: publish must reach the bus without error/panic.
	p.Publish(context.Background(), "t1", SnapshotReceived,
		SnapshotReceivedPayload("h1", "s1", "host-1", time.Unix(0, 0)))
}

func TestSnapshotReceivedPayload(t *testing.T) {
	ts := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	p := SnapshotReceivedPayload("h1", "s1", "host-1", ts)
	if p["host_id"] != "h1" || p["snapshot_id"] != "s1" || p["hostname"] != "host-1" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p["collected_at"] != "2026-09-21T10:00:00Z" {
		t.Errorf("collected_at=%v", p["collected_at"])
	}
	assertNoSecrets(t, p)
}

func TestAgentPayloads(t *testing.T) {
	on := AgentOnlinePayload("a1", "h1", "host-1")
	off := AgentOfflinePayload("a1", "h1", "host-1")
	for _, p := range []map[string]any{on, off} {
		if p["agent_id"] != "a1" || p["host_id"] != "h1" || p["hostname"] != "host-1" {
			t.Fatalf("unexpected agent payload: %+v", p)
		}
		assertNoSecrets(t, p)
	}
}

func TestHostChangedPayload(t *testing.T) {
	p := HostChangedPayload("h1", 3)
	if p["host_id"] != "h1" || p["change_count"] != 3 {
		t.Fatalf("unexpected payload: %+v", p)
	}
	assertNoSecrets(t, p)
}

// assertNoSecrets guards the constitution's "payloads carry no credentials or
// serials" contract at the key level.
func assertNoSecrets(t *testing.T, p map[string]any) {
	t.Helper()
	for k := range p {
		switch k {
		case "credential", "credential_sealed", "token", "secret", "serial", "system_serial":
			t.Errorf("payload leaks sensitive key %q", k)
		}
	}
}
