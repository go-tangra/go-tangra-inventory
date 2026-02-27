package server

import (
	"context"
	"crypto/subtle"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ApiSecretMiddleware returns a Kratos middleware that validates the X-API-Key
// HTTP header. An empty secret disables authentication (pass-through).
// Swagger UI is unaffected because it's registered via HandlePrefix which
// bypasses the Kratos middleware chain.
func ApiSecretMiddleware(secret string) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if secret == "" {
				return handler(ctx, req)
			}

			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return nil, status.Error(codes.Internal, "no transport in context")
			}

			key := tr.RequestHeader().Get("X-API-Key")
			if key == "" {
				return nil, status.Error(codes.Unauthenticated, "missing X-API-Key header")
			}

			if subtle.ConstantTimeCompare([]byte(key), []byte(secret)) != 1 {
				return nil, status.Error(codes.Unauthenticated, "invalid X-API-Key")
			}

			return handler(ctx, req)
		}
	}
}
