package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra/v4/catalogue"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/pkg/inventorymanifest"
)

var placeholderRE = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

// tangra-module.yaml is a valid descriptor for this module, and its routes
// cover what the module registers with the gateway.
func TestCatalogueDescriptor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tangra-module.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalogue.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	m, err := inventorymanifest.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if d.Module != m.Module || d.Module != inventorymanifest.Module {
		t.Fatalf("descriptor module %q, manifest %q", d.Module, m.Module)
	}
	have := strings.Join(d.Routes.Prefixes, ",")
	for _, p := range append(m.Prefixes, "/m/"+m.Module) {
		if !strings.Contains(","+have+",", ","+p+",") {
			t.Fatalf("routes.prefixes %v miss %s", d.Routes.Prefixes, p)
		}
	}
	for _, n := range m.Nav {
		if n.Path != "/"+m.Module && !strings.HasPrefix(n.Path, "/"+m.Module+"/") {
			t.Fatalf("nav %s outside /%s", n.Path, m.Module)
		}
	}
}

// The join bundle's templates, rendered with values of the shape the gateway
// fills in, are a valid production configuration and policy.
func TestJoinBundleRendersToValidProductionConfig(t *testing.T) {
	values := map[string]string{
		"TRUST_DOMAIN": "infra.example.org", "GATEWAY_ISSUER": "https://portal.example.org:8443",
		"LCM_ENROLL_URL": "https://portal.example.org:8443/api/lcm/v1/enroll", "LCM_GRPC": "portal.example.org:9945",
		"GATEWAY_GRPC": "portal.example.org:9643", "AUTH_GRPC": "portal.example.org:9543", "MESH_TENANT_ID": "00000000-0000-0000-0000-000000000001",
		"GEN_PASSWORD_1": strings.Repeat("a", 48), "GEN_PASSWORD_2": strings.Repeat("b", 48), "GEN_PASSWORD_3": strings.Repeat("c", 48),
	}
	dir := t.TempDir()
	for _, name := range []string{"config.yaml", "policy.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "bundle", name))
		if err != nil {
			t.Fatal(err)
		}
		var missing []string
		rendered := placeholderRE.ReplaceAllStringFunc(string(raw), func(m string) string {
			k := placeholderRE.FindStringSubmatch(m)[1]
			v, ok := values[k]
			if !ok {
				missing = append(missing, k)
			}
			return v
		})
		if len(missing) > 0 {
			t.Fatalf("%s uses placeholders the gateway does not fill: %v", name, missing)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(rendered), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rendered config invalid: %v", err)
	}
	if !cfg.IsProduction() || cfg.Gateway.Issuer != "https://portal.example.org:8443" || cfg.Ingest.Insecure || strings.Contains(strings.Join(cfg.Warnings(), ";"), "development only") {
		t.Fatalf("%+v %+v %v", cfg.Gateway, cfg.Ingest, cfg.Warnings())
	}
	pol, _ := os.ReadFile(filepath.Join(dir, "policy.yaml"))
	if !strings.Contains(string(pol), "spiffe://infra.example.org/svc/gateway") {
		t.Fatal(string(pol))
	}
}
