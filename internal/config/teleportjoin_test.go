package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTeleportJoinDefaultsAndBounds(t *testing.T) {
	c := valid()
	d := c.TeleportJoin
	if d.Enabled || len(d.Sources) != 1 || d.Sources[0] != "teleport" || d.SourceService != "teleport" || d.ReportTimeoutMinutes != 30 ||
		d.MaxConcurrentFetches != 20 || d.SourceTimeoutSeconds != 10 || d.AllowPlaintextIngest {
		t.Fatalf("defaults = %+v (server relay must be off by default)", d)
	}
	if d.ReportTimeout() != 30*time.Minute || d.SourceTimeout() != 10*time.Second {
		t.Fatalf("durations = %s %s", d.ReportTimeout(), d.SourceTimeout())
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*TeleportJoin){
		"report low":         func(d *TeleportJoin) { d.ReportTimeoutMinutes = 14 },
		"report high":        func(d *TeleportJoin) { d.ReportTimeoutMinutes = 181 },
		"fetches low":        func(d *TeleportJoin) { d.MaxConcurrentFetches = 0 },
		"fetches high":       func(d *TeleportJoin) { d.MaxConcurrentFetches = 501 },
		"source timeout low": func(d *TeleportJoin) { d.SourceTimeoutSeconds = 1 },
		"source timeout hi":  func(d *TeleportJoin) { d.SourceTimeoutSeconds = 121 },
		"source empty":       func(d *TeleportJoin) { d.SourceService = "" },
		"source bad":         func(d *TeleportJoin) { d.SourceService = "teleport:9000" },
		"sources entry":      func(d *TeleportJoin) { d.Sources = []string{"svc/teleport"} },
		"sources too many": func(d *TeleportJoin) {
			d.Sources = nil
			for i := 0; i < 33; i++ {
				d.Sources = append(d.Sources, "s")
			}
		},
	} {
		c := valid()
		mut(&c.TeleportJoin)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "teleport_join") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The edges of every bound are accepted.
	c = valid()
	c.TeleportJoin.ReportTimeoutMinutes, c.TeleportJoin.MaxConcurrentFetches, c.TeleportJoin.SourceTimeoutSeconds = 15, 500, 120
	if err := c.Validate(); err != nil {
		t.Fatalf("upper/lower edges: %v", err)
	}
}

func TestTeleportJoinPlaintextIngest(t *testing.T) {
	c := valid()
	c.TeleportJoin.AllowPlaintextIngest = true
	if err := c.Validate(); err != nil {
		t.Fatalf("dev: %v", err)
	}
	if w := strings.Join(c.Warnings(), "\n"); !strings.Contains(w, "teleport_join.allow_plaintext_ingest") {
		t.Fatalf("no startup warning: %s", w)
	}
	c.Env = "production"
	c.DB.DSN = "postgres://h/db?sslmode=verify-full"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "teleport_join.allow_plaintext_ingest") {
		t.Fatalf("production: %v", err)
	}
}

func TestTeleportJoinLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	raw := "teleport_join:\n  enabled: true\n  sources: [teleport]\n  source_service: teleport-svc\n  report_timeout_minutes: 45\n" +
		"  max_concurrent_fetches: 3\n  source_timeout_seconds: 5\n  allow_plaintext_ingest: false\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	d := c.TeleportJoin
	if err != nil || !d.Enabled || d.SourceService != "teleport-svc" || d.ReportTimeoutMinutes != 45 || d.MaxConcurrentFetches != 3 ||
		d.SourceTimeoutSeconds != 5 {
		t.Fatalf("load = %+v %v", d, err)
	}
}

func TestAgentTeleportDefaultsAndRules(t *testing.T) {
	tp := DefaultAgent().Teleport
	if !tp.Enabled || tp.TimeoutMinutes != 15 || tp.AllowInsecureTransport || tp.Timeout() != 15*time.Minute {
		t.Fatalf("defaults = %+v", tp)
	}
	a := agentWithModules()
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []int{4, 61} {
		a := agentWithModules()
		a.Teleport.TimeoutMinutes = m
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "teleport.timeout_minutes") {
			t.Errorf("timeout %d: %v", m, err)
		}
	}
	// Disabled is still validated.
	a.Teleport.Enabled, a.Teleport.TimeoutMinutes = false, 60
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestAgentTeleportAnnounce: teleport.v1 on Linux whether or not enabled;
// joins only when enabled and over TLS unless the plaintext opt-out is set
// locally (warned about).
func TestAgentTeleportAnnounce(t *testing.T) {
	tp := DefaultAgent().Teleport
	if !tp.Announce("linux") || tp.Announce("windows") {
		t.Fatal("announce")
	}
	if !tp.Accepts(false) || tp.Accepts(true) {
		t.Fatal("accepts")
	}
	tp.AllowInsecureTransport = true
	if !tp.Accepts(true) {
		t.Fatal("opt-out")
	}
	tp.Enabled = false
	if tp.Accepts(false) || !tp.Announce("linux") {
		t.Fatal("disabled")
	}
	a := agentWithModules()
	a.Insecure, a.Teleport.AllowInsecureTransport = true, true
	if w := strings.Join(a.Warnings(), "\n"); !strings.Contains(w, "teleport.allow_insecure_transport") {
		t.Fatalf("warnings = %s", w)
	}
}

func TestAgentTeleportLoad(t *testing.T) {
	cfg, err := decodeAgent([]byte("teleport:\n  enabled: false\n  timeout_minutes: 30\n  allow_insecure_transport: true\n"))
	tp := cfg.Teleport
	if err != nil || tp.Enabled || tp.TimeoutMinutes != 30 || !tp.AllowInsecureTransport {
		t.Fatalf("load = %+v %v", tp, err)
	}
	if _, err := decodeAgent([]byte("teleport:\n  version: 17.0.0\n")); err == nil {
		t.Fatal("unknown key accepted")
	}
}
