package upgrader

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type call struct {
	env  []string
	argv string
}

// fakeRunner records commands; outputs/errors are served in order.
type fakeRunner struct {
	calls []call
	outs  [][]byte
	errs  []error
}

func (f *fakeRunner) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{env: env, argv: strings.Join(append([]string{name}, args...), " ")})
	i := len(f.calls) - 1
	var out []byte
	var err error
	if i < len(f.outs) {
		out = f.outs[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return out, err
}

func linux(t *testing.T, r *fakeRunner) *Linux {
	t.Helper()
	l := NewLinux(r)
	l.SystemdDir = t.TempDir() // "systemd is running"
	var slept time.Duration
	l.Sleep = func(d time.Duration) { slept += d }
	return l
}

func TestLinuxArgv(t *testing.T) {
	r := &fakeRunner{}
	l := linux(t, r)
	ctx := context.Background()
	if err := l.StartHelper(ctx, "/var/lib/inventory-agent/upgrade/4.5.0/inventory-agent-helper", "/var/lib/inventory-agent/upgrade/state.json", "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"); err != nil {
		t.Fatal(err)
	}
	if err := l.InstallPackage(ctx, "deb", "/s/a.deb", false); err != nil {
		t.Fatal(err)
	}
	if err := l.InstallPackage(ctx, "rpm", "/s/a.rpm", false); err != nil {
		t.Fatal(err)
	}
	if err := l.InstallPackage(ctx, "rpm", "/s/old.rpm", true); err != nil {
		t.Fatal(err)
	}
	if err := l.RestartService(ctx); err != nil {
		t.Fatal(err)
	}
	l.ConfigPath = "/etc/inventory-agent/agent.yaml"
	if err := l.StartHelper(ctx, "/h", "/s", "r"); err != nil || !strings.HasSuffix(r.calls[5].argv, "-state /s -config /etc/inventory-agent/agent.yaml") {
		t.Fatalf("helper with config = %v %q", err, r.calls[5].argv)
	}
	want := []string{
		"systemd-run --unit inventory-agent-upgrade-2f9d1b4c55 --collect --property=Type=exec --quiet /var/lib/inventory-agent/upgrade/4.5.0/inventory-agent-helper upgrade-apply -state /var/lib/inventory-agent/upgrade/state.json",
		"dpkg -i --force-confold /s/a.deb",
		"rpm -U --replacepkgs /s/a.rpm",
		"rpm -U --replacepkgs --oldpackage /s/old.rpm",
		"systemctl restart inventory-agent.service",
	}
	want[0] = strings.Replace(want[0], "2f9d1b4c55", "9d1b4c55", 1)
	for i, w := range want {
		if r.calls[i].argv != w {
			t.Errorf("call %d = %q\nwant %q", i, r.calls[i].argv, w)
		}
	}
	if len(r.calls[1].env) != 1 || r.calls[1].env[0] != "DEBIAN_FRONTEND=noninteractive" {
		t.Fatalf("dpkg env = %v", r.calls[1].env)
	}
	if err := l.InstallPackage(ctx, "snap", "/s/a.snap", false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("snap = %v", err)
	}
}

func TestDpkgLockRetried(t *testing.T) {
	locked := []byte("dpkg: error: dpkg frontend lock was locked by another process with pid 42")
	r := &fakeRunner{outs: [][]byte{locked, locked, nil}, errs: []error{errors.New("exit 2"), errors.New("exit 2"), nil}}
	l := linux(t, r)
	if err := l.InstallPackage(context.Background(), "deb", "/s/a.deb", false); err != nil || len(r.calls) != 3 {
		t.Fatalf("retry = %v after %d calls", err, len(r.calls))
	}
	r = &fakeRunner{outs: [][]byte{locked, locked, locked}, errs: []error{errors.New("2"), errors.New("2"), errors.New("2")}}
	l = linux(t, r)
	if err := l.InstallPackage(context.Background(), "deb", "/s/a.deb", false); err == nil || len(r.calls) != 3 {
		t.Fatalf("give up after 3 = %v (%d)", err, len(r.calls))
	}
	// Other dpkg errors are not retried.
	r = &fakeRunner{outs: [][]byte{[]byte("dpkg: error processing archive")}, errs: []error{errors.New("1")}}
	l = linux(t, r)
	if err := l.InstallPackage(context.Background(), "deb", "/s/a.deb", false); err == nil || len(r.calls) != 1 {
		t.Fatalf("no retry = %v (%d)", err, len(r.calls))
	}
	for _, msg := range []string{"dpkg status database is locked", "E: Could not get lock /var/lib/dpkg/lock-frontend"} {
		if !dpkgLocked([]byte(msg)) {
			t.Errorf("%q not recognised as a lock", msg)
		}
	}
}

func TestLinuxFailuresAndSystemd(t *testing.T) {
	r := &fakeRunner{errs: []error{errors.New("x"), errors.New("x"), errors.New("x")}, outs: [][]byte{[]byte(strings.Repeat("e", 300))}}
	l := linux(t, r)
	if err := l.StartHelper(context.Background(), "h", "s", ""); err == nil || len(err.Error()) > 300 {
		t.Fatalf("systemd-run error = %v", err)
	}
	if err := l.InstallPackage(context.Background(), "rpm", "a.rpm", false); err == nil {
		t.Fatal("rpm error swallowed")
	}
	if err := l.RestartService(context.Background()); err == nil {
		t.Fatal("systemctl error swallowed")
	}
	for _, it := range []string{"deb", "rpm", "binary"} {
		if err := l.Supported(it); err != nil {
			t.Fatalf("%s: %v", it, err)
		}
	}
	if err := l.Supported("snap"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("snap = %v", err)
	}
	l.SystemdDir = filepath.Join(t.TempDir(), "absent")
	if err := l.Supported("deb"); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("no systemd = %v", err)
	}
	if req8("") != "manual" || req8("AB-12") != "ab12" {
		t.Fatal("req8")
	}
}

func TestSwapAndRestore(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "inventory-agent")
	staged := filepath.Join(t.TempDir(), "staged")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(staged, []byte("new"), 0o600)
	l := NewLinux(&fakeRunner{})
	prev, err := l.SwapBinary(context.Background(), staged, target)
	if err != nil || prev != target+".prev" {
		t.Fatalf("swap = %q %v", prev, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("target = %q", b)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	if err := l.RestoreBinary(context.Background(), prev, target); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatalf("restored = %q", b)
	}
	// A backup outside the binary's directory (package installs) is copied back.
	backup := filepath.Join(t.TempDir(), "inventory-agent.prev")
	_ = os.WriteFile(backup, []byte("backup"), 0o700)
	if err := restoreBinary(backup, target); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "backup" {
		t.Fatalf("restored from backup = %q", b)
	}
	if _, err := swapBinary(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("missing staged binary swapped")
	}
	if _, err := swapBinary(staged, filepath.Join(dir, "no-target")); err == nil {
		t.Fatal("missing target swapped")
	}
	if err := restoreBinary(filepath.Join(t.TempDir(), "missing"), target); err == nil {
		t.Fatal("missing backup restored")
	}
}

func TestOSFS(t *testing.T) {
	var f OSFS
	dir := filepath.Join(t.TempDir(), "upgrade")
	if err := f.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o", st.Mode().Perm())
	}
	p := filepath.Join(dir, "a")
	w, err := f.Create(p, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("hello"))
	_ = w.Close()
	if b, _ := f.ReadFile(p); string(b) != "hello" {
		t.Fatalf("read = %q", b)
	}
	r, _ := f.Open(p)
	_ = r.Close()
	if err := f.CreateExclusive(filepath.Join(dir, "lock"), []byte("r1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateExclusive(filepath.Join(dir, "lock"), []byte("r2"), 0o600); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive = %v", err)
	}
	if err := f.WriteFileAtomic(filepath.Join(dir, "state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat(filepath.Join(dir, "state.json"))
	if err != nil || st.Mode.Perm() != 0o600 || st.Size != 2 || !st.Private {
		t.Fatalf("stat = %+v %v", st, err)
	}
	// The staging directory: private, a real directory, no symlink.
	d, err := f.StatDir(dir)
	if err != nil || !d.Private || !d.Mode.IsDir() || (runtime.GOOS != "windows" && d.OwnerRoot != (os.Getuid() == 0)) {
		t.Fatalf("stat dir = %+v %v", d, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.StatDir(dir); d.Private {
		t.Fatal("0755 staging directory counted as private")
	}
	if err := f.MkdirAll(dir, 0o700); err != nil { // an existing directory is tightened again
		t.Fatal(err)
	}
	if d, _ := f.StatDir(dir); !d.Private {
		t.Fatal("existing directory not tightened")
	}
	link := filepath.Join(filepath.Dir(dir), "link")
	if err := os.Symlink(dir, link); err == nil {
		if _, err := f.StatDir(link); err == nil {
			t.Fatal("symlink accepted as the staging directory")
		}
	}
	if _, err := f.StatDir(filepath.Join(dir, "state.json")); err == nil {
		t.Fatal("a file is not a directory")
	}
	if _, err := f.StatDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing directory")
	}
	_ = os.Chmod(filepath.Join(dir, "state.json"), 0o644)
	if st, _ := f.Stat(filepath.Join(dir, "state.json")); st.Private {
		t.Fatal("0644 file counted as private")
	}
	_ = os.Chmod(filepath.Join(dir, "state.json"), 0o600)
	if runtime.GOOS != "windows" && st.OwnerRoot != (os.Getuid() == 0) {
		t.Fatalf("owner root = %v", st.OwnerRoot)
	}
	if _, err := f.Stat(dir); err == nil {
		t.Fatal("a directory is not a regular file")
	}
	if err := f.CopyFile(p, filepath.Join(dir, "sub", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	names, err := f.ReadDir(dir)
	if err != nil || strings.Join(names, ",") != "a,lock,state.json,sub" {
		t.Fatalf("readdir = %v %v", names, err)
	}
	if free, err := f.Free(dir); err != nil || free == 0 {
		t.Fatalf("free = %d %v", free, err)
	}
	if err := f.Remove(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("removing a missing file = %v", err)
	}
	if err := f.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadDir(filepath.Join(dir, "sub")); err == nil {
		t.Fatal("removed dir listed")
	}
	if _, err := f.Free(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("free of a missing path")
	}
	if DefaultStagingDir() == "" {
		t.Fatal("staging dir")
	}
}

func TestPlatformDetection(t *testing.T) {
	r := &fakeRunner{}
	p := Platform(context.Background(), r)
	if p.OS != runtime.GOOS || p.Arch != runtime.GOARCH || p.InstallType != "binary" || len(r.calls) != 0 {
		t.Fatalf("test binary platform = %+v (%d calls)", p, len(r.calls))
	}
	if _, err := (ExecRunner{}).Run(context.Background(), []string{"X=1"}, "true"); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if NewInstaller(r, "/etc/inventory-agent/agent.yaml") == nil {
		t.Fatal("installer")
	}
}
