package ingest

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Metadata keys carrying the per-agent credential. They mirror the keys the
// agent-side sender attaches to outgoing calls.
const (
	metaAgentIDKey    = "x-agent-id"
	metaCredentialKey = "x-agent-credential"
)

// agentCtxKey is the (unexported, typed) context key under which the verified
// agent is stored. A private key type prevents collisions with any other
// package's context values.
type agentCtxKey struct{}

// withAgent returns ctx carrying the verified agent.
func withAgent(ctx context.Context, a store.Agent) context.Context {
	return context.WithValue(ctx, agentCtxKey{}, a)
}

// AgentFromContext returns the verified agent placed on the context by the
// ingest auth interceptor, and whether one is present.
func AgentFromContext(ctx context.Context) (store.Agent, bool) {
	a, ok := ctx.Value(agentCtxKey{}).(store.Agent)
	return a, ok
}

// isUnauthenticatedMethod reports whether a full gRPC method is served without
// a per-agent credential. Only Enroll is open (the enrollment token in its
// request body is the credential).
func isUnauthenticatedMethod(fullMethod string) bool {
	return fullMethod == inventoryv1.IngestService_Enroll_FullMethodName
}

// authenticate reads the per-agent credential from call metadata, verifies it
// against the enroll service, and returns a context carrying the resolved
// agent. Missing, malformed, unknown, revoked, or mismatched credentials all
// collapse to codes.Unauthenticated (no enumeration oracle). The credential is
// never logged.
func (s *Server) authenticate(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	agentID := firstMeta(md, metaAgentIDKey)
	credential := firstMeta(md, metaCredentialKey)
	if agentID == "" || credential == "" {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	agent, err := s.enroll.Verify(ctx, agentID, credential)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "agent credential rejected")
	}
	return withAgent(ctx, agent), nil
}

// firstMeta returns the first metadata value for a key, or "".
func firstMeta(md metadata.MD, key string) string {
	if vals := md.Get(key); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// UnaryInterceptor authenticates unary ingest calls. Enroll passes through
// unauthenticated; every other method requires a valid per-agent credential and
// runs with the verified agent on its context.
func (s *Server) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isUnauthenticatedMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		authed, err := s.authenticate(ctx)
		if err != nil {
			return nil, err
		}
		return handler(authed, req)
	}
}

// StreamInterceptor authenticates streaming ingest calls (StreamCommands). The
// verified agent is placed on a wrapped ServerStream context.
func (s *Server) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isUnauthenticatedMethod(info.FullMethod) {
			return handler(srv, ss)
		}
		authed, err := s.authenticate(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &authedStream{ServerStream: ss, ctx: authed})
	}
}

// authedStream overrides Context so downstream handlers observe the verified
// agent placed on the context by the stream interceptor.
type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }

// Interceptors returns the unary and stream interceptors for the ingest
// listener.
func (s *Server) Interceptors() (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	return s.UnaryInterceptor(), s.StreamInterceptor()
}

// ServerOptions returns the grpc.ServerOptions the app should use to build the
// ingest listener: the auth interceptors plus the inbound message-size ceiling.
func (s *Server) ServerOptions() []grpc.ServerOption {
	unary, stream := s.Interceptors()
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unary),
		grpc.ChainStreamInterceptor(stream),
		grpc.MaxRecvMsgSize(int(s.maxBytes)),
	}
}

// RegisterServer registers the ingest service on a gRPC server. The server must
// have been built with s.ServerOptions() (or NewGRPCServer) so the auth
// interceptors are in force.
func RegisterServer(gs *grpc.Server, s *Server) {
	inventoryv1.RegisterIngestServiceServer(gs, s)
}

// NewGRPCServer builds a ready-to-serve ingest grpc.Server: a grpc.Server with
// the auth interceptors and message-size ceiling applied, with the ingest
// service registered. extra carries transport options — in particular
// CertLoader.TransportOption() for TLS; without it the server is plaintext
// (development only). The app binds it to the off-mesh listener.
func NewGRPCServer(s *Server, extra ...grpc.ServerOption) *grpc.Server {
	gs := grpc.NewServer(append(s.ServerOptions(), extra...)...)
	RegisterServer(gs, s)
	return gs
}
