package ingest

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/moduledelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Module delivery on the ingest edge (feature 037). Both RPCs are
// authenticated by the per-agent credential (auth.go) and bound to the
// verified agent's own delivery items. FetchModuleBundle streams a bundle
// that holds secrets: it is relayed from memory, never logged (this edge has
// no payload-logging interceptor), and the buffer is zeroed once the
// handler returns.

// ModuleDelivery is the relay the edge drives (internal/moduledelivery).
type ModuleDelivery interface {
	OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error)
	Fetch(ctx context.Context, a store.Agent, itemID string) (*moduledelivery.Bundle, error)
	Report(ctx context.Context, a store.Agent, r moduledelivery.Report) (bool, error)
	RefusePlaintext(ctx context.Context, a store.Agent, itemID string)
}

// moduleEdge holds the module wiring and the fetch caps.
type moduleEdge struct {
	svc       ModuleDelivery
	plaintext bool // the edge serves without TLS and allow_plaintext_ingest is off
	slots     chan struct{}
	mu        sync.Mutex
	perAgent  map[string]bool
}

var errModuleDisabled = status.Error(codes.FailedPrecondition, moduledelivery.ErrDisabled.Error())

// WithModuleDelivery attaches module delivery: at most maxFetches
// concurrent fetches (one per agent). refusePlaintext is set when the edge
// serves plaintext (ingest.insecure) without module_delivery.
// allow_plaintext_ingest: fetches are then refused.
func (s *Server) WithModuleDelivery(svc ModuleDelivery, maxFetches int, refusePlaintext bool) *Server {
	if maxFetches < 1 {
		maxFetches = 1
	}
	s.moduleEdge = &moduleEdge{svc: svc, plaintext: refusePlaintext, slots: make(chan struct{}, maxFetches), perAgent: map[string]bool{}}
	return s
}

func (e *moduleEdge) acquire(agentID string) bool {
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

func (e *moduleEdge) release(agentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.perAgent, agentID)
	<-e.slots
}

// moduleCommands returns the MODULE commands to send right after connect.
func (s *Server) moduleCommands(ctx context.Context, a store.Agent) []registry.Command {
	if s.moduleEdge == nil {
		return nil
	}
	cmds, err := s.moduleEdge.svc.OnConnect(ctx, a)
	if err != nil {
		return nil // replayed on the next connect
	}
	return cmds
}

// moduleStatus maps a relay error to a gRPC status with a stable message.
func moduleStatus(err error) error {
	var ie *moduledelivery.InvalidError
	switch {
	case errors.As(err, &ie):
		return status.Error(codes.InvalidArgument, ie.Error())
	case errors.Is(err, moduledelivery.ErrDisabled):
		return errModuleDisabled
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "no such module delivery for this agent")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// FetchModuleBundle streams the rendered bundle of one of the agent's own
// active module items: the header (item id set) first, then chunks of at
// most 1 MiB in order.
func (s *Server) FetchModuleBundle(req *inventoryv1.FetchModuleBundleRequest, stream inventoryv1.IngestService_FetchModuleBundleServer) error {
	ctx := stream.Context()
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "agent credential required")
	}
	e := s.moduleEdge
	if e == nil {
		return errModuleDisabled
	}
	if e.plaintext {
		e.svc.RefusePlaintext(ctx, agent, req.GetItemId())
		return status.Error(codes.FailedPrecondition, "module delivery requires a TLS ingest edge")
	}
	if !e.acquire(agent.ID) {
		return status.Error(codes.ResourceExhausted, "too many module fetches")
	}
	defer e.release(agent.ID)
	b, err := e.svc.Fetch(ctx, agent, req.GetItemId())
	if err != nil {
		return moduleStatus(err)
	}
	defer b.Wipe()
	if err := stream.Send(&inventoryv1.ModuleBundleChunk{Part: &inventoryv1.ModuleBundleChunk_Header{Header: &inventoryv1.ModuleBundleHeader{
		ItemId: b.ItemID, Module: b.Module, Version: b.Version, Size: int64(len(b.Data)), Sha256: b.SHA256, DeliveryId: b.DeliveryID,
	}}}); err != nil {
		return err
	}
	for off := 0; off < len(b.Data); off += moduledelivery.MaxChunkBytes {
		end := min(off+moduledelivery.MaxChunkBytes, len(b.Data))
		if err := stream.Send(&inventoryv1.ModuleBundleChunk{Part: &inventoryv1.ModuleBundleChunk_Chunk{
			Chunk: &inventoryv1.ArtifactChunk{Offset: int64(off), Data: b.Data[off:end]}}}); err != nil {
			return err
		}
	}
	return nil
}

// ReportModule records the agent's result for one of its own items.
func (s *Server) ReportModule(ctx context.Context, req *inventoryv1.ReportModuleRequest) (*inventoryv1.ReportModuleResponse, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	if s.moduleEdge == nil {
		return nil, errModuleDisabled
	}
	accepted, err := s.moduleEdge.svc.Report(ctx, agent, moduledelivery.Report{ItemID: req.GetItemId(), State: req.GetState(),
		Reason: req.GetReason(), HookExitCode: int(req.GetHookExitCode()), Detail: req.GetDetail()})
	if err != nil {
		return nil, moduleStatus(err)
	}
	return &inventoryv1.ReportModuleResponse{Accepted: accepted}, nil
}
