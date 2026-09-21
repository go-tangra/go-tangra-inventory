// Package registry is the shared agent-connection registry for the off-mesh
// agent plane. Long-poll/stream handlers Register a connected agent and read
// push commands (e.g. "refresh") from the returned channel; any service
// instance can Deliver a command to an agent regardless of which instance holds
// the connection. A single-instance in-memory implementation (NewMemory) and a
// Valkey-backed cross-instance implementation (NewValkey) are provided.
package registry

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrInvalid is returned when a required field (e.g. AgentID) is missing.
var ErrInvalid = errors.New("registry: invalid connected agent")

// deliverBuffer bounds queued commands per connection before Deliver reports
// the agent as not currently reachable (slow/stuck reader).
const deliverBuffer = 16

// Command is a control message pushed to a connected agent.
type Command struct {
	ID   string `json:"id"`
	Type string `json:"type"` // e.g. "refresh"
}

// ConnectedAgent is the metadata of a live agent connection.
type ConnectedAgent struct {
	AgentID     string    `json:"agent_id"`
	TenantID    string    `json:"tenant_id"`
	HostID      string    `json:"host_id"`
	Version     string    `json:"version"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Registry is the shared connection registry contract.
type Registry interface {
	// Register records a live connection and returns a channel of pushed
	// commands plus an unregister func (idempotent) that removes the
	// connection and marks the agent offline.
	Register(ctx context.Context, a ConnectedAgent) (<-chan Command, func(), error)
	// Deliver pushes a command to a connected agent. delivered is false (with a
	// nil error) when the agent is not currently connected/reachable.
	Deliver(ctx context.Context, agentID string, cmd Command) (delivered bool, err error)
	// ListConnected returns the agents currently connected for a tenant.
	ListConnected(ctx context.Context, tenantID string) ([]ConnectedAgent, error)
	// IsOnline reports whether an agent currently has a live connection.
	IsOnline(ctx context.Context, agentID string) (bool, error)
}

// Memory is a single-instance, in-process Registry. Push commands are only
// delivered to connections held by this process; use NewValkey to fan out
// across instances.
type Memory struct {
	mu    sync.Mutex
	conns map[string]*memConn
}

type memConn struct {
	meta ConnectedAgent
	ch   chan Command
}

// NewMemory returns an empty in-process registry.
func NewMemory() *Memory { return &Memory{conns: map[string]*memConn{}} }

// Register implements Registry.
func (m *Memory) Register(_ context.Context, a ConnectedAgent) (<-chan Command, func(), error) {
	if a.AgentID == "" {
		return nil, nil, ErrInvalid
	}
	if a.ConnectedAt.IsZero() {
		a.ConnectedAt = time.Now()
	}
	c := &memConn{meta: a, ch: make(chan Command, deliverBuffer)}

	m.mu.Lock()
	// Supersede a stale connection for the same agent (e.g. reconnect).
	if old, ok := m.conns[a.AgentID]; ok {
		close(old.ch)
	}
	m.conns[a.AgentID] = c
	m.mu.Unlock()

	var once sync.Once
	unregister := func() {
		once.Do(func() {
			m.mu.Lock()
			if cur, ok := m.conns[a.AgentID]; ok && cur == c {
				delete(m.conns, a.AgentID)
				close(c.ch)
			}
			m.mu.Unlock()
		})
	}
	return c.ch, unregister, nil
}

// Deliver implements Registry.
func (m *Memory) Deliver(_ context.Context, agentID string, cmd Command) (bool, error) {
	m.mu.Lock()
	c, ok := m.conns[agentID]
	m.mu.Unlock()
	if !ok {
		return false, nil
	}
	select {
	case c.ch <- cmd:
		return true, nil
	default:
		// Reader is not keeping up; treat as not currently deliverable.
		return false, nil
	}
}

// ListConnected implements Registry (tenant-scoped).
func (m *Memory) ListConnected(_ context.Context, tenantID string) ([]ConnectedAgent, error) {
	m.mu.Lock()
	out := make([]ConnectedAgent, 0, len(m.conns))
	for _, c := range m.conns {
		if c.meta.TenantID == tenantID {
			out = append(out, c.meta)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out, nil
}

// IsOnline implements Registry.
func (m *Memory) IsOnline(_ context.Context, agentID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.conns[agentID]
	return ok, nil
}

var _ Registry = (*Memory)(nil)
