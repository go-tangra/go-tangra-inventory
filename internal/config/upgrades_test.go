package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentReleasesDefaultsAndBounds(t *testing.T) {
	c := valid()
	r := c.AgentReleases
	if r.BundleDir != "/app/agent-releases" || r.KeepVersions != 5 || r.MaxConcurrentDownloads != 20 || r.ChunkBytes != 1<<20 ||
		r.RequestTTLHours != 168 || r.ProgressTimeoutMinutes != 15 {
		t.Fatalf("defaults = %+v", r)
	}
	if c.RequestTTL() != 7*24*time.Hour || c.ProgressTimeout() != 15*time.Minute {
		t.Fatalf("durations = %s %s", c.RequestTTL(), c.ProgressTimeout())
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*AgentReleases){
		"keep low":       func(r *AgentReleases) { r.KeepVersions = 1 },
		"keep high":      func(r *AgentReleases) { r.KeepVersions = 51 },
		"downloads low":  func(r *AgentReleases) { r.MaxConcurrentDownloads = 0 },
		"downloads high": func(r *AgentReleases) { r.MaxConcurrentDownloads = 501 },
		"chunk low":      func(r *AgentReleases) { r.ChunkBytes = 1024 },
		"chunk high":     func(r *AgentReleases) { r.ChunkBytes = 2 << 20 },
		"ttl low":        func(r *AgentReleases) { r.RequestTTLHours = 0 },
		"ttl high":       func(r *AgentReleases) { r.RequestTTLHours = 721 },
		"progress low":   func(r *AgentReleases) { r.ProgressTimeoutMinutes = 4 },
		"progress high":  func(r *AgentReleases) { r.ProgressTimeoutMinutes = 121 },
		"relative dir":   func(r *AgentReleases) { r.BundleDir = "agent-releases" },
	} {
		c := valid()
		mut(&c.AgentReleases)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "agent_releases") {
			t.Errorf("%s: %v", name, err)
		}
	}
	c = valid()
	c.AgentReleases.BundleDir = "" // no bundle directory: nothing seeded
	if err := c.Validate(); err != nil {
		t.Fatalf("empty bundle dir: %v", err)
	}
}

func TestAgentUpgradeConfig(t *testing.T) {
	a := DefaultAgent()
	if !a.Upgrade.Enabled || a.Upgrade.ConfirmTimeoutSeconds != 300 || a.Upgrade.StagingDir != "" || a.ConfirmTimeout() != 5*time.Minute {
		t.Fatalf("defaults = %+v", a.Upgrade)
	}
	a.IngestEndpoint, a.TokenFile = "x:1", "/t"
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []int{59, 1801} {
		b := a
		b.Upgrade.ConfirmTimeoutSeconds = s
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "confirm_timeout_seconds") {
			t.Errorf("confirm timeout %d: %v", s, err)
		}
	}
	b := a
	b.Upgrade.StagingDir = "relative/dir"
	if err := b.Validate(); err == nil {
		t.Error("relative staging dir accepted")
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("upgrade:\n  enabled: false\n  confirm_timeout_seconds: 600\n  staging_dir: /srv/staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgent(path)
	if err != nil || cfg.Upgrade.Enabled || cfg.Upgrade.ConfirmTimeoutSeconds != 600 || cfg.Upgrade.StagingDir != "/srv/staging" {
		t.Fatalf("loaded = %+v %v", cfg.Upgrade, err)
	}
	if err := os.WriteFile(path, []byte("upgrade:\n  url: https://example.org\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgent(path); err == nil {
		t.Fatal("unknown upgrade field accepted (no download source other than the ingest edge)")
	}
}
