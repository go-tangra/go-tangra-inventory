package inventorymanifest

import (
	"slices"
	"testing"
)

// Feature 019: the module ships administrator, editor and viewer roles made
// only of its own permissions.
func TestRoles(t *testing.T) {
	want := map[string]struct {
		name  string
		perms []string
	}{
		"administrator": {"Inventory administrator", PermissionRefs()},
		"editor":        {"Inventory editor", []string{"inventory:read", "inventory:write", "snapshots:read"}},
		"viewer":        {"Inventory viewer", []string{"inventory:read", "snapshots:read", "stats:read"}},
	}
	if len(Roles) != len(want) {
		t.Fatalf("%d roles, want %d", len(Roles), len(want))
	}
	own := PermissionRefs()
	for _, r := range Roles {
		w, ok := want[r.Slug]
		if !ok {
			t.Fatalf("unexpected role %q", r.Slug)
		}
		if r.DisplayName != w.name || r.Description == "" {
			t.Errorf("%s: display name %q, description %q", r.Slug, r.DisplayName, r.Description)
		}
		if !slices.Equal(slices.Sorted(slices.Values(r.Permissions)), slices.Sorted(slices.Values(w.perms))) {
			t.Errorf("%s: permissions %v, want %v", r.Slug, r.Permissions, w.perms)
		}
		for _, p := range r.Permissions {
			if !slices.Contains(own, p) {
				t.Errorf("%s: %q is not an inventory permission", r.Slug, p)
			}
		}
	}
}

func TestRegistration(t *testing.T) {
	r := Registration()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Module != Module || r.DisplayName != DisplayName || len(r.Permissions) != len(Permissions) || len(r.Roles) != len(Roles) {
		t.Fatalf("%+v", r)
	}
	for _, slug := range []string{"owner", "admin", "member", "auditor", "operator"} {
		if !slices.Equal(r.BuiltinGrants[slug], Grants[slug]) {
			t.Errorf("grant %s: %v, want %v", slug, r.BuiltinGrants[slug], Grants[slug])
		}
	}
	req := r.Request()
	if req.GetModule() != "inventory" || req.GetModuleDisplayName() != "Inventory" || !req.GetDeclaresRoles() || len(req.GetRoles()) != 3 || len(req.GetBuiltinGrants()) != 5 {
		t.Fatalf("%v", req)
	}
}
