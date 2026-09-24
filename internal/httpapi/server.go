// Package httpapi is the gateway-facing HTTP API of the lcm module: every
// declared OpenAPI route is mounted (501 until a story implements it),
// requests are validated against the document, and the platform token
// forwarded by the gateway yields the caller on every non-public route.
package httpapi

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/go-tangra/go-tangra-inventory/v4/api/openapi"
	"github.com/go-tangra/go-tangra/v4/transport"
	"github.com/go-tangra/go-tangra/v4/transport/edge"
)

// RemotePrefix is where the federated remote is served to the gateway
// (relayed under /m/lcm/).
const RemotePrefix = "/ui"

// Route is a declared method/path pair.
type Route struct{ Method, Path string }

func (r Route) String() string { return r.Method + " " + r.Path }

// Option configures the server.
type Option func(*Server)

// WithVerifier installs the platform token verifier.
func WithVerifier(v Verifier) Option { return func(s *Server) { s.verifier = v } }

// WithRemote serves the federated remote build under RemotePrefix.
func WithRemote(dist fs.FS) Option { return func(s *Server) { s.remote = dist } }

// Server mounts the API.
type Server struct {
	rt        transport.Runtime
	edge      *edge.Server
	doc       *openapi3.T
	router    routers.Router
	mux       *http.ServeMux
	mu        sync.RWMutex
	handlers  map[Route]http.Handler
	declared  []Route
	public    map[Route]bool
	protected map[string]bool
	verifier  Verifier
	remote    fs.FS
	handler   http.Handler
}

var formatsOnce sync.Once

// LoadDocument parses and validates the embedded OpenAPI document. The uuid
// format (RFC 9562, so UUIDv7 ids validate) is registered once.
func LoadDocument() (*openapi3.T, error) {
	formatsOnce.Do(func() {
		openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	})
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapi.Inventory)
	if err != nil {
		return nil, fmt.Errorf("httpapi: openapi: %w", err)
	}
	if err := doc.Validate(loader.Context, openapi3.DisableExamplesValidation()); err != nil {
		return nil, fmt.Errorf("httpapi: openapi: %w", err)
	}
	return doc, nil
}

// DeclaredRoutes lists every operation in the document, sorted.
func DeclaredRoutes(doc *openapi3.T) []Route {
	var out []Route
	for p, item := range doc.Paths.Map() {
		for m := range item.Operations() {
			out = append(out, Route{Method: strings.ToUpper(m), Path: p})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// PublicRoutes lists operations flagged x-freya-public.
func PublicRoutes(doc *openapi3.T) map[Route]bool {
	out := map[Route]bool{}
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			if extensionBool(op.Extensions[PublicExtension]) {
				out[Route{strings.ToUpper(m), p}] = true
			}
		}
	}
	return out
}

// NewHandler builds the API without binding a listener.
func NewHandler(rt transport.Runtime, opts ...Option) (*Server, error) {
	doc, err := LoadDocument()
	if err != nil {
		return nil, err
	}
	servers := doc.Servers
	doc.Servers = nil
	router, err := gorillamux.NewRouter(doc)
	doc.Servers = servers
	if err != nil {
		return nil, fmt.Errorf("httpapi: router: %w", err)
	}
	s := &Server{rt: rt, doc: doc, router: router, mux: http.NewServeMux(), handlers: map[Route]http.Handler{}}
	for _, o := range opts {
		o(s)
	}
	s.declared = DeclaredRoutes(doc)
	s.public = PublicRoutes(doc)
	s.protected = map[string]bool{}
	for _, rt := range s.declared {
		rt := rt
		if !s.public[rt] {
			s.protected[rt.Method+" "+rt.Path] = true
		}
		s.mux.Handle(rt.Method+" "+rt.Path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.mu.RLock()
			h := s.handlers[rt]
			s.mu.RUnlock()
			if h == nil {
				WriteError(w, ErrNotImplemented.Status, ErrNotImplemented.Reason)
				return
			}
			h.ServeHTTP(w, r)
		}))
	}
	if s.remote != nil {
		s.mux.Handle("GET "+RemotePrefix+"/", http.StripPrefix(RemotePrefix, remoteHandler(s.remote)))
	}
	s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, ErrNotFound.Reason)
	}))
	inner := s.authenticate(s.mux)
	s.handler = secure(s.validate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := s.mux.Handler(r)
		r.Pattern = pattern
		inner.ServeHTTP(w, r)
	})))
	return s, nil
}

// New builds the API and binds it to an edge listener.
func New(rt transport.Runtime, cfg edge.Config, opts ...Option) (*Server, error) {
	s, err := NewHandler(rt, opts...)
	if err != nil {
		return nil, err
	}
	e, err := edge.NewServer(rt, cfg)
	if err != nil {
		return nil, err
	}
	e.HandlePrefix("/", s.handler)
	s.edge = e
	return s, nil
}

// Handle installs h for a declared route; undeclared routes are refused so
// the contract and the implementation cannot drift.
func (s *Server) Handle(method, path string, h http.Handler) error {
	rt := Route{Method: strings.ToUpper(method), Path: path}
	if !s.isDeclared(rt) {
		return fmt.Errorf("httpapi: route %s is not declared in the OpenAPI document", rt)
	}
	s.mu.Lock()
	s.handlers[rt] = h
	s.mu.Unlock()
	return nil
}

// HandleFunc is Handle for a function.
func (s *Server) HandleFunc(method, path string, h func(http.ResponseWriter, *http.Request)) error {
	return s.Handle(method, path, http.HandlerFunc(h))
}

// MustHandle panics on an undeclared route (a wiring error).
func (s *Server) MustHandle(method, path string, h func(http.ResponseWriter, *http.Request)) {
	if err := s.HandleFunc(method, path, h); err != nil {
		panic(err)
	}
}

func (s *Server) isDeclared(rt Route) bool {
	for _, d := range s.declared {
		if d == rt {
			return true
		}
	}
	return false
}

// Declared lists routes from the document.
func (s *Server) Declared() []Route { return append([]Route(nil), s.declared...) }

// Implemented lists the routes with a handler installed, sorted.
func (s *Server) Implemented() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Route
	for rt := range s.handlers {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// Missing lists declared routes without a handler.
func (s *Server) Missing() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Route
	for _, rt := range s.declared {
		if s.handlers[rt] == nil {
			out = append(out, rt)
		}
	}
	return out
}

// IsPublic reports whether the route skips authentication.
func (s *Server) IsPublic(method, path string) bool { return s.public[Route{method, path}] }

// Document returns the parsed OpenAPI document.
func (s *Server) Document() *openapi3.T { return s.doc }

// Handler returns the full chain (headers -> validation -> token -> routes).
func (s *Server) Handler() http.Handler { return s.handler }

// Edge returns the bound listener (nil for NewHandler).
func (s *Server) Edge() *edge.Server { return s.edge }

// Start delegates to the edge listener.
func (s *Server) Start(ctx context.Context) error { return s.edge.Start(ctx) }

// Stop stops the listener.
func (s *Server) Stop(ctx context.Context) error { return s.edge.Stop(ctx) }

// remoteHandler serves the federated remote build: immutable hashed assets,
// never-cached manifest, no directory listings.
func remoteHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || strings.HasSuffix(p, "/") {
			WriteError(w, http.StatusNotFound, ErrNotFound.Reason)
			return
		}
		if _, err := fs.Stat(dist, p); err != nil {
			WriteError(w, http.StatusNotFound, ErrNotFound.Reason)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}
