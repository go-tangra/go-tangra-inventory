package server

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// allowedClientSecretUnaryMethods lists unary RPCs that client-secret callers may invoke.
var allowedClientSecretUnaryMethods = map[string]bool{
	"/SubmitInventory": true,
}

// allowedClientSecretStreamMethods lists streaming RPCs that client-secret callers may invoke.
var allowedClientSecretStreamMethods = map[string]bool{
	"/StreamCommands": true,
}

// ClientSecretInterceptor returns a gRPC unary server interceptor that
// validates the x-client-secret metadata header. When the secret is non-empty,
// only the SubmitInventory RPC is allowed for client-secret authenticated callers.
// An empty secret disables authentication (pass-through).
func ClientSecretInterceptor(secret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if secret == "" {
			return handler(ctx, req)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		vals := md.Get("x-client-secret")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing x-client-secret")
		}

		if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(secret)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "invalid x-client-secret")
		}

		// Client-secret callers may only call allowed unary methods.
		allowed := false
		for suffix := range allowedClientSecretUnaryMethods {
			if strings.HasSuffix(info.FullMethod, suffix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, status.Error(codes.PermissionDenied, "client-secret not permitted for this method")
		}

		return handler(ctx, req)
	}
}

// ClientSecretStreamInterceptor returns a gRPC stream server interceptor that
// validates the x-client-secret metadata header. When the secret is non-empty,
// only the StreamCommands RPC is allowed for client-secret authenticated callers.
// An empty secret disables authentication (pass-through).
func ClientSecretStreamInterceptor(secret string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if secret == "" {
			return handler(srv, ss)
		}

		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing metadata")
		}

		vals := md.Get("x-client-secret")
		if len(vals) == 0 {
			return status.Error(codes.Unauthenticated, "missing x-client-secret")
		}

		if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(secret)) != 1 {
			return status.Error(codes.Unauthenticated, "invalid x-client-secret")
		}

		// Client-secret callers may only call allowed streaming methods.
		allowed := false
		for suffix := range allowedClientSecretStreamMethods {
			if strings.HasSuffix(info.FullMethod, suffix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return status.Error(codes.PermissionDenied, "client-secret not permitted for this method")
		}

		return handler(srv, ss)
	}
}
