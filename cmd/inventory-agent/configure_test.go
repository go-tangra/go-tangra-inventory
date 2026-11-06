package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
)

const testKeyID = "ak_0123456789abcdef01234567"

// configureEnv is a packaged agent.yaml in a temp dir and a fake systemd.
type configureEnv struct {
	dir, cfg, key string
	calls         []string
	deps          configureDeps
}

func newConfigureEnv(t *testing.T) *configureEnv {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	e := &configureEnv{dir: t.TempDir()}
	e.cfg = filepath.Join(e.dir, "agent.yaml")
	e.key = filepath.Join(e.dir, "auto-enroll.key")
	if err := os.WriteFile(e.cfg, raw, 0o640); err != nil {
		t.Fatal(err)
	}
	e.deps = configureDeps{
		isAdmin: func() bool { return true },
		systemd: func() bool { return true },
		systemctl: func(args ...string) error {
			e.calls = append(e.calls, strings.Join(args, " "))
			return nil
		},
	}
	return e
}

func (e *configureEnv) run(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	var out strings.Builder
	code := runConfigure(append([]string{"-config", e.cfg}, args...), strings.NewReader(stdin), &out, e.deps)
	return code, out.String()
}

// TestConfigureAutoEnroll is the console's command (AutoEnrollCard.vue) on a
// packaged host: the result is what the daemon accepts, the packaged
// settings and comments stay, the secret lands in a 0600 file and the
// service is enabled and restarted. Running it again changes nothing.
func TestConfigureAutoEnroll(t *testing.T) {
	e := newConfigureEnv(t)
	args := []string{"-ingest", "portal.example.org:9977", "-auto-enroll-key-id", testKeyID, "-auto-enroll-key-file", e.key, "-auto-enroll-secret-stdin"}
	code, out := e.run(t, "aks_secret\n", args...)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, out)
	}
	cfg, err := config.LoadAgent(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("daemon would refuse the config: %v", err)
	}
	if cfg.IngestEndpoint != "portal.example.org:9977" || cfg.AutoEnroll.KeyID != testKeyID || cfg.AutoEnroll.KeyFile != e.key {
		t.Fatalf("settings not written: %+v", cfg)
	}
	if cfg.CredentialFile != "/var/lib/inventory-agent/credential" || cfg.StateFile != "/var/lib/inventory-agent/state.json" || !cfg.CollectBMC {
		t.Fatalf("packaged settings lost: %+v", cfg)
	}
	raw, _ := os.ReadFile(e.cfg)
	if !strings.Contains(string(raw), "# host:port of the inventory ingest edge") || !strings.Contains(string(raw), "# certificates:") {
		t.Fatalf("comments lost:\n%s", raw)
	}
	if fi, _ := os.Stat(e.cfg); fi.Mode().Perm() != 0o640 {
		t.Fatalf("config mode %o, want 0640", fi.Mode().Perm())
	}
	key, err := os.ReadFile(e.key)
	if err != nil || string(key) != "aks_secret\n" {
		t.Fatalf("key file %q %v", key, err)
	}
	if fi, _ := os.Stat(e.key); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o, want 0600", fi.Mode().Perm())
	}
	if got := strings.Join(e.calls, ";"); got != "enable inventory-agent.service;restart inventory-agent.service" {
		t.Fatalf("systemctl calls %q", got)
	}

	if code, out := e.run(t, "aks_secret\n", args...); code != exitOK {
		t.Fatalf("second run: exit %d: %s", code, out)
	}
	again, _ := os.ReadFile(e.cfg)
	if string(again) != string(raw) {
		t.Fatalf("second run changed the config:\n%s\n---\n%s", raw, again)
	}
	if entries, _ := os.ReadDir(e.dir); len(entries) != 2 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

// TestConfigureRefusals: nothing is written when the result would not be
// valid, the secret is malformed or the caller is not root.
func TestConfigureRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, stdin string
		args        []string
		admin       bool
		want        string
	}{
		{"not root", "", []string{"-ingest", "h:9977"}, false, "must run as root"},
		{"no endpoint", "aks_x\n", []string{"-auto-enroll-key-id", testKeyID, "-auto-enroll-secret-stdin"}, true, "ingest_endpoint is required"},
		{"bad key id", "aks_x\n", []string{"-ingest", "h:9977", "-auto-enroll-key-id", "ak_nope", "-auto-enroll-secret-stdin"}, true, "key_id must look like"},
		{"bad secret", "nope\n", []string{"-ingest", "h:9977", "-auto-enroll-key-id", testKeyID, "-auto-enroll-secret-stdin"}, true, "must look like aks_"},
		{"missing key file", "", []string{"-ingest", "h:9977", "-auto-enroll-key-id", testKeyID}, true, "key file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newConfigureEnv(t)
			e.deps.isAdmin = func() bool { return tc.admin }
			before, _ := os.ReadFile(e.cfg)
			code, out := e.run(t, tc.stdin, append(tc.args, "-auto-enroll-key-file", e.key)...)
			if code != exitError || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, output %q, want %q", code, out, tc.want)
			}
			after, _ := os.ReadFile(e.cfg)
			if string(after) != string(before) {
				t.Fatal("config changed on refusal")
			}
			if _, err := os.Stat(e.key); err == nil {
				t.Fatal("key file written on refusal")
			}
			if entries, _ := os.ReadDir(e.dir); len(entries) != 1 {
				t.Fatalf("leftover files: %v", entries)
			}
			if len(e.calls) != 0 {
				t.Fatalf("systemctl called: %v", e.calls)
			}
		})
	}
}

// TestConfigureNoStart: -no-start and hosts without systemd only write.
func TestConfigureNoStart(t *testing.T) {
	e := newConfigureEnv(t)
	if code, out := e.run(t, "", "-ingest", "h:9977", "-token", "/etc/inventory-agent/enrollment.token", "-no-start"); code != exitOK || !strings.Contains(out, "systemctl restart") {
		t.Fatalf("exit %d: %s", code, out)
	}
	e.deps.systemd = func() bool { return false }
	if code, out := e.run(t, "", "-ingest", "h2:9977"); code != exitOK || !strings.Contains(out, "no systemd") {
		t.Fatalf("exit %d: %s", code, out)
	}
	if len(e.calls) != 0 {
		t.Fatalf("systemctl called: %v", e.calls)
	}
	cfg, _ := config.LoadAgent(e.cfg)
	if cfg.IngestEndpoint != "h2:9977" {
		t.Fatalf("ingest %q", cfg.IngestEndpoint)
	}
}
