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

// Feature 023: agentupgrades:manage (automatic upgrades, version pins,
// downgrades) is held by owner, admin and the module administrator only.
func TestAgentUpgradesPermission(t *testing.T) {
	const perm = "agentupgrades:manage"
	if !slices.Contains(PermissionRefs(), perm) {
		t.Fatalf("%s not declared", perm)
	}
	for _, slug := range []string{"owner", "admin"} {
		if !slices.Contains(Grants[slug], perm) {
			t.Errorf("%s lacks %s", slug, perm)
		}
	}
	for _, slug := range []string{"operator", "member", "auditor"} {
		if slices.Contains(Grants[slug], perm) {
			t.Errorf("%s must not hold %s", slug, perm)
		}
	}
	for _, r := range Roles {
		if has := slices.Contains(r.Permissions, perm); has != (r.Slug == "administrator") {
			t.Errorf("module role %s holds %s = %v", r.Slug, perm, has)
		}
	}
	// Routine upgrades stay with agents:manage (operators keep them).
	if !slices.Contains(Grants["operator"], "agents:manage") {
		t.Error("operator lost agents:manage")
	}
	found := false
	for _, a := range Abilities {
		if slices.Equal(a.Action, []string{"manage"}) && slices.Equal(a.Subject, []string{"InventoryAgentUpgradePolicy"}) && a.Requires == perm {
			found = true
		}
	}
	if !found {
		t.Fatal("ability {manage, InventoryAgentUpgradePolicy} missing")
	}
	// The policy write routes require it; reading the policy needs agents:manage.
	doc, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	routes, err := Routes(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GET /api/inventory/v1/agents/upgrade-policy":         "agents:manage",
		"PUT /api/inventory/v1/agents/upgrade-policy":         perm,
		"POST /api/inventory/v1/agents/upgrade-policy/resume": perm,
	}
	for _, r := range routes {
		if p, ok := want[r.Method+" "+r.Path]; ok {
			if r.Permission != p {
				t.Errorf("%s %s requires %s, want %s", r.Method, r.Path, r.Permission, p)
			}
			delete(want, r.Method+" "+r.Path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("routes missing: %v", want)
	}
}
