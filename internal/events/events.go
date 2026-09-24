// Package events publishes inventory realtime events to the shared platform
// event bus so the gateway SSE hub relays them to the browser. The module only
// publishes (it consumes no external events); payloads carry no snapshot
// contents, credentials or hardware serial numbers.
package events

import (
	"context"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/stream"
)

// Event types published to platform:events:<tenant>.
const (
	SnapshotReceived = "inventory.snapshot.received"
	AgentOnline      = "inventory.agent.online"
	AgentOffline     = "inventory.agent.offline"
	HostChanged      = "inventory.host.changed"
)

// Publisher emits a realtime event to all of a tenant's subscribers.
type Publisher interface {
	Publish(ctx context.Context, tenantID, eventType string, payload any)
}

// HubPublisher publishes through the stream hub (nil hub is a no-op).
type HubPublisher struct{ Hub *stream.Hub }

// Publish broadcasts eventType to every subscriber of tenantID. A nil hub (or
// nil-hub publisher) is a safe no-op so callers need not branch on it.
func (p HubPublisher) Publish(ctx context.Context, tenantID, eventType string, payload any) {
	if p.Hub == nil {
		return
	}
	_, _ = p.Hub.PublishID(ctx, tenantID, nil, true, eventType, payload, true)
}

// SnapshotReceivedPayload is the (content-free) payload for a new snapshot.
func SnapshotReceivedPayload(hostID, snapshotID, hostname string, collectedAt time.Time) map[string]any {
	return map[string]any{
		"host_id":      hostID,
		"snapshot_id":  snapshotID,
		"hostname":     hostname,
		"collected_at": collectedAt.UTC().Format(time.RFC3339),
	}
}

// AgentOnlinePayload signals an agent transitioned to online.
func AgentOnlinePayload(agentID, hostID, hostname string) map[string]any {
	return agentPayload(agentID, hostID, hostname)
}

// AgentOfflinePayload signals an agent transitioned to offline.
func AgentOfflinePayload(agentID, hostID, hostname string) map[string]any {
	return agentPayload(agentID, hostID, hostname)
}

func agentPayload(agentID, hostID, hostname string) map[string]any {
	return map[string]any{"agent_id": agentID, "host_id": hostID, "hostname": hostname}
}

// HostChangedPayload reports that a snapshot produced changeCount changes.
func HostChangedPayload(hostID string, changeCount int) map[string]any {
	return map[string]any{"host_id": hostID, "change_count": changeCount}
}
