//go:build linux && agentcerts_e2e

package agentcerts

// Privileged end-to-end test of the store and the deploy hook with real
// ownership (make test-agent-certs; run as root, e.g. in a container:
// docker run --rm -v "$PWD":/src -w /src golang:1.26 go test -tags agentcerts_e2e ./internal/agentcerts/).

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const e2eGID = 33 // www-data on Debian; numeric so no account database is needed

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("agentcerts_e2e needs root (run make test-agent-certs in a privileged container)")
	}
}

// rootDir is a root-owned 0755 directory for hooks directly under "/":
// every ancestor of a hook must be root-owned and not group/other writable,
// so t.TempDir (under the world-writable /tmp) cannot hold one.
func rootDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/", "agentcerts-hooks-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func writeHook(t *testing.T, dir, name, body string, mode os.FileMode, uid int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(p, uid, 0); err != nil {
		t.Fatal(err)
	}
	return p
}

func e2eStore(t *testing.T, hook string, timeout time.Duration) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "certs")
	cfg := Config{Dir: dir, Owner: "root", Group: strconv.Itoa(e2eGID), DirMode: 0o750, CertMode: 0o644, KeyMode: 0o640,
		KeepPrevious: 1, Hook: hook, HookTimeout: timeout}
	st, err := NewOS(cfg, Deps{Now: func() time.Time { return testNow }, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}

func TestE2EOwnershipAndHookEnvironment(t *testing.T) {
	requireRoot(t)
	hd := rootDir(t)
	out := filepath.Join(hd, "env.out")
	hook := writeHook(t, hd, "reload.sh", "env > "+out, 0o755, 0)
	st, dir := e2eStore(t, hook, 30*time.Second)
	ca := newCA(t)
	res := st.Install(context.Background(), ca.bundle(t, 0x4f3a).request(ca, "www"))
	if res.State != store.DeliveryInstalled || res.HookExitCode != 0 {
		t.Fatalf("install: %+v", res)
	}
	for _, c := range []struct {
		rel       string
		mode      os.FileMode
		uid, gid  int
		directory bool
	}{
		{"", 0o750, 0, e2eGID, true}, {"live", 0o750, 0, e2eGID, true}, {"archive/www", 0o750, 0, e2eGID, true},
		{"live/www/privkey.pem", 0o640, 0, e2eGID, false}, {"live/www/cert.pem", 0o644, 0, e2eGID, false},
		{"renewal/www.json", 0o644, 0, e2eGID, false},
	} {
		fi, err := os.Stat(filepath.Join(dir, c.rel))
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if fi.Mode().Perm() != c.mode || int(st.Uid) != c.uid || int(st.Gid) != c.gid || fi.IsDir() != c.directory {
			t.Fatalf("%s: %v %d:%d", c.rel, fi.Mode(), st.Uid, st.Gid)
		}
	}
	env, _ := os.ReadFile(out)
	for _, want := range []string{"LCM_CERT_NAME=www", "LCM_SERIAL_NUMBER=4f3a", "LCM_KEY_PATH=" + dir + "/live/www/privkey.pem",
		"PATH=" + hookPath, "LANG=C.UTF-8"} {
		if !strings.Contains(string(env), want) {
			t.Fatalf("env lacks %s:\n%s", want, env)
		}
	}
	if strings.Contains(string(env), "HOME=") {
		t.Fatalf("inherited environment:\n%s", env)
	}
}

func TestE2EUserOwnedHookRefused(t *testing.T) {
	requireRoot(t)
	hd := rootDir(t)
	marker := filepath.Join(hd, "ran")
	hook := writeHook(t, hd, "user.sh", "touch "+marker, 0o755, 65534)
	st, _ := e2eStore(t, hook, 30*time.Second)
	ca := newCA(t)
	res := st.Install(context.Background(), ca.bundle(t, 1).request(ca, "www"))
	if res.State != store.DeliveryHookFailed || res.Reason != store.ReasonHookRefused || res.HookExitCode != HookNotRun {
		t.Fatalf("result: %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("refused hook ran")
	}
	// A group-writable hook directory is refused as well.
	_ = os.Chmod(hd, 0o775)
	hook2 := writeHook(t, hd, "root.sh", "touch "+marker, 0o755, 0)
	st2, _ := e2eStore(t, hook2, 30*time.Second)
	if res := st2.Install(context.Background(), ca.bundle(t, 2).request(ca, "www")); res.Reason != store.ReasonHookRefused {
		t.Fatalf("writable dir: %+v", res)
	}
	// A root-owned hook in a root-owned directory under the world-writable
	// /tmp is refused: an ancestor is writable by others (T110).
	tmpDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook3 := writeHook(t, tmpDir, "root.sh", "touch "+marker, 0o755, 0)
	st3, _ := e2eStore(t, hook3, 30*time.Second)
	if res := st3.Install(context.Background(), ca.bundle(t, 3).request(ca, "www")); res.Reason != store.ReasonHookRefused {
		t.Fatalf("writable ancestor: %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("refused hook ran")
	}
}

func TestE2EHookTimeoutKillsChildren(t *testing.T) {
	requireRoot(t)
	hd := rootDir(t)
	pidf := filepath.Join(hd, "pid")
	hook := writeHook(t, hd, "slow.sh", "sleep 600 & echo $! > "+pidf+"; wait", 0o755, 0)
	st, _ := e2eStore(t, hook, 30*time.Second)
	st.cfg.HookTimeout = time.Second // below the configurable minimum, for the test only
	ca := newCA(t)
	res := st.Install(context.Background(), ca.bundle(t, 1).request(ca, "www"))
	if res.State != store.DeliveryHookFailed || res.Reason != store.ReasonHookTimeout || res.HookExitCode != HookTimedOut {
		t.Fatalf("result: %+v", res)
	}
	raw, _ := os.ReadFile(pidf)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	deadline := time.Now().Add(10 * time.Second)
	for pid > 0 && syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if pid <= 0 || syscall.Kill(pid, 0) == nil {
		t.Fatalf("child %d survived", pid)
	}
}
