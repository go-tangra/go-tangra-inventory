package ingest

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Certificate delivery on the ingest edge (feature 033). Both RPCs are
// authenticated by the per-agent credential (auth.go) and bound to the
// verified agent's own delivery items. FetchCertificate is the only RPC that
// returns a private key: the response is built from memory, never logged
// (this edge has no payload-logging interceptor), and the key bytes are
// zeroed once the handler returns.

// CertDelivery is the relay the edge drives (internal/certdelivery).
type CertDelivery interface {
	OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error)
	Fetch(ctx context.Context, a store.Agent, itemID string) (*certdelivery.Material, error)
	Report(ctx context.Context, a store.Agent, r certdelivery.Report) (bool, error)
	RefusePlaintext(ctx context.Context, a store.Agent, itemID string)
}

// certEdge holds the certificate wiring and the fetch caps.
type certEdge struct {
	svc       CertDelivery
	plaintext bool // the edge serves without TLS and allow_plaintext_ingest is off
	slots     chan struct{}
	mu        sync.Mutex
	perAgent  map[string]bool
}

var errCertDisabled = status.Error(codes.FailedPrecondition, certdelivery.ErrDisabled.Error())

// WithCertDelivery attaches certificate delivery: at most maxFetches
// concurrent fetches (one per agent). refusePlaintext is set when the edge
// serves plaintext (ingest.insecure) without cert_delivery.
// allow_plaintext_ingest: fetches are then refused.
func (s *Server) WithCertDelivery(svc CertDelivery, maxFetches int, refusePlaintext bool) *Server {
	if maxFetches < 1 {
		maxFetches = 1
	}
	s.certEdge = &certEdge{svc: svc, plaintext: refusePlaintext, slots: make(chan struct{}, maxFetches), perAgent: map[string]bool{}}
	return s
}

func (e *certEdge) acquire(agentID string) bool {
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

func (e *certEdge) release(agentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.perAgent, agentID)
	<-e.slots
}

// certCommands returns the CERTIFICATE commands to send right after connect.
func (s *Server) certCommands(ctx context.Context, a store.Agent) []registry.Command {
	if s.certEdge == nil {
		return nil
	}
	cmds, err := s.certEdge.svc.OnConnect(ctx, a)
	if err != nil {
		return nil // replayed on the next connect
	}
	return cmds
}

// certStatus maps a relay error to a gRPC status with a stable message.
func certStatus(err error) error {
	var ie *certdelivery.InvalidError
	var fe *certdelivery.ItemFailedError
	switch {
	case errors.As(err, &ie):
		return status.Error(codes.InvalidArgument, ie.Error())
	case errors.As(err, &fe):
		return status.Error(codes.FailedPrecondition, fe.Error())
	case errors.Is(err, certdelivery.ErrDisabled):
		return errCertDisabled
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "no such certificate delivery for this agent")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// FetchCertificate serves the certificate bundle of one of the agent's own
// active delivery items.
func (s *Server) FetchCertificate(ctx context.Context, req *inventoryv1.FetchCertificateRequest) (*inventoryv1.CertificateBundle, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	e := s.certEdge
	if e == nil {
		return nil, errCertDisabled
	}
	if e.plaintext {
		e.svc.RefusePlaintext(ctx, agent, req.GetItemId())
		return nil, status.Error(codes.FailedPrecondition, "certificate delivery requires a TLS ingest edge")
	}
	if !e.acquire(agent.ID) {
		return nil, status.Error(codes.ResourceExhausted, "too many certificate fetches")
	}
	defer e.release(agent.ID)
	m, err := e.svc.Fetch(ctx, agent, req.GetItemId())
	if err != nil {
		return nil, certStatus(err)
	}
	defer m.Wipe()
	b := m.Bundle
	return &inventoryv1.CertificateBundle{
		ItemId: m.ItemID, Name: m.Name, CertificateId: m.CertificateID,
		CertPem: string(b.CertPEM), ChainPem: string(b.ChainPEM), KeyPem: string(b.KeyPEM), HasKey: b.HasKey,
		Serial: b.Serial, FingerprintSha256: b.Fingerprint, CommonName: b.CommonName, DnsNames: b.DNSNames, IpAddresses: b.IPAddresses,
		NotBefore: b.NotBefore.Unix(), NotAfter: b.NotAfter.Unix(), IsRenewal: m.IsRenewal, RerunHook: m.RerunHook,
	}, nil
}

// ReportCertificate records the agent's result for one of its own items.
func (s *Server) ReportCertificate(ctx context.Context, req *inventoryv1.ReportCertificateRequest) (*inventoryv1.ReportCertificateResponse, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	if s.certEdge == nil {
		return nil, errCertDisabled
	}
	accepted, err := s.certEdge.svc.Report(ctx, agent, certdelivery.Report{ItemID: req.GetItemId(), State: req.GetState(),
		Serial: req.GetSerial(), Fingerprint: req.GetFingerprintSha256(), Reason: req.GetReason(),
		HookExitCode: int(req.GetHookExitCode()), Detail: req.GetDetail()})
	if err != nil {
		return nil, certStatus(err)
	}
	return &inventoryv1.ReportCertificateResponse{Accepted: accepted}, nil
}
