package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigTLSOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	yaml := "ingest_endpoint: a.example.org:9977\ntoken_file: /t\nca_file: /etc/a/ca.pem\nserver_name: a.example.org\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := resolveConfig(path, flags{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CAFile != "/etc/a/ca.pem" || cfg.ServerName != "a.example.org" || cfg.Insecure {
		t.Fatalf("config values not kept: %+v", cfg)
	}

	cfg, err = resolveConfig(path, flags{ingest: "b.example.org:9977", caFile: "/etc/b/ca.pem", serverName: "b.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IngestEndpoint != "b.example.org:9977" || cfg.CAFile != "/etc/b/ca.pem" || cfg.ServerName != "b.example.org" {
		t.Fatalf("flag overrides not applied: %+v", cfg)
	}
}

func TestResolveConfigAutoEnrollOverrides(t *testing.T) {
	cfg, err := resolveConfig("", flags{autoKeyID: "ak_0123456789abcdef01234567", autoKeyFile: "/etc/inv/auto.key"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoEnroll.KeyID != "ak_0123456789abcdef01234567" || cfg.AutoEnroll.KeyFile != "/etc/inv/auto.key" {
		t.Fatalf("auto-enroll flags not applied: %+v", cfg.AutoEnroll)
	}
}

// TestAutoEnrollCommandNeedsPackagedConfig pins the console's foreground
// command (AutoEnrollCard.vue): with the packaged agent.yaml it is valid;
// flags alone lack credential_file and state_file.
func TestAutoEnrollCommandNeedsPackagedConfig(t *testing.T) {
	f := flags{ingest: "portal.example.org:9977", autoKeyID: "ak_0123456789abcdef01234567", autoKeyFile: "/etc/inventory-agent/auto-enroll.key"}

	cfg, err := resolveConfig(filepath.Join("..", "..", "packaging", "agent.yaml"), f)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("command with -config refused: %v", err)
	}

	cfg, err = resolveConfig("", f)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("command without -config accepted; the console may drop -config")
	}
}
