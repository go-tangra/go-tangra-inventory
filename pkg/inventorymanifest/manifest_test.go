package inventorymanifest

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/go-freya/freya/services/inventory/api/openapi"
)

// TestOpenAPIParsesAndValidates confirms the embedded document loads and passes
// full validation the same way the HTTP server loads it (uuid format registered,
// examples validation disabled).
func TestOpenAPIParsesAndValidates(t *testing.T) {
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapi.Inventory)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(loader.Context, openapi3.DisableExamplesValidation()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if doc.Paths == nil || len(doc.Paths.Map()) == 0 {
		t.Fatal("no paths")
	}
}

// TestManifestBuilds confirms every route derives a known permission (or is
// public) and that the manifest carries the expected identity, nav and prefixes.
func TestManifestBuilds(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Module != "inventory" {
		t.Fatalf("module = %q", m.Module)
	}
	if len(m.Routes) == 0 {
		t.Fatal("no routes")
	}
	known := map[string]bool{}
	for _, p := range PermissionRefs() {
		known[p] = true
	}
	for _, r := range m.Routes {
		if r.Public {
			continue
		}
		if !known[r.Permission] {
			t.Fatalf("route %s %s has unknown permission %q", r.Method, r.Path, r.Permission)
		}
	}
	if len(m.Nav) != 3 {
		t.Fatalf("nav entries = %d", len(m.Nav))
	}
	if len(m.Permissions) != 8 {
		t.Fatalf("permissions = %d", len(m.Permissions))
	}
}
