package docs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-tangra/go-tangra/v4/authn"
	"github.com/go-tangra/go-tangra/v4/helpdocs"
	"github.com/go-tangra/go-tangra/v4/identity"

	"github.com/go-tangra/go-tangra-inventory/v4/docs"
	"github.com/go-tangra/go-tangra-inventory/v4/pkg/inventorymanifest"
)

func load(t *testing.T) *helpdocs.Bundle {
	t.Helper()
	b, err := helpdocs.Load(docs.Help(), inventorymanifest.Module)
	if err != nil {
		t.Fatalf("embedded help pages: %v", err)
	}
	return b
}

// The embedded tree loads without problems, has a start page and an operator
// page, and gates pages only on permissions inventory registers.
func TestEmbeddedHelpLoads(t *testing.T) {
	b := load(t)
	if b.Module != inventorymanifest.Module || b.Digest == "" || len(b.Pages) < 5 {
		t.Fatalf("bundle: module %q, digest %q, %d pages", b.Module, b.Digest, len(b.Pages))
	}
	if b.Digest != helpdocs.ComputeDigest(b) {
		t.Fatal("digest does not match the content")
	}
	perms := inventorymanifest.PermissionRefs()
	var index, operator bool
	for _, p := range b.Pages {
		index = index || p.Slug == "index"
		operator = operator || p.Audience == helpdocs.AudienceOperator
		if p.Requires != "" && !slices.Contains(perms, p.Requires) {
			t.Errorf("page %s requires %q, not an inventory permission (%v)", p.Slug, p.Requires, perms)
		}
	}
	if !index || !operator {
		t.Fatalf("index page %v, operator page %v", index, operator)
	}
}

func as(service string, r *http.Request) *http.Request {
	if service == "" {
		return r
	}
	return r.WithContext(authn.WithPeer(r.Context(), authn.PeerIdentity{ID: identity.ForService("example.org", service), ServiceName: service}))
}

// The handler wired in app.Build serves the bundle to the configured gateway
// service only.
func TestHelpHandlerServesGatewayOnly(t *testing.T) {
	b := load(t)
	h := helpdocs.NewHandler(b, helpdocs.AllowServices("gateway"))
	for _, c := range []struct {
		caller string
		status int
	}{
		{"gateway", http.StatusOK},
		{"deployer", http.StatusForbidden},
		{"", http.StatusForbidden},
	} {
		r := as(c.caller, httptest.NewRequestWithContext(context.Background(), http.MethodGet, helpdocs.BundlePath, http.NoBody))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatalf("caller %q: status %d, want %d", c.caller, w.Code, c.status)
		}
		if c.status != http.StatusOK {
			continue
		}
		got, err := helpdocs.Decode(w.Body.Bytes())
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Digest != b.Digest || len(got.Pages) != len(b.Pages) || w.Header().Get("ETag") != `"`+b.Digest+`"` {
			t.Fatalf("served digest %s (%d pages), etag %s; want %s (%d pages)", got.Digest, len(got.Pages), w.Header().Get("ETag"), b.Digest, len(b.Pages))
		}
	}
}
