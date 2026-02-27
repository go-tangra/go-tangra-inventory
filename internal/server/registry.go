package server

import (
	"fmt"
	"sync"
	"time"

	collectorv1 "github.com/go-tangra/go-tangra-inventory/gen/go/inventory/collector/v1"
)

const commandChannelBufferSize = 16

// connectedAgent holds the command channel and metadata for a connected agent.
type connectedAgent struct {
	ch          chan *collectorv1.InventoryCommand
	version     string
	connectedAt time.Time
}

// ConnectedAgentInfo is a read-only snapshot of a connected agent's metadata.
type ConnectedAgentInfo struct {
	ClientID    string
	Version     string
	ConnectedAt time.Time
}

// CommandRegistry manages in-memory command channels for connected agents.
type CommandRegistry struct {
	mu     sync.RWMutex
	agents map[string]*connectedAgent
}

// NewCommandRegistry creates a new CommandRegistry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		agents: make(map[string]*connectedAgent),
	}
}

// Register creates a buffered channel for the given agent.
// If one already exists, it is closed first.
func (r *CommandRegistry) Register(clientID, version string) <-chan *collectorv1.InventoryCommand {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.agents[clientID]; ok {
		close(old.ch)
	}
	ch := make(chan *collectorv1.InventoryCommand, commandChannelBufferSize)
	r.agents[clientID] = &connectedAgent{
		ch:          ch,
		version:     version,
		connectedAt: time.Now(),
	}
	return ch
}

// Unregister closes and removes the channel for the given agent.
func (r *CommandRegistry) Unregister(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if a, ok := r.agents[clientID]; ok {
		close(a.ch)
		delete(r.agents, clientID)
	}
}

// Send sends an inventory command to a connected agent.
// Returns an error if the agent is not connected or the channel is full.
func (r *CommandRegistry) Send(clientID string, cmd *collectorv1.InventoryCommand) error {
	r.mu.RLock()
	a, ok := r.agents[clientID]
	r.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent %s not connected", clientID)
	}

	select {
	case a.ch <- cmd:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timeout sending command to agent %s", clientID)
	}
}

// IsConnected checks whether an agent has an active channel.
func (r *CommandRegistry) IsConnected(clientID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[clientID]
	return ok
}

// ListConnected returns a snapshot of all currently connected agents.
func (r *CommandRegistry) ListConnected() []ConnectedAgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ConnectedAgentInfo, 0, len(r.agents))
	for id, a := range r.agents {
		result = append(result, ConnectedAgentInfo{
			ClientID:    id,
			Version:     a.version,
			ConnectedAt: a.connectedAt,
		})
	}
	return result
}
