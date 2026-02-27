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

// AuthInterceptor returns a gRPC unary server interceptor that validates
// either x-client-secret or x-api-secret metadata headers.
//
// When both secrets are empty, authentication is disabled (pass-through).
// x-client-secret callers may only invoke SubmitInventory (agent write path).
// x-api-secret callers may invoke any RPC (service-to-service read path).
func AuthInterceptor(clientSecret, apiSecret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if clientSecret == "" && apiSecret == "" {
			return handler(ctx, req)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		// Try x-api-secret first — grants access to all RPCs.
		if apiSecret != "" {
			if vals := md.Get("x-api-secret"); len(vals) > 0 {
				if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(apiSecret)) == 1 {
					return handler(ctx, req)
				}
				return nil, status.Error(codes.Unauthenticated, "invalid x-api-secret")
			}
		}

		// Fall back to x-client-secret — restricted to agent methods only.
		if clientSecret != "" {
			if vals := md.Get("x-client-secret"); len(vals) > 0 {
				if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(clientSecret)) != 1 {
					return nil, status.Error(codes.Unauthenticated, "invalid x-client-secret")
				}

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

		return nil, status.Error(codes.Unauthenticated, "missing x-api-secret or x-client-secret")
	}
}

// AuthStreamInterceptor returns a gRPC stream server interceptor that validates
// either x-client-secret or x-api-secret metadata headers.
//
// x-client-secret callers may only invoke StreamCommands (agent path).
// x-api-secret callers may invoke any streaming RPC.
func AuthStreamInterceptor(clientSecret, apiSecret string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if clientSecret == "" && apiSecret == "" {
			return handler(srv, ss)
		}

		md, ok := metadata.FromIncomingContext(ss.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing metadata")
		}

		// Try x-api-secret first — grants access to all RPCs.
		if apiSecret != "" {
			if vals := md.Get("x-api-secret"); len(vals) > 0 {
				if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(apiSecret)) == 1 {
					return handler(srv, ss)
				}
				return status.Error(codes.Unauthenticated, "invalid x-api-secret")
			}
		}

		// Fall back to x-client-secret — restricted to agent methods only.
		if clientSecret != "" {
			if vals := md.Get("x-client-secret"); len(vals) > 0 {
				if subtle.ConstantTimeCompare([]byte(vals[0]), []byte(clientSecret)) != 1 {
					return status.Error(codes.Unauthenticated, "invalid x-client-secret")
				}

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

		return status.Error(codes.Unauthenticated, "missing x-api-secret or x-client-secret")
	}
}
