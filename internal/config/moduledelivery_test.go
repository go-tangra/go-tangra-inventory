package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModuleDeliveryDefaultsAndBounds(t *testing.T) {
	c := valid()
	d := c.ModuleDelivery
	if d.Enabled || len(d.Sources) != 1 || d.Sources[0] != "gateway" || d.SourceService != "gateway" || d.ReportTimeoutMinutes != 30 ||
		d.MaxConcurrentFetches != 20 || d.SourceTimeoutSeconds != 30 || d.AllowPlaintextIngest {
		t.Fatalf("defaults = %+v (server delivery must be off by default)", d)
	}
	if d.ReportTimeout() != 30*time.Minute || d.SourceTimeout() != 30*time.Second {
		t.Fatalf("durations = %s %s", d.ReportTimeout(), d.SourceTimeout())
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*ModuleDelivery){
		"report low":         func(d *ModuleDelivery) { d.ReportTimeoutMinutes = 4 },
		"report high":        func(d *ModuleDelivery) { d.ReportTimeoutMinutes = 121 },
		"fetches low":        func(d *ModuleDelivery) { d.MaxConcurrentFetches = 0 },
		"fetches high":       func(d *ModuleDelivery) { d.MaxConcurrentFetches = 501 },
		"source timeout low": func(d *ModuleDelivery) { d.SourceTimeoutSeconds = 1 },
		"source timeout hi":  func(d *ModuleDelivery) { d.SourceTimeoutSeconds = 121 },
		"source empty":       func(d *ModuleDelivery) { d.SourceService = "" },
		"source bad":         func(d *ModuleDelivery) { d.SourceService = "gateway:9000" },
		"sources entry":      func(d *ModuleDelivery) { d.Sources = []string{"svc/gateway"} },
		"sources too many": func(d *ModuleDelivery) {
			d.Sources = nil
			for i := 0; i < 33; i++ {
				d.Sources = append(d.Sources, "s")
			}
		},
	} {
		c := valid()
		mut(&c.ModuleDelivery)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "module_delivery") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestModuleDeliveryPlaintextIngest(t *testing.T) {
	c := valid()
	c.ModuleDelivery.AllowPlaintextIngest = true
	if err := c.Validate(); err != nil {
		t.Fatalf("dev: %v", err)
	}
	if w := strings.Join(c.Warnings(), "\n"); !strings.Contains(w, "module_delivery.allow_plaintext_ingest") {
		t.Fatalf("no startup warning: %s", w)
	}
	c.Env = "production"
	c.DB.DSN = "postgres://h/db?sslmode=verify-full"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "module_delivery.allow_plaintext_ingest") {
		t.Fatalf("production: %v", err)
	}
}

func TestModuleDeliveryLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	raw := "module_delivery:\n  enabled: true\n  sources: [gateway]\n  source_service: portal-gw\n  report_timeout_minutes: 10\n" +
		"  max_concurrent_fetches: 3\n  source_timeout_seconds: 5\n  allow_plaintext_ingest: false\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	d := c.ModuleDelivery
	if err != nil || !d.Enabled || d.SourceService != "portal-gw" || d.ReportTimeoutMinutes != 10 || d.MaxConcurrentFetches != 3 ||
		d.SourceTimeoutSeconds != 5 {
		t.Fatalf("load = %+v %v", d, err)
	}
}

func agentWithModules() AgentConfig {
	a := DefaultAgent()
	a.IngestEndpoint, a.TokenFile = "x:1", "/t"
	return a
}

func TestAgentModulesDefaults(t *testing.T) {
	m := DefaultAgent().Modules
	if !m.Enabled || m.Directory != "/opt/tangra/modules" || m.Owner != "root" || m.Group != "root" || m.DeployHook != "" ||
		m.HookTimeoutSeconds != 600 || m.AllowInsecureTransport || m.HookTimeout() != 10*time.Minute {
		t.Fatalf("defaults = %+v", m)
	}
	if err := agentWithModules().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentModulesRules(t *testing.T) {
	for name, mut := range map[string]func(*AgentModules){
		"relative":       func(m *AgentModules) { m.Directory = "opt/modules" },
		"unclean":        func(m *AgentModules) { m.Directory = "/opt/../etc" },
		"trailing slash": func(m *AgentModules) { m.Directory = "/opt/tangra/" },
		"root":           func(m *AgentModules) { m.Directory = "/" },
		"empty":          func(m *AgentModules) { m.Directory = "" },
		"proc":           func(m *AgentModules) { m.Directory = "/proc/x" },
		"home":           func(m *AgentModules) { m.Directory = "/home/u/modules" },
		"root home":      func(m *AgentModules) { m.Directory = "/root" },
		"tmp":            func(m *AgentModules) { m.Directory = "/tmp/modules" },
		"owner":          func(m *AgentModules) { m.Owner = "Root!" },
		"group":          func(m *AgentModules) { m.Group = "99999999999" },
		"hook relative":  func(m *AgentModules) { m.DeployHook = "up.sh" },
		"hook unclean":   func(m *AgentModules) { m.DeployHook = "/usr/local/sbin/../up.sh" },
		"hook root":      func(m *AgentModules) { m.DeployHook = "/" },
		"timeout low":    func(m *AgentModules) { m.HookTimeoutSeconds = 29 },
		"timeout high":   func(m *AgentModules) { m.HookTimeoutSeconds = 1801 },
	} {
		a := agentWithModules()
		mut(&a.Modules)
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "modules.") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Disabled is still validated; a numeric owner and a hook are fine.
	a := agentWithModules()
	a.Modules.Enabled, a.Modules.Owner, a.Modules.Group, a.Modules.DeployHook = false, "1000", "docker", "/usr/local/sbin/module-up"
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestAgentModulesAnnounce: module.v1 only when enabled, on Linux and over
// TLS unless the plaintext opt-out is set locally (warned about).
func TestAgentModulesAnnounce(t *testing.T) {
	m := DefaultAgent().Modules
	if !m.Announce("linux", false) || m.Announce("windows", false) || m.Announce("linux", true) {
		t.Fatal("announce")
	}
	m.AllowInsecureTransport = true
	if !m.Announce("linux", true) {
		t.Fatal("opt-out")
	}
	m.Enabled = false
	if m.Announce("linux", false) {
		t.Fatal("disabled announced")
	}
	a := agentWithModules()
	a.Insecure, a.Modules.AllowInsecureTransport = true, true
	if w := strings.Join(a.Warnings(), "\n"); !strings.Contains(w, "modules.allow_insecure_transport") {
		t.Fatalf("warnings = %s", w)
	}
}

func TestAgentModulesLoad(t *testing.T) {
	cfg, err := decodeAgent([]byte("modules:\n  enabled: false\n  directory: /srv/modules\n  owner: \"1000\"\n  group: docker\n" +
		"  deploy_hook: /usr/local/sbin/module-up\n  hook_timeout_seconds: 120\n  allow_insecure_transport: true\n"))
	m := cfg.Modules
	if err != nil || m.Enabled || m.Directory != "/srv/modules" || m.Owner != "1000" || m.Group != "docker" ||
		m.DeployHook != "/usr/local/sbin/module-up" || m.HookTimeoutSeconds != 120 || !m.AllowInsecureTransport {
		t.Fatalf("load = %+v %v", m, err)
	}
	if _, err := decodeAgent([]byte("modules:\n  path: /x\n")); err == nil {
		t.Fatal("unknown key accepted")
	}
}
