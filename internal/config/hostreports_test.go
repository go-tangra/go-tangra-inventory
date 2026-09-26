package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostReportsConfig(t *testing.T) {
	d := Default()
	if len(d.HostReports.Consumers) != 1 || d.HostReports.Consumers[0] != "ipam" || d.HostReports.MaxPageBytes != 3<<20 {
		t.Fatalf("host_reports defaults = %+v", d.HostReports)
	}
	if !d.HostReports.IsConsumer("ipam") || d.HostReports.IsConsumer("gateway") || d.HostReports.IsConsumer("") {
		t.Fatal("IsConsumer")
	}
	for _, tc := range []struct {
		name string
		mut  func(*HostReports)
	}{
		{"bad consumer", func(h *HostReports) { h.Consumers = []string{"IPAM!"} }},
		{"empty consumer", func(h *HostReports) { h.Consumers = []string{""} }},
		{"wildcard consumer", func(h *HostReports) { h.Consumers = []string{"*"} }},
		{"too many", func(h *HostReports) { h.Consumers = make([]string, 33) }},
		{"page small", func(h *HostReports) { h.MaxPageBytes = 1024 }},
		{"page large", func(h *HostReports) { h.MaxPageBytes = 8 << 20 }},
	} {
		c := valid()
		tc.mut(&c.HostReports)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "host_reports") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	c := valid()
	c.HostReports.Consumers = nil // no consumer: the cross-tenant RPC is closed to everyone
	if err := c.Validate(); err != nil {
		t.Fatalf("empty consumer list must be allowed: %v", err)
	}
}

func TestAgentCollectOptions(t *testing.T) {
	d := DefaultAgent()
	if !d.CollectBMC || !d.CollectUpdates || d.RefreshPackageLists || d.UpdateTimeoutSeconds != 120 {
		t.Fatalf("agent collect defaults = %+v", d)
	}
	if d.UpdateTimeout() != 2*time.Minute {
		t.Fatalf("UpdateTimeout = %v", d.UpdateTimeout())
	}
	base := DefaultAgent()
	base.IngestEndpoint = "https://x"
	base.TokenFile = "/t"
	for _, v := range []int{29, 601} {
		b := base
		b.UpdateTimeoutSeconds = v
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "update_timeout_seconds") {
			t.Errorf("update_timeout_seconds %d: %v", v, err)
		}
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	yaml := "ingest_endpoint: https://x\ntoken_file: /t\ncollect_bmc: false\ncollect_updates: false\nrefresh_package_lists: true\nupdate_timeout_seconds: 300\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.CollectBMC || a.CollectUpdates || !a.RefreshPackageLists || a.UpdateTimeoutSeconds != 300 || a.Validate() != nil {
		t.Fatalf("loaded = %+v", a)
	}
}
