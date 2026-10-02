package agentcerts

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const testHook = "/usr/local/sbin/reload-nginx.sh"

func withHook(c *Config) { c.Hook = testHook }

func TestNoHookConfiguredNeverRuns(t *testing.T) {
	h := newHarness(t, nil)
	b := h.ca.bundle(t, 1)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	req := b.request(h.ca, "www")
	req.RerunHook = true
	h.mustInstall(req, store.DeliveryUnchanged)
	if h.hooks.runCount() != 0 || h.hooks.lstatArg != "" {
		t.Fatal("something was executed without a configured hook")
	}
}

// TestHookEnvironment: exactly the contract table; values from the agent's
// own parse; nothing inherited.
func TestHookEnvironment(t *testing.T) {
	t.Setenv("SECRET_FROM_AGENT_ENV", "leak")
	h := newHarness(t, withHook)
	a, b := h.ca.bundle(t, 0x4f3a), h.ca.bundle(t, 0x5b)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	spec := h.hooks.runs[0]
	if spec.Path != testHook || spec.Dir != "/etc/inventory-agent/certs/live/www" || spec.Timeout != testConfig().HookTimeout {
		t.Fatalf("spec = %+v", spec)
	}
	want := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"LCM_CERT_NAME=www",
		"LCM_CERT_DIR=/etc/inventory-agent/certs/live/www",
		"LCM_CERT_PATH=/etc/inventory-agent/certs/live/www/cert.pem",
		"LCM_KEY_PATH=/etc/inventory-agent/certs/live/www/privkey.pem",
		"LCM_CHAIN_PATH=/etc/inventory-agent/certs/live/www/chain.pem",
		"LCM_FULLCHAIN_PATH=/etc/inventory-agent/certs/live/www/fullchain.pem",
		"LCM_COMMON_NAME=www.example.com",
		"LCM_DNS_NAMES=www.example.com,example.com",
		"LCM_IP_ADDRESSES=192.0.2.10",
		"LCM_SERIAL_NUMBER=4f3a",
		"LCM_EXPIRES_AT=2036-01-01T00:00:00Z",
		"LCM_IS_RENEWAL=false",
		"LCM_CERTIFICATE_ID=cert-1",
	}
	if strings.Join(spec.Env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env =\n%s", strings.Join(spec.Env, "\n"))
	}
	// A renewal (local metadata wins over the server's flag).
	req := b.request(h.ca, "www")
	req.IsRenewal = false
	h.mustInstall(req, store.DeliveryInstalled)
	if env := strings.Join(h.hooks.runs[1].Env, "\n"); !strings.Contains(env, "LCM_IS_RENEWAL=true") || !strings.Contains(env, "LCM_SERIAL_NUMBER=5b") {
		t.Fatalf("renewal env = %s", env)
	}
	// Without local metadata the server's flag is used; no key → empty key path.
	req = b.request(h.ca, "fresh")
	req.IsRenewal, req.KeyPEM, req.HasKey = true, nil, false
	h.mustInstall(req, store.DeliveryInstalled)
	env := strings.Join(h.hooks.runs[2].Env, "\n")
	if !strings.Contains(env, "LCM_IS_RENEWAL=true") || !strings.Contains(env, "LCM_KEY_PATH=\n") {
		t.Fatalf("fresh env = %s", env)
	}
	if strings.Contains(env, "SECRET_FROM_AGENT_ENV") {
		t.Fatal("agent environment inherited")
	}
}

func TestHookEnvCleansControlCharacters(t *testing.T) {
	env := hookEnv("/d", "www", "id\nX=1", certmaterial.Bundle{CommonName: "a\x00b\x1bc\u0085", DNSNames: []string{"x\r\ny"}}, false, false)
	for _, e := range env {
		for _, r := range e {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				t.Fatalf("control character in %q", e)
			}
		}
	}
	if !strings.Contains(strings.Join(env, "|"), "LCM_COMMON_NAME=abc|") || !strings.Contains(strings.Join(env, "|"), "LCM_CERTIFICATE_ID=idX=1") {
		t.Fatalf("env = %v", env)
	}
}

// TestHookResults: exit 0 → installed; non-zero → hook_failed with the code;
// timeout → hook_timeout/256; not startable → hook_failed/127. Files stay
// installed and the metadata records the run.
func TestHookResults(t *testing.T) {
	for _, c := range []struct {
		name    string
		outcome HookOutcome
		state   string
		reason  string
		code    int
	}{
		{"ok", HookOutcome{ExitCode: 0}, store.DeliveryInstalled, "", 0},
		{"exit 3", HookOutcome{ExitCode: 3, Output: []byte("reload failed: secret=xyz")}, store.DeliveryHookFailed, store.ReasonHookFailed, 3},
		{"timeout", HookOutcome{ExitCode: HookTimedOut, TimedOut: true}, store.DeliveryHookFailed, store.ReasonHookTimeout, HookTimedOut},
		{"no start", HookOutcome{ExitCode: 127, Err: errors.New("exec format error")}, store.DeliveryHookFailed, store.ReasonHookFailed, 127},
	} {
		h := newHarness(t, withHook)
		h.hooks.outcome = c.outcome
		b := h.ca.bundle(t, 1)
		res := h.mustInstall(b.request(h.ca, "www"), c.state)
		if res.Reason != c.reason || res.HookExitCode != c.code {
			t.Fatalf("%s: %+v", c.name, res)
		}
		if strings.Contains(res.Detail, "secret") || len(res.Detail) > 256 {
			t.Fatalf("%s: hook output in detail: %q", c.name, res.Detail)
		}
		if !bytes.Equal(h.fs.liveFile(t, "www", fileKey), b.key) {
			t.Fatalf("%s: files not installed", c.name)
		}
		m := h.st.readMeta("www")
		if m.LastHookExecution == nil || m.HookExitCode == nil || *m.HookExitCode != c.code {
			t.Fatalf("%s: metadata = %+v", c.name, m)
		}
	}
}

// TestHookOutputOnlyInLocalLog: output is logged (bounded) and never
// reported.
func TestHookOutputOnlyInLocalLog(t *testing.T) {
	h := newHarness(t, withHook)
	big := append([]byte("BEGIN-MARK"), bytes.Repeat([]byte("x"), 2*MaxHookOutput)...)
	big = append(big, []byte("END-MARK")...)
	h.hooks.outcome = HookOutcome{ExitCode: 1, Output: big}
	res := h.mustInstall(h.ca.bundle(t, 1).request(h.ca, "www"), store.DeliveryHookFailed)
	log := h.log.String()
	if !strings.Contains(log, "BEGIN-MARK") || strings.Contains(log, "END-MARK") {
		t.Fatal("output not bounded in the log")
	}
	if strings.Contains(res.Detail, "MARK") || res.Detail != "hook exited with code 1" {
		t.Fatalf("detail = %q", res.Detail)
	}
}

// TestHookRerun: an unchanged bundle runs the hook only with rerun_hook.
func TestHookRerun(t *testing.T) {
	h := newHarness(t, withHook)
	h.hooks.outcome = HookOutcome{ExitCode: 2}
	b := h.ca.bundle(t, 1)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryHookFailed)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryUnchanged)
	if h.hooks.runCount() != 1 {
		t.Fatal("hook ran for unchanged")
	}
	h.hooks.outcome = HookOutcome{}
	req := b.request(h.ca, "www")
	req.RerunHook = true
	h.fs.resetWrites()
	res := h.mustInstall(req, store.DeliveryInstalled)
	if h.hooks.runCount() != 2 || res.HookExitCode != 0 {
		t.Fatalf("rerun: %+v", res)
	}
	if *h.st.readMeta("www").HookExitCode != 0 {
		t.Fatal("metadata not updated")
	}
	if !strings.Contains(strings.Join(h.hooks.runs[1].Env, "|"), "LCM_IS_RENEWAL=false") {
		t.Fatal("rerun renewal flag")
	}
	// Metadata failures after a hook run are logged only.
	h.fs.setFail("Rename renewal/www.json", errBoom)
	h.mustInstall(req, store.DeliveryInstalled)
	if !strings.Contains(h.log.String(), "metadata not written") {
		t.Fatal("not logged")
	}
}

// TestHookRefused: file and directory checks before every run.
func TestHookRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*fakeHooks)
	}{
		{"missing", func(f *fakeHooks) { f.fileErr = fs.ErrNotExist }},
		{"symlink", func(f *fakeHooks) { f.file.Mode = fs.ModeSymlink | 0o777 }},
		{"directory", func(f *fakeHooks) { f.file.Mode = fs.ModeDir | 0o755 }},
		{"not root-owned", func(f *fakeHooks) { f.file.UID = 1000 }},
		{"group-writable", func(f *fakeHooks) { f.file.Mode = 0o775 }},
		{"other-writable", func(f *fakeHooks) { f.file.Mode = 0o757 }},
		{"not executable", func(f *fakeHooks) { f.file.Mode = 0o644 }},
		{"dir missing", func(f *fakeHooks) { f.dirErr = fs.ErrNotExist }},
		{"dir is a file", func(f *fakeHooks) { f.dir.Mode = 0o755 }},
		{"dir not root-owned", func(f *fakeHooks) { f.dir.UID = 1000 }},
		{"dir writable", func(f *fakeHooks) { f.dir.Mode = fs.ModeDir | 0o777 }},
	} {
		h := newHarness(t, withHook)
		c.mut(h.hooks)
		b := h.ca.bundle(t, 1)
		res := h.mustInstall(b.request(h.ca, "www"), store.DeliveryHookFailed)
		if res.Reason != store.ReasonHookRefused || res.HookExitCode != HookNotRun || h.hooks.runCount() != 0 {
			t.Fatalf("%s: %+v", c.name, res)
		}
		if !bytes.Equal(h.fs.liveFile(t, "www", fileKey), b.key) {
			t.Fatalf("%s: files not installed", c.name)
		}
		if h.hooks.lstatArg != testHook {
			t.Fatalf("%s: lstat %q", c.name, h.hooks.lstatArg)
		}
	}
	h := newHarness(t, withHook)
	h.mustInstall(h.ca.bundle(t, 1).request(h.ca, "www"), store.DeliveryInstalled)
	if h.hooks.statArg != "/usr/local/sbin" {
		t.Fatalf("dir stat %q", h.hooks.statArg)
	}
}
