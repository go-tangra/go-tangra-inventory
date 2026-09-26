//go:build linux

package collector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// fakeRunner records every command with its deadline and answers from a table.
type fakeRunner struct {
	mu        sync.Mutex
	calls     []string
	deadlines []time.Duration
	answers   map[string]fakeAnswer
	block     map[string]bool
}

type fakeAnswer struct {
	out  string
	exit int
	err  error
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	d, ok := ctx.Deadline()
	if !ok {
		f.deadlines = append(f.deadlines, -1)
	} else {
		f.deadlines = append(f.deadlines, time.Until(d))
	}
	a, known := f.answers[cmd]
	blocked := f.block[cmd]
	f.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return "", -1, ctx.Err()
	}
	if !known {
		return "", -1, errors.New("not found")
	}
	return a.out, a.exit, a.err
}

func (f *fakeRunner) ran(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func aptEnv(t *testing.T, r *fakeRunner) *updatesEnv {
	t.Helper()
	return &updatesEnv{
		run:      r,
		lookPath: func(n string) bool { return n == "apt-get" || n == "apt-config" || n == "systemctl" },
		exists:   func(p string) bool { return p == "/run/reboot-required" },
		now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		stateDir: t.TempDir(),
	}
}

func aptAnswers() map[string]fakeAnswer {
	return map[string]fakeAnswer{
		"apt-get -s -o Debug::NoLocking=1 dist-upgrade":     {out: "Inst openssl [3.0.1] (3.0.2 Ubuntu:22.04/jammy-security [amd64])\nInst curl [8.0] (8.1 Ubuntu:22.04/jammy-updates [amd64])\n"},
		"apt-config dump APT::Periodic::Unattended-Upgrade": {out: `APT::Periodic::Unattended-Upgrade "1";` + "\n"},
		"systemctl is-enabled apt-daily-upgrade.timer":      {out: "enabled\n"},
		"apt-get update -q": {out: "ok"},
	}
}

func TestCollectUpdatesApt(t *testing.T) {
	r := &fakeRunner{answers: aptAnswers()}
	env := aptEnv(t, r)
	progs, st, dropped := env.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute},
		[]store.Program{{Name: "openssl", Version: "3.0.1"}, {Name: "curl", Version: "8.0"}})
	if st.PackageManager != "apt" || st.Status != store.UpdateAvailable || st.RebootRequired != store.TriTrue ||
		st.AutomaticUpdates != store.TriTrue || !st.SecurityClassified || st.PendingCount != 2 || st.SecurityCount != 1 ||
		st.CheckedAt.IsZero() || dropped != 0 {
		t.Fatalf("state = %+v", st)
	}
	if progs[0].AvailableVersion != "3.0.2" || !progs[0].SecurityUpdate || progs[1].AvailableVersion != "8.1" {
		t.Fatalf("programs = %+v", progs)
	}
	// Negative: no package list refresh by default.
	if r.ran("apt-get update") != 0 {
		t.Fatalf("package lists refreshed: %v", r.calls)
	}
	// Every command ran with a deadline of at most 60 s.
	for i, d := range r.deadlines {
		if d < 0 || d > commandTimeout {
			t.Fatalf("command %q deadline %v", r.calls[i], d)
		}
	}
}

func TestCollectUpdatesRefreshAtMostDaily(t *testing.T) {
	r := &fakeRunner{answers: aptAnswers()}
	env := aptEnv(t, r)
	opts := Options{CollectUpdates: true, RefreshPackageLists: true, UpdateTimeout: time.Minute}
	env.collect(context.Background(), opts, nil)
	env.collect(context.Background(), opts, nil)
	if n := r.ran("apt-get update"); n != 1 {
		t.Fatalf("refreshed %d times within 24 h", n)
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, refreshStampFile)); err != nil {
		t.Fatalf("refresh stamp not persisted: %v", err)
	}
	// A new process (fresh env, same state dir) still honours the stamp.
	env2 := aptEnv(t, r)
	env2.stateDir = env.stateDir
	env2.collect(context.Background(), opts, nil)
	if n := r.ran("apt-get update"); n != 1 {
		t.Fatalf("stamp ignored after restart: %d", n)
	}
	// After 24 h it refreshes again.
	env2.now = func() time.Time { return time.Unix(1_700_000_000, 0).Add(25 * time.Hour) }
	env2.collect(context.Background(), opts, nil)
	if n := r.ran("apt-get update"); n != 2 {
		t.Fatalf("no refresh after 24 h: %d", n)
	}
}

func TestCollectUpdatesTimeoutsAndErrors(t *testing.T) {
	r := &fakeRunner{answers: aptAnswers(), block: map[string]bool{"apt-get -s -o Debug::NoLocking=1 dist-upgrade": true}}
	env := aptEnv(t, r)
	start := time.Now()
	_, st, _ := env.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: 100 * time.Millisecond}, nil)
	if st.Status != store.UpdateError || time.Since(start) > 5*time.Second {
		t.Fatalf("total budget not enforced: %+v after %v", st, time.Since(start))
	}

	r = &fakeRunner{answers: map[string]fakeAnswer{"apt-get -s -o Debug::NoLocking=1 dist-upgrade": {exit: 100}}}
	_, st, _ = aptEnv(t, r).collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute}, nil)
	if st.Status != store.UpdateError {
		t.Fatalf("failed command: %+v", st)
	}

	none := &updatesEnv{run: &fakeRunner{}, lookPath: func(string) bool { return false }, exists: func(string) bool { return false },
		now: time.Now}
	_, st, _ = none.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute}, nil)
	if st.Status != store.UpdateUnsupported || st.AutomaticUpdates != store.TriUnknown || st.RebootRequired != store.TriUnknown {
		t.Fatalf("no manager: %+v", st)
	}

	_, st, _ = none.collect(context.Background(), Options{CollectUpdates: false}, nil)
	if st.Status != store.UpdateUnknown || st.CheckedAt.IsZero() == false {
		t.Fatalf("disabled: %+v", st)
	}
}

func TestCollectUpdatesDnfAndPacman(t *testing.T) {
	r := &fakeRunner{answers: map[string]fakeAnswer{
		"dnf -q -C check-update":                           {out: "openssl.x86_64  1:3.0.7-25.el9  baseos\n", exit: 100},
		"dnf -q -C updateinfo list --updates security":     {out: "RHSA-1 Important/Sec. openssl-1:3.0.7-25.el9.x86_64\n"},
		"needs-restarting -r":                              {exit: 1},
		"systemctl is-enabled dnf-automatic.timer":         {out: "disabled\n", exit: 1},
		"systemctl is-enabled dnf-automatic-install.timer": {out: "enabled\n"},
	}}
	env := &updatesEnv{run: r, lookPath: func(n string) bool { return n == "dnf" || n == "needs-restarting" || n == "systemctl" },
		exists: func(string) bool { return false }, now: time.Now}
	progs, st, _ := env.collect(context.Background(), Options{CollectUpdates: true, RefreshPackageLists: true, UpdateTimeout: time.Minute},
		[]store.Program{{Name: "openssl", Version: "3.0.7-24.el9"}})
	if st.PackageManager != "dnf" || st.Status != store.UpdateAvailable || st.SecurityCount != 1 || !st.SecurityClassified ||
		st.RebootRequired != store.TriTrue || st.AutomaticUpdates != store.TriTrue || progs[0].AvailableVersion != "3.0.7-25.el9" {
		t.Fatalf("dnf = %+v %+v", st, progs)
	}
	if r.ran("dnf makecache") != 1 {
		t.Fatalf("refresh with refresh_package_lists: %v", r.calls)
	}

	r = &fakeRunner{answers: map[string]fakeAnswer{"dnf -q -C check-update": {exit: 0}}}
	env.run = r
	_, st, _ = env.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute}, nil)
	if st.Status != store.UpdateUpToDate || st.SecurityClassified {
		t.Fatalf("dnf up to date = %+v", st)
	}

	pac := &updatesEnv{run: &fakeRunner{answers: map[string]fakeAnswer{"checkupdates": {out: "linux 6.5-1 -> 6.6-1\n"}}},
		lookPath: func(n string) bool { return n == "pacman" || n == "checkupdates" }, exists: func(string) bool { return false }, now: time.Now}
	_, st, _ = pac.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute}, nil)
	if st.PackageManager != "pacman" || st.Status != store.UpdateAvailable || st.AutomaticUpdates != store.TriFalse {
		t.Fatalf("pacman = %+v", st)
	}
	noCU := &updatesEnv{run: &fakeRunner{}, lookPath: func(n string) bool { return n == "pacman" }, exists: func(string) bool { return false }, now: time.Now}
	_, st, _ = noCU.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute}, nil)
	if st.Status != store.UpdateUnsupported {
		t.Fatalf("pacman without checkupdates = %+v", st)
	}
}

func TestCollectUpdatesApk(t *testing.T) {
	env := &updatesEnv{run: &fakeRunner{answers: map[string]fakeAnswer{
		"apk -u list": {out: "busybox-1.36.1-r5 x86_64 {busybox} (GPL-2.0-only) [upgradable from: busybox-1.36.1-r4]\n"},
	}}, lookPath: func(n string) bool { return n == "apk" }, exists: func(string) bool { return false }, now: time.Now}
	progs, st, _ := env.collect(context.Background(), Options{CollectUpdates: true, UpdateTimeout: time.Minute}, nil)
	if st.Status != store.UpdateAvailable || len(progs) != 1 || progs[0].Version != "1.36.1-r4" {
		t.Fatalf("apk = %+v %+v", st, progs)
	}
}

// The real runner uses no shell and a C locale.
func TestExecRunnerEnvironment(t *testing.T) {
	out, exit, err := execRunner{}.Run(context.Background(), "env")
	if err != nil || exit != 0 {
		t.Skipf("env unavailable: %v", err)
	}
	if !strings.Contains(out, "LANG=C\n") || !strings.Contains(out, "LC_ALL=C\n") {
		t.Fatalf("locale not forced: %s", out)
	}
	if _, exit, _ := (execRunner{}).Run(context.Background(), "false"); exit != 1 {
		t.Fatalf("exit code = %d", exit)
	}
	if _, _, err := (execRunner{}).Run(context.Background(), "definitely-not-a-command-xyz"); err == nil {
		t.Fatal("missing command")
	}
}
