package selfupdate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

var statePath = filepath.Join(staging, "state.json")

// staged runs the agent side of an upgrade so the helper has a state file.
func staged(t *testing.T, p agentrelease.Platform, cmd Command) *fixture {
	t.Helper()
	f := newFixture(t, "4.4.0", p)
	f.fs.put(filepath.Join(staging, "4.3.0", "old.deb"), []byte("old"), 0o600) // an older staging dir
	if err := f.u.Upgrade(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	f.client.reports = nil
	return f
}

// confirmOnSleep makes the new version (4.5.0) start and confirm while the
// helper waits.
func confirmOnSleep(t *testing.T, f *fixture) {
	t.Helper()
	f.clock.onSleep = func() {
		f.clock.onSleep = nil
		if err := f.updater("4.5.0", f.u.cfg.Platform).Resume(context.Background()); err != nil {
			t.Errorf("new agent resume: %v", err)
		}
	}
}

func TestApplyPackageConfirmed(t *testing.T) {
	for _, p := range []agentrelease.Platform{debP, rpmP} {
		t.Run(p.InstallType, func(t *testing.T) {
			f := staged(t, p, cmd45)
			confirmOnSleep(t, f)
			if err := f.u.Apply(context.Background(), statePath); err != nil {
				t.Fatal(err)
			}
			if got := f.inst.log(); !strings.Contains(got, "install "+p.InstallType+" agent-4.5.0-") || strings.Contains(got, "downgrade=true") {
				t.Fatalf("installer = %s", got)
			}
			if got := f.client.states(); len(got) != 1 || got[0] != "succeeded" || f.client.reports[0].FromVersion != "4.4.0" || f.client.reports[0].ToVersion != "4.5.0" {
				t.Fatalf("reports = %v", got)
			}
			if f.fs.exists(statePath) || f.fs.exists(filepath.Join(staging, "lock")) || f.fs.exists(filepath.Join(staging, "4.3.0", "old.deb")) {
				t.Fatal("state, lock or old staging dir left behind")
			}
			if !f.fs.exists(filepath.Join(staging, "4.4.0", backupName)) {
				t.Fatal("the last two versions are kept")
			}
		})
	}
}

func TestApplyTimeoutRollsBackPackage(t *testing.T) {
	f := staged(t, debP, cmd45)
	err := f.u.Apply(context.Background(), statePath)
	if err == nil || !strings.Contains(err.Error(), "start_timeout") {
		t.Fatalf("apply = %v", err)
	}
	if got := f.inst.log(); !strings.Contains(got, "install deb agent-4.4.0-0.deb downgrade=true; restart") {
		t.Fatalf("rollback installer calls = %s", got)
	}
	st := f.state(t)
	if st.Phase != PhaseRolledBack || st.Reason != ReasonStartTimeout || f.clock.Now().Before(t0.Add(5*time.Minute)) {
		t.Fatalf("state = %+v at %s", st, f.clock.Now())
	}
	// The old agent restarts and reports the rollback.
	if err := f.u.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.client.states(); len(got) != 1 || got[0] != "rolled_back:start_timeout" {
		t.Fatalf("reports = %v", got)
	}
	if f.fs.exists(statePath) || f.fs.exists(filepath.Join(staging, "lock")) {
		t.Fatal("state or lock left after the rollback report")
	}
}

func TestApplyRollbackFallbacks(t *testing.T) {
	// No rollback package: the binary copy is restored (package DB mismatch).
	f := newFixture(t, "4.4.0", debP)
	f.client.downloadErr["4.4.0"] = errors.New("not stored")
	if err := f.u.Upgrade(context.Background(), cmd45); err != nil {
		t.Fatal(err)
	}
	_ = f.u.Apply(context.Background(), statePath)
	if st := f.state(t); st.Phase != PhaseRolledBack || st.Reason != ReasonPackageDBMismatch || !strings.Contains(f.inst.log(), "restore "+backupName) {
		t.Fatalf("state = %+v calls = %s", st, f.inst.log())
	}
	// The rollback package fails to install: same fallback.
	f = staged(t, rpmP, cmd45)
	st := f.state(t)
	f.inst.installErr[st.RollbackArtifact] = errors.New("rpm: dependency hell")
	_ = f.u.Apply(context.Background(), statePath)
	if st := f.state(t); st.Phase != PhaseRolledBack || st.Reason != ReasonPackageDBMismatch {
		t.Fatalf("state = %+v", st)
	}
	// A tampered rollback package is not installed.
	f = staged(t, debP, cmd45)
	st = f.state(t)
	f.fs.put(st.RollbackArtifact, []byte("tampered"), 0o600)
	_ = f.u.Apply(context.Background(), statePath)
	if strings.Contains(f.inst.log(), "downgrade=true") || f.state(t).Reason != ReasonPackageDBMismatch {
		t.Fatalf("tampered rollback package installed: %s", f.inst.log())
	}
	// Nothing restorable: failed, install_failed; the old agent reports failed.
	f = staged(t, debP, cmd45)
	st = f.state(t)
	f.inst.installErr[st.RollbackArtifact] = errors.New("dpkg broken")
	f.inst.restoreErr = errors.New("EROFS")
	_ = f.u.Apply(context.Background(), statePath)
	if st := f.state(t); st.Phase != PhaseFailed || st.Reason != ReasonInstallFailed {
		t.Fatalf("state = %+v", st)
	}
	if err := f.u.Resume(context.Background()); err != nil || f.client.states()[0] != "failed:install_failed" {
		t.Fatalf("resume = %v %v", err, f.client.states())
	}
}

func TestApplyInstallFailure(t *testing.T) {
	f := staged(t, debP, cmd45)
	st := f.state(t)
	f.inst.installErr[st.Artifact] = errors.New("dpkg: error processing archive")
	err := f.u.Apply(context.Background(), statePath)
	if err == nil || !strings.Contains(err.Error(), "install_failed") {
		t.Fatalf("apply = %v", err)
	}
	if st := f.state(t); st.Phase != PhaseRolledBack || st.Reason != ReasonInstallFailed {
		t.Fatalf("state = %+v", st)
	}
}

func TestApplyPinnedDowngrade(t *testing.T) {
	f := newFixture(t, "4.5.0", rpmP)
	if err := f.u.Upgrade(context.Background(), Command{RequestID: "r", TargetVersion: "4.4.0", AllowDowngrade: true}); err != nil {
		t.Fatal(err)
	}
	f.clock.onSleep = func() { _ = f.updater("4.4.0", rpmP).Resume(context.Background()) }
	if err := f.u.Apply(context.Background(), statePath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.inst.log(), "install rpm agent-4.4.0-1.rpm downgrade=true") {
		t.Fatalf("installer = %s", f.inst.log())
	}
}

func TestApplyBinary(t *testing.T) {
	for _, p := range []agentrelease.Platform{binP, winP} {
		f := staged(t, p, cmd45)
		confirmOnSleep(t, f)
		if err := f.u.Apply(context.Background(), statePath); err != nil {
			t.Fatal(err)
		}
		if got := f.inst.log(); !strings.Contains(got, "swap agent-4.5.0-") || !strings.HasSuffix(got, "restart") {
			t.Fatalf("installer = %s", got)
		}
		// Timeout: the .prev binary is restored.
		f = staged(t, p, cmd45)
		_ = f.u.Apply(context.Background(), statePath)
		if st := f.state(t); st.Phase != PhaseRolledBack || st.Reason != ReasonStartTimeout || !strings.Contains(f.inst.log(), "restore inventory-agent.prev") {
			t.Fatalf("binary rollback: %+v %s", st, f.inst.log())
		}
		// Swap failure: nothing replaced, nothing to restore.
		f = staged(t, p, cmd45)
		f.inst.swapErr = errors.New("EXDEV")
		_ = f.u.Apply(context.Background(), statePath)
		if st := f.state(t); st.Phase != PhaseFailed || st.Reason != ReasonInstallFailed {
			t.Fatalf("swap failure: %+v", st)
		}
	}
	// The state write after the swap fails: rolled back with the swap's backup.
	f := staged(t, binP, cmd45)
	f.inst.onInstall = func() { f.fs.failOn["writeatomic:*"] = errors.New("ENOSPC") }
	_ = f.u.Apply(context.Background(), statePath)
	if !strings.Contains(f.inst.log(), "restore") {
		t.Fatalf("installer = %s", f.inst.log())
	}
}

func TestApplyRefusals(t *testing.T) {
	f := staged(t, debP, cmd45)
	if err := f.u.Apply(context.Background(), "/tmp/state.json"); !errors.Is(err, ErrStateLocation) {
		t.Fatalf("outside staging = %v", err)
	}
	if err := f.u.Apply(context.Background(), filepath.Join(staging, "..", "upgrade", "x", "..", "..", "state.json")); !errors.Is(err, ErrStateLocation) {
		t.Fatalf("traversal = %v", err)
	}
	f.fs.files[statePath].mode = 0o644
	if err := f.u.Apply(context.Background(), statePath); !errors.Is(err, ErrStateMode) {
		t.Fatalf("mode = %v", err)
	}
	f.fs.files[statePath].mode = 0o600
	f.fs.files[statePath].ownerRoot = false
	if err := f.u.Apply(context.Background(), statePath); !errors.Is(err, ErrStateMode) {
		t.Fatalf("owner = %v", err)
	}
	f.fs.files[statePath].ownerRoot = true
	f.fs.failOn["stat:"+statePath] = errors.New("EIO")
	if err := f.u.Apply(context.Background(), statePath); err == nil {
		t.Fatal("stat error swallowed")
	}
	f.fs.failOn["read:"+statePath] = errors.New("EIO")
	if err := f.u.Apply(context.Background(), statePath); err == nil {
		t.Fatal("read error swallowed")
	}
	if got := f.inst.log(); strings.Contains(got, "install") {
		t.Fatalf("installer called: %s", got)
	}
}

func TestApplyReverifies(t *testing.T) {
	cases := map[string]func(f *fixture, st *State){
		"tampered artifact":   func(f *fixture, st *State) { f.fs.put(st.Artifact, []byte("evil"), 0o600) },
		"artifact outside":    func(f *fixture, st *State) { st.Artifact = "/usr/bin/evil" },
		"state sha mismatch":  func(f *fixture, st *State) { st.ArtifactSHA256 = strings.Repeat("0", 64) },
		"install type switch": func(f *fixture, st *State) { st.InstallType = "rpm" },
		"manifest tampered":   func(f *fixture, st *State) { st.Manifest[10] ^= 1 },
		"artifact removed":    func(f *fixture, st *State) { _ = f.fs.Remove(st.Artifact) },
		"artifact unreadable": func(f *fixture, st *State) { f.fs.failOn["readerr:"+st.Artifact] = errors.New("EIO") },
		"manifest of another": func(f *fixture, st *State) {
			r := f.client.releases["4.6.0"]
			st.Manifest, st.Signature = r.manifest, r.signature
		},
		"platform not in list": func(f *fixture, st *State) {
			st.Platform = agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "deb"}
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := staged(t, debP, cmd45)
			st := f.state(t)
			mut(f, &st)
			if err := f.u.saveState(st); err != nil {
				t.Fatal(err)
			}
			if err := f.u.Apply(context.Background(), statePath); err == nil {
				t.Fatal("tampered state applied")
			}
			if got := f.inst.log(); strings.Contains(got, "install") || strings.Contains(got, "swap") || !strings.HasSuffix(got, "restart") {
				t.Fatalf("installer = %s", got)
			}
			if st := f.state(t); st.Phase != PhaseFailed {
				t.Fatalf("state = %+v", st)
			}
		})
	}
}

func TestApplyStorageErrors(t *testing.T) {
	f := staged(t, debP, cmd45)
	f.fs.failOn["writeatomic:"+statePath] = errors.New("ENOSPC")
	if err := f.u.Apply(context.Background(), statePath); err == nil {
		t.Fatal("state write error swallowed")
	}
	f = staged(t, debP, cmd45)
	f.clock.err = context.Canceled
	if err := f.u.Apply(context.Background(), statePath); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
	f = staged(t, debP, cmd45)
	f.inst.restartErr = errors.New("systemctl failed")
	if err := f.u.Apply(context.Background(), statePath); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("restart error = %v", err)
	}
	// A state file too large or not JSON is refused.
	f = staged(t, debP, cmd45)
	f.fs.put(statePath, []byte("{"), 0o600)
	if err := f.u.Apply(context.Background(), statePath); err == nil {
		t.Fatal("broken state accepted")
	}
	f.fs.put(statePath, make([]byte, maxStateBytes+1), 0o600)
	if _, err := f.u.loadState(); !errors.Is(err, errStateTooLarge) {
		t.Fatalf("large state = %v", err)
	}
	if f.u.sizeOf("/missing") != 0 {
		t.Fatal("size of a missing file")
	}
}

func TestResumeCases(t *testing.T) {
	f := newFixture(t, "4.4.0", debP)
	if err := f.u.Resume(context.Background()); err != nil {
		t.Fatal("no state must be a no-op")
	}
	f.fs.put(statePath, []byte("{"), 0o600)
	if err := f.u.Resume(context.Background()); err == nil {
		t.Fatal("broken state swallowed")
	}
	// Awaiting confirmation, report fails: retried later, state unchanged.
	f = staged(t, debP, cmd45)
	st := f.state(t)
	st.Phase = PhaseAwaitingConfirm
	_ = f.u.saveState(st)
	newAgent := f.updater("4.5.0", debP)
	f.client.reportErr = errors.New("offline")
	if err := newAgent.Resume(context.Background()); err == nil || f.state(t).Phase != PhaseAwaitingConfirm {
		t.Fatalf("failed report = %v %s", err, f.state(t).Phase)
	}
	// The old version still awaiting (new one not up yet): nothing to do.
	if err := f.u.Resume(context.Background()); err != nil || f.state(t).Phase != PhaseAwaitingConfirm {
		t.Fatal("old agent must not act on an awaiting state")
	}
	// Confirmed and running the new version: cleanup.
	st.Phase = PhaseConfirmed
	_ = f.u.saveState(st)
	f.client.reportErr = nil
	f.fs.failOn["readdir:"+staging] = errors.New("EIO") // cleanup is best effort
	if err := newAgent.Resume(context.Background()); err != nil || f.fs.exists(statePath) {
		t.Fatalf("confirmed cleanup = %v", err)
	}
	// Rolled back, report fails: the state stays for the next start.
	f = staged(t, debP, cmd45)
	st = f.state(t)
	st.Phase, st.Reason = PhaseRolledBack, ReasonStartTimeout
	_ = f.u.saveState(st)
	f.client.reportErr = errors.New("offline")
	if err := f.u.Resume(context.Background()); err == nil || !f.fs.exists(statePath) {
		t.Fatal("failed rollback report must keep the state")
	}
}

// A request arriving right after this version confirmed an upgrade (the
// helper has not cleaned up yet) takes the lock instead of failing busy; the
// previous version never takes over a lock of an unconfirmed upgrade.
func TestLockAfterConfirmedUpgrade(t *testing.T) {
	f := staged(t, debP, cmd45)
	st := f.state(t)
	st.Phase = PhaseConfirmed
	_ = f.u.saveState(st)
	if held, _ := f.u.lock("req-2"); held != cmd45.RequestID {
		t.Fatalf("old version took a confirmed lock of 4.5.0: held %q", held)
	}
	newAgent := f.updater("4.5.0", debP)
	if held, err := newAgent.lock("req-2"); err != nil || held != "" || f.fs.exists(statePath) {
		t.Fatalf("confirmed lock = %q %v (state kept %v)", held, err, f.fs.exists(statePath))
	}
	// Still awaiting confirmation: busy.
	f = staged(t, debP, cmd45)
	st = f.state(t)
	st.Phase = PhaseAwaitingConfirm
	_ = f.u.saveState(st)
	if held, _ := f.updater("4.5.0", debP).lock("req-2"); held != cmd45.RequestID {
		t.Fatalf("awaiting lock = %q", held)
	}
}

func TestRealClock(t *testing.T) {
	c := RealClock()
	if c.Now().IsZero() {
		t.Fatal("now")
	}
	if err := c.Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep = %v", err)
	}
	if reasonOf(errors.New("anything else")) != ReasonDownloadFailed {
		t.Fatal("default reason")
	}
	if clip("abcdef", 3) != "abc" || clip("ab", 3) != "ab" {
		t.Fatal("clip")
	}
	if (&failure{reason: "x", err: errors.New("y")}).Error() != "x: y" {
		t.Fatal("failure string")
	}
}
