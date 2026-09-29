package ingest

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// AutoEnroller verifies automatic enrollments (autoenroll.Service).
type AutoEnroller interface {
	Enroll(ctx context.Context, p *inventoryv1.AutoEnrollProof, ident store.Identity, agentVersion string, peer netip.Addr) (agentID, credential string, err error)
}

// WithAutoEnroll enables automatic enrollment (feature 029) on the edge.
func (s *Server) WithAutoEnroll(a AutoEnroller) *Server {
	s.auto = a
	return s
}

// autoEnroll handles an EnrollRequest carrying an auto-enrollment proof. The
// caller's address is the transport peer (the ingest listener is not behind
// a proxy); a request carrying both a token and a proof is refused. Every
// refusal is the same PermissionDenied.
func (s *Server) autoEnroll(ctx context.Context, req *inventoryv1.EnrollRequest, ident store.Identity) (*inventoryv1.EnrollResponse, error) {
	if req.GetEnrollmentToken() != "" {
		return nil, status.Error(codes.InvalidArgument, "send either an enrollment token or an auto-enrollment proof")
	}
	if s.auto == nil {
		return nil, status.Error(codes.PermissionDenied, "enrollment rejected")
	}
	// Bound the unauthenticated strings stored with the agent and in audit.
	if len(ident.Hostname) > maxHostnameLen || len(ident.MachineID) > maxIDLen || len(ident.HardwareUUID) > maxIDLen || len(req.GetAgentVersion()) > maxVersionLen {
		return nil, status.Error(codes.InvalidArgument, "identity or agent version too long")
	}
	agentID, credential, err := s.auto.Enroll(ctx, req.GetAutoEnroll(), ident, req.GetAgentVersion(), peerAddr(ctx))
	if errors.Is(err, autoenroll.ErrRejected) {
		return nil, status.Error(codes.PermissionDenied, "enrollment rejected")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	return &inventoryv1.EnrollResponse{AgentId: agentID, AgentCredential: credential}, nil
}

// Length bounds of automatic enrollment request fields.
const (
	maxHostnameLen = 253
	maxIDLen       = 128
)

// peerAddr is the caller's IP address (invalid when unknown).
func peerAddr(ctx context.Context) netip.Addr {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return netip.Addr{}
	}
	if ap, err := netip.ParseAddrPort(p.Addr.String()); err == nil {
		return ap.Addr().Unmap()
	}
	if tcp, ok := p.Addr.(*net.TCPAddr); ok {
		if a, ok := netip.AddrFromSlice(tcp.IP); ok {
			return a.Unmap()
		}
	}
	return netip.Addr{}
}
