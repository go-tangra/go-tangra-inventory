package ingest

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/teleportjoin"
)

// Teleport join delivery on the ingest edge (feature 038). Both RPCs are
// authenticated by the per-agent credential (auth.go) and bound to the
// verified agent's own join items. FetchTeleportJoin answers with a
// single-use join token: it is relayed from memory and never logged (this
// edge has no payload-logging interceptor); the edge keeps no reference
// once the response is sent.

// TeleportJoin is the relay the edge drives (internal/teleportjoin).
type TeleportJoin interface {
	OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error)
	Fetch(ctx context.Context, a store.Agent, itemID string) (*inventoryv1.TeleportJoinMaterial, error)
	Report(ctx context.Context, a store.Agent, r teleportjoin.Report) (bool, error)
	RefusePlaintext(ctx context.Context, a store.Agent, itemID string)
}

// teleportEdge holds the Teleport join wiring and the fetch caps.
type teleportEdge struct {
	svc       TeleportJoin
	plaintext bool // the edge serves without TLS and allow_plaintext_ingest is off
	slots     chan struct{}
	mu        sync.Mutex
	perAgent  map[string]bool
}

var errTeleportDisabled = status.Error(codes.FailedPrecondition, teleportjoin.ErrDisabled.Error())

// WithTeleportJoin attaches Teleport join delivery: at most maxFetches
// concurrent fetches (one per agent). refusePlaintext is set when the edge
// serves plaintext (ingest.insecure) without
// teleport_join.allow_plaintext_ingest: fetches are then refused.
func (s *Server) WithTeleportJoin(svc TeleportJoin, maxFetches int, refusePlaintext bool) *Server {
	if maxFetches < 1 {
		maxFetches = 1
	}
	s.teleportEdge = &teleportEdge{svc: svc, plaintext: refusePlaintext, slots: make(chan struct{}, maxFetches), perAgent: map[string]bool{}}
	return s
}

func (e *teleportEdge) acquire(agentID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.perAgent[agentID] {
		return false
	}
	select {
	case e.slots <- struct{}{}:
	default:
		return false
	}
	e.perAgent[agentID] = true
	return true
}

func (e *teleportEdge) release(agentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.perAgent, agentID)
	<-e.slots
}

// teleportCommands returns the TELEPORT_JOIN commands to send right after
// connect.
func (s *Server) teleportCommands(ctx context.Context, a store.Agent) []registry.Command {
	if s.teleportEdge == nil {
		return nil
	}
	cmds, err := s.teleportEdge.svc.OnConnect(ctx, a)
	if err != nil {
		return nil // replayed on the next connect
	}
	return cmds
}

// teleportStatus maps a relay error to a gRPC status with a stable message.
func teleportStatus(err error) error {
	var ie *teleportjoin.InvalidError
	switch {
	case errors.As(err, &ie):
		return status.Error(codes.InvalidArgument, ie.Error())
	case errors.Is(err, teleportjoin.ErrDisabled):
		return errTeleportDisabled
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "no such teleport join for this agent")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// FetchTeleportJoin answers the join material of one of the agent's own
// active items (item id set).
func (s *Server) FetchTeleportJoin(ctx context.Context, req *inventoryv1.FetchTeleportJoinRequest) (*inventoryv1.TeleportJoinMaterial, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	e := s.teleportEdge
	if e == nil {
		return nil, errTeleportDisabled
	}
	if e.plaintext {
		e.svc.RefusePlaintext(ctx, agent, req.GetItemId())
		return nil, status.Error(codes.FailedPrecondition, "teleport join requires a TLS ingest edge")
	}
	if !e.acquire(agent.ID) {
		return nil, status.Error(codes.ResourceExhausted, "too many teleport join fetches")
	}
	defer e.release(agent.ID)
	m, err := e.svc.Fetch(ctx, agent, req.GetItemId())
	if err != nil {
		return nil, teleportStatus(err)
	}
	return m, nil
}

// ReportTeleportJoin records the agent's result for one of its own items.
func (s *Server) ReportTeleportJoin(ctx context.Context, req *inventoryv1.ReportTeleportJoinRequest) (*inventoryv1.ReportTeleportJoinResponse, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	if s.teleportEdge == nil {
		return nil, errTeleportDisabled
	}
	accepted, err := s.teleportEdge.svc.Report(ctx, agent, teleportjoin.Report{ItemID: req.GetItemId(), State: req.GetState(),
		Reason: req.GetReason(), NodeHostUUID: req.GetNodeHostUuid(), Detail: req.GetDetail()})
	if err != nil {
		return nil, teleportStatus(err)
	}
	return &inventoryv1.ReportTeleportJoinResponse{Accepted: accepted}, nil
}
