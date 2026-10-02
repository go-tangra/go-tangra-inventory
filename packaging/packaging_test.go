// Package packaging holds the agent's package files; its tests keep them in
// line with the agent (feature 033).
package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

// TestUnitKeepsCertificateDirectoryWritable: the agent writes certificates
// below /etc/inventory-agent/certs. The unit has no ProtectSystem today; if
// one is added, the certificate directory must be listed in ReadWritePaths.
func TestUnitKeepsCertificateDirectoryWritable(t *testing.T) {
	raw, err := os.ReadFile("inventory-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	if strings.Contains(unit, "ProtectSystem=") && !strings.Contains(unit, "ReadWritePaths=/etc/inventory-agent/certs") {
		t.Fatal("ProtectSystem without ReadWritePaths=/etc/inventory-agent/certs")
	}
	for _, want := range []string{"ProtectHome=yes", "PrivateTmp=yes", "NoNewPrivileges=yes"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %s (documented hook sandbox)", want)
		}
	}
}

// TestSampleCertificatesSection: the commented certificates section of the
// sample config, uncommented, is valid and equals the built-in defaults.
func TestSampleCertificatesSection(t *testing.T) {
	raw, err := os.ReadFile("agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(raw), "\n") {
		switch {
		case l == "# certificates:":
			in = true
			out = append(out, "certificates:")
		case in && strings.HasPrefix(l, "#   "):
			out = append(out, l[2:])
		default:
			in = false
		}
	}
	if len(out) != 12 {
		t.Fatalf("certificates section has %d lines:\n%s", len(out), strings.Join(out, "\n"))
	}
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAgent(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Certificates != config.DefaultAgent().Certificates {
		t.Fatalf("sample %+v differs from the defaults %+v", cfg.Certificates, config.DefaultAgent().Certificates)
	}
}
