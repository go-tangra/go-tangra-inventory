package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
)

// OpenAPI operation extensions.
const (
	BodyLimitExtension  = "x-freya-max-body-bytes"
	PermissionExtension = "x-freya-permission"
	PublicExtension     = "x-freya-public"
	TimeoutExtension    = "x-freya-timeout-seconds"
)

// Gateway headers.
const HeaderRequestID = "X-Request-Id"

// Verifier authenticates platform tokens (implemented by authclient.Verifier).
type Verifier interface {
	Verify(ctx context.Context, token string) (authclient.Identity, error)
}

// Identity is the caller as established from the platform token.
type Identity = authclient.Identity

// Caller returns the verified identity or ErrUnauthenticated.
func Caller(r *http.Request) (Identity, error) {
	id, ok := authclient.FromContext(r.Context())
	if !ok || id.UserID == "" || id.TenantID == "" {
		return Identity{}, ErrUnauthenticated
	}
	return id, nil
}

// RequestID returns the gateway correlation id ("" when absent).
func RequestID(r *http.Request) string { return r.Header.Get(HeaderRequestID) }

// secure sets the response headers every route shares.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// authenticate requires a platform token on every non-public route.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.protected[r.Pattern] {
			next.ServeHTTP(w, r)
			return
		}
		if s.verifier == nil {
			WriteError(w, ErrUnauthenticated.Status, ErrUnauthenticated.Reason)
			return
		}
		id, err := s.verifier.Verify(r.Context(), authclient.BearerToken(r.Header.Get("Authorization")))
		if err != nil || id.UserID == "" || id.TenantID == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="freya"`)
			WriteError(w, ErrUnauthenticated.Status, ErrUnauthenticated.Reason)
			return
		}
		next.ServeHTTP(w, r.WithContext(authclient.WithIdentity(r.Context(), id)))
	})
}

// validate checks the request against the OpenAPI document for declared routes.
func (s *Server) validate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, err := s.router.FindRoute(r)
		if err != nil {
			if errors.Is(err, routers.ErrPathNotFound) {
				next.ServeHTTP(w, r)
				return
			}
			WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		limit, binary := int64(MaxBodyBytes), false
		if route.Operation != nil {
			if v, ok := route.Operation.Extensions[BodyLimitExtension]; ok {
				if n, ok := extensionInt(v); ok && n > 0 {
					limit, binary = n, true
				}
			}
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		body := r.Body
		if binary {
			r.Body = http.NoBody
		}
		in := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, ExcludeRequestBody: binary}}
		err = openapi3filter.ValidateRequest(r.Context(), in)
		if binary {
			r.Body = body
		}
		if err != nil {
			var mbe *http.MaxBytesError
			var pe *openapi3filter.ParseError
			switch {
			case errors.As(err, &mbe) || strings.Contains(err.Error(), "request body too large"):
				WriteError(w, ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason)
			case errors.As(err, &pe):
				WriteError(w, ErrMalformed.Status, ErrMalformed.Reason)
			default:
				WriteError(w, ErrValidation.Status, ErrValidation.Reason)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extensionInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

func extensionBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
