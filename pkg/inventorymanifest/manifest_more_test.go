package inventorymanifest

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// docWith builds a one-operation OpenAPI doc whose GET /x carries the given
// extensions, for exercising Routes' validation branches.
func docWith(ext map[string]any) *openapi3.T {
	op := &openapi3.Operation{Extensions: ext}
	item := &openapi3.PathItem{Get: op}
	paths := openapi3.NewPaths()
	paths.Set("/x", item)
	return &openapi3.T{Paths: paths}
}

func TestRoutesValidationBranches(t *testing.T) {
	// public route: no permission needed.
	routes, err := Routes(docWith(map[string]any{PublicExtension: true}))
	if err != nil || len(routes) != 1 || !routes[0].Public {
		t.Fatalf("public route: %v %+v", err, routes)
	}

	// valid permission + body limit + timeout.
	routes, err = Routes(docWith(map[string]any{
		PermissionExtension: "inventory:read",
		BodyLimitExtension:  float64(2048),
		TimeoutExtension:    float64(30),
	}))
	if err != nil || len(routes) != 1 {
		t.Fatalf("valid route: %v", err)
	}
	if routes[0].Permission != "inventory:read" || routes[0].MaxBodyBytes != 2048 || routes[0].Timeout.Seconds() != 30 {
		t.Fatalf("route fields: %+v", routes[0])
	}

	// missing permission.
	if _, err := Routes(docWith(map[string]any{})); err == nil || !strings.Contains(err.Error(), "no permission") {
		t.Fatalf("missing permission: %v", err)
	}
	// undeclared permission.
	if _, err := Routes(docWith(map[string]any{PermissionExtension: "made:up"})); err == nil || !strings.Contains(err.Error(), "undeclared permission") {
		t.Fatalf("undeclared permission: %v", err)
	}
	// bad body limit (non-positive).
	if _, err := Routes(docWith(map[string]any{PermissionExtension: "inventory:read", BodyLimitExtension: float64(0)})); err == nil || !strings.Contains(err.Error(), "bad body limit") {
		t.Fatalf("bad body limit: %v", err)
	}
	// bad body limit (wrong type).
	if _, err := Routes(docWith(map[string]any{PermissionExtension: "inventory:read", BodyLimitExtension: "big"})); err == nil {
		t.Fatal("bad body limit type should error")
	}
	// bad timeout (out of range).
	if _, err := Routes(docWith(map[string]any{PermissionExtension: "inventory:read", TimeoutExtension: float64(9999)})); err == nil || !strings.Contains(err.Error(), "bad timeout") {
		t.Fatalf("bad timeout: %v", err)
	}
}

func TestPermissionRefs(t *testing.T) {
	refs := PermissionRefs()
	if len(refs) != len(Permissions) {
		t.Fatalf("refs = %d", len(refs))
	}
	if refs[0] != "inventory:read" {
		t.Fatalf("first ref = %q", refs[0])
	}
}
