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
