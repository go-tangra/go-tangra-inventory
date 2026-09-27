package selfupdate

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

var cmd45 = Command{RequestID: "req-1", TargetVersion: "4.5.0"}

func TestUpgradeHappyPaths(t *testing.T) {
	for _, p := range []agentrelease.Platform{debP, rpmP, binP, winP} {
		t.Run(p.String(), func(t *testing.T) {
			f := newFixture(t, "4.4.0", p)
			if err := f.u.Upgrade(context.Background(), cmd45); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(f.client.states(), ","); got != "downloading,installing" {
				t.Fatalf("reports = %s", got)
			}
			st := f.state(t)
			entry := f.client.releases["4.5.0"].entries[p]
			if st.Phase != PhaseInstalling || st.RequestID != "req-1" || st.FromVersion != "4.4.0" || st.ToVersion != "4.5.0" ||
				st.InstallType != p.InstallType || st.ArtifactSHA256 != entry.SHA256 || st.Artifact != filepath.Join(staging, "4.5.0", entry.File) ||
				!st.Deadline.Equal(t0.Add(5*time.Minute)) || st.Executable != exe {
				t.Fatalf("state = %+v", st)
			}
			if f.fs.mode(st.Artifact) != 0o600 || f.fs.mode(filepath.Join(staging, "state.json")) != 0o600 || f.fs.mode(staging) != 0o700 {
				t.Fatalf("modes: artifact %o state %o dir %o", f.fs.mode(st.Artifact), f.fs.mode(filepath.Join(staging, "state.json")), f.fs.mode(staging))
			}
			helper := filepath.Join(staging, "4.5.0", helperName)
			if !f.fs.exists(helper) || f.inst.startedWith[0] != helper || f.inst.startedWith[1] != filepath.Join(staging, "state.json") {
				t.Fatalf("helper = %v", f.inst.startedWith)
			}
			if p.InstallType == "binary" {
				if st.RollbackArtifact != "" || st.PreviousBinary != "" {
					t.Fatalf("binary installs need no rollback package: %+v", st)
				}
			} else {
				rb := f.client.releases["4.4.0"].entries[p]
				if st.RollbackArtifact != filepath.Join(staging, "4.4.0", rb.File) || st.RollbackSHA256 != rb.SHA256 ||
					st.PreviousBinary != filepath.Join(staging, "4.4.0", backupName) || !f.fs.exists(st.PreviousBinary) {
					t.Fatalf("rollback material = %+v", st)
				}
			}
			if got := f.inst.log(); got != "start req-1" {
				t.Fatalf("installer calls = %s", got)
			}
			if held, _ := f.u.lock("other"); held != "req-1" {
				t.Fatalf("lock holder = %q", held)
			}
		})
	}
}

func TestVerificationFailuresNeverInstall(t *testing.T) {
	cases := map[string]struct {
		setup  func(f *fixture)
		reason string
	}{
		"signature_invalid": {func(f *fixture) {
			f.client.mutateHeader = func(h *Header) { h.Signature = append([]byte(nil), h.Signature...); h.Signature[0] ^= 1 }
		}, ReasonSignatureInvalid},
		"tampered manifest": {func(f *fixture) {
			f.client.mutateHeader = func(h *Header) { h.Manifest = append([]byte(nil), h.Manifest...); h.Manifest[20] ^= 1 }
		}, ReasonSignatureInvalid},
		"unknown_key": {func(f *fixture) {
			r := f.client.releases["4.5.0"]
			r.keyID = "rogue-key"
			f.client.releases["4.5.0"] = r
		}, ReasonUnknownKey},
		"checksum_mismatch": {func(f *fixture) {
			f.client.mutateData = func(b []byte) []byte { b[100] ^= 1; return b }
		}, ReasonChecksumMismatch},
		"truncated": {func(f *fixture) {
			f.client.mutateData = func(b []byte) []byte { return b[:len(b)-10] }
		}, ReasonSizeMismatch},
		"oversized": {func(f *fixture) {
			f.client.mutateData = func(b []byte) []byte { return append(b, 'x') }
		}, ReasonSizeMismatch},
		"platform_mismatch (no entry)": {func(f *fixture) {
			f.client.releases["4.5.0"] = makeRelease(t, f.priv, "test-key", "4.5.0", func(m *agentrelease.Manifest) { m.Artifacts = m.Artifacts[1:] })
		}, ReasonPlatformMismatch},
		"platform_mismatch (header)": {func(f *fixture) {
			f.client.mutateHeader = func(h *Header) { h.File = "other.rpm" }
		}, ReasonPlatformMismatch},
		"version_mismatch": {func(f *fixture) {
			f.client.releases["4.5.0"] = f.client.releases["4.6.0"]
		}, ReasonVersionMismatch},
		"disk_full":      {func(f *fixture) { f.fs.free = 1000 }, ReasonDiskFull},
		"download error": {func(f *fixture) { f.client.downloadErr["4.5.0"] = errors.New("unavailable") }, ReasonDownloadFailed},
		"stream error": {func(f *fixture) {
			f.client.nextErr = errors.New("reset")
			f.client.mutateData = func(b []byte) []byte { return b[:len(b)/2] }
		}, ReasonDownloadFailed},
		"out of order": {func(f *fixture) {
			f.client.mutateOffs = func(o []int64) { o[1] = 5 }
		}, ReasonDownloadFailed},
		"unsupported platform": {func(f *fixture) { f.u.cfg.Platform = agentrelease.Platform{OS: "linux", Arch: "amd64"} }, ReasonUnsupportedInstall},
		"no systemd":           {func(f *fixture) { f.inst.supported = errors.New("no systemd") }, ReasonUnsupportedInstall},
		"create fails":         {func(f *fixture) { f.fs.failOn["create:*"] = errors.New("ro fs") }, ReasonDiskFull},
		"write fails":          {func(f *fixture) { f.fs.failOn["write:*"] = errors.New("ENOSPC") }, ReasonDiskFull},
		"close fails":          {func(f *fixture) { f.fs.failOn["close:*"] = errors.New("EIO") }, ReasonDiskFull},
		"mkdir fails":          {func(f *fixture) { f.fs.failOn["mkdir:"+filepath.Join(staging, "4.5.0")] = errors.New("ro fs") }, ReasonDiskFull},
		"helper copy fails": {func(f *fixture) {
			f.fs.failOn["copy:"+filepath.Join(staging, "4.5.0", helperName)] = errors.New("ENOSPC")
		}, ReasonInstallFailed},
		"backup copy fails": {func(f *fixture) {
			f.fs.failOn["copy:"+filepath.Join(staging, "4.4.0", backupName)] = errors.New("ENOSPC")
		}, ReasonInstallFailed},
		"state write fails": {func(f *fixture) { f.fs.failOn["writeatomic:*"] = errors.New("ENOSPC") }, ReasonInstallFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "4.4.0", debP)
			c.setup(f)
			err := f.u.Upgrade(context.Background(), cmd45)
			if err == nil {
				t.Fatal("upgrade succeeded")
			}
			states := f.client.states()
			if last := states[len(states)-1]; last != "failed:"+c.reason {
				t.Fatalf("reports = %v, want failed:%s", states, c.reason)
			}
			if got := f.inst.log(); strings.Contains(got, "install") || strings.Contains(got, "swap") || strings.Contains(got, "start") {
				t.Fatalf("installer called after a refusal: %s", got)
			}
			if f.fs.exists(filepath.Join(staging, "lock")) || f.fs.exists(filepath.Join(staging, "state.json")) {
				t.Fatal("lock or state left behind")
			}
			if r := f.client.reports[len(f.client.reports)-1]; r.Detail == "" || len(r.Detail) > 256 {
				t.Fatalf("detail = %q", r.Detail)
			}
		})
	}
}

func TestStartHelperFailure(t *testing.T) {
	f := newFixture(t, "4.4.0", debP)
	f.inst.startErr = errors.New("systemd-run failed")
	if err := f.u.Upgrade(context.Background(), cmd45); err == nil {
		t.Fatal("helper start failure swallowed")
	}
	if got := f.client.states(); got[len(got)-1] != "failed:install_failed" || f.fs.exists(filepath.Join(staging, "state.json")) {
		t.Fatalf("reports = %v", got)
	}
}

func TestRollbackPackageUnavailable(t *testing.T) {
	f := newFixture(t, "4.4.0", debP)
	f.client.downloadErr["4.4.0"] = errors.New("not stored")
	if err := f.u.Upgrade(context.Background(), cmd45); err != nil {
		t.Fatal(err)
	}
	if st := f.state(t); st.RollbackArtifact != "" || st.PreviousBinary == "" {
		t.Fatalf("state = %+v", st)
	}
}

func TestVersionRules(t *testing.T) {
	cases := []struct {
		name    string
		running string
		cmd     Command
		want    string // "" = proceeds
	}{
		{"upgrade", "4.4.0", Command{RequestID: "r", TargetVersion: "4.5.0"}, ""},
		{"downgrade refused", "4.5.0", Command{RequestID: "r", TargetVersion: "4.4.0"}, "failed:downgrade_refused"},
		{"downgrade pinned", "4.5.0", Command{RequestID: "r", TargetVersion: "4.4.0", AllowDowngrade: true}, ""},
		{"below the floor even pinned", "4.5.0", Command{RequestID: "r", TargetVersion: "4.3.0", AllowDowngrade: true}, "failed:downgrade_refused"},
		{"not a release", "4.4.0", Command{RequestID: "r", TargetVersion: "latest"}, "failed:version_mismatch"},
		{"dev build upgrades", "dev", Command{RequestID: "r", TargetVersion: "4.5.0"}, ""},
		{"already current", "4.5.0", Command{RequestID: "r", TargetVersion: "4.5.0"}, "succeeded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.running, debP)
			err := f.u.Upgrade(context.Background(), c.cmd)
			states := f.client.states()
			switch c.want {
			case "":
				if err != nil || states[len(states)-1] != "installing" {
					t.Fatalf("err = %v reports = %v", err, states)
				}
			case "succeeded":
				if err != nil || len(states) != 1 || states[0] != "succeeded" || f.fs.exists(filepath.Join(staging, "lock")) {
					t.Fatalf("err = %v reports = %v", err, states)
				}
			default:
				if err == nil || states[len(states)-1] != c.want {
					t.Fatalf("err = %v reports = %v", err, states)
				}
			}
		})
	}
}

func TestLocking(t *testing.T) {
	f := newFixture(t, "4.4.0", debP)
	if err := f.u.Upgrade(context.Background(), cmd45); err != nil {
		t.Fatal(err)
	}
	n := len(f.client.reports)
	// Duplicate delivery of the same request: ignored.
	if err := f.u.Upgrade(context.Background(), cmd45); err != nil || len(f.client.reports) != n {
		t.Fatalf("duplicate = %v, reports %v", err, f.client.states())
	}
	// Another request while locked: busy.
	if err := f.u.Upgrade(context.Background(), Command{RequestID: "req-2", TargetVersion: "4.6.0"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second request = %v", err)
	}
	if r := f.client.reports[len(f.client.reports)-1]; r.State != "failed" || r.Reason != "busy" || r.RequestID != "req-2" {
		t.Fatalf("busy report = %+v", r)
	}
	// A stale lock is taken over.
	f.clock.now = t0.Add(time.Hour)
	if held, err := f.u.lock("req-3"); err != nil || held != "" {
		t.Fatalf("stale lock = %q %v", held, err)
	}
	// An empty or unreadable lock counts as held by an unknown upgrade.
	f2 := newFixture(t, "4.4.0", debP)
	f2.fs.put(filepath.Join(staging, "lock"), nil, 0o600)
	if held, _ := f2.u.lock("x"); held != "unknown" {
		t.Fatalf("empty lock = %q", held)
	}
	f2.fs.failOn["read:"+filepath.Join(staging, "lock")] = errors.New("EIO")
	if held, _ := f2.u.lock("x"); held != "unknown" {
		t.Fatalf("unreadable lock = %q", held)
	}
	f2.fs.failOn["excl:*"] = errors.New("EROFS")
	if _, err := f2.u.lock("x"); err == nil {
		t.Fatal("lock error swallowed")
	}
	f2.fs.put(filepath.Join(staging, "lock"), []byte("b"), 0o600)
	f2.clock.now = t0.Add(time.Hour)
	f2.fs.failOn["remove:"+filepath.Join(staging, "lock")] = errors.New("EPERM")
	if _, err := f2.u.lock("x"); err == nil {
		t.Fatal("stale lock removal error swallowed")
	}
	// A lock taken by someone else right after a stale takeover stays held.
	f3 := newFixture(t, "4.4.0", debP)
	f3.fs.put(filepath.Join(staging, "lock"), []byte("a"), 0o600)
	f3.clock.now = t0.Add(time.Hour)
	f3.fs.failOn["excl:*"] = fs.ErrExist
	f3.fs.failOn["excl:"+filepath.Join(staging, "lock")] = fs.ErrExist
	if held, err := f3.u.lock("x"); held != "unknown" || err != nil {
		t.Fatalf("racing lock = %q %v", held, err)
	}
	// Staging directory not creatable.
	f4 := newFixture(t, "4.4.0", debP)
	f4.fs.failOn["mkdir:"+staging] = errors.New("EROFS")
	if err := f4.u.Upgrade(context.Background(), cmd45); err == nil {
		t.Fatal("mkdir error swallowed")
	}
	f5 := newFixture(t, "4.4.0", debP)
	f5.fs.failOn["excl:*"] = errors.New("EROFS")
	if err := f5.u.Upgrade(context.Background(), cmd45); err == nil {
		t.Fatal("lock error swallowed")
	}
	f5.client.reportErr = errors.New("offline") // a lost report is logged, not fatal
	f5.u.report(context.Background(), cmd45, StateFailed, ReasonBusy, nil)
}

func TestCheckAndUpdate(t *testing.T) {
	f := newFixture(t, "4.4.0", debP)
	f.client.check = CheckResult{Available: true, TargetVersion: "4.5.0"}
	if res, err := f.u.Check(context.Background()); err != nil || !res.Available || f.client.checks[0] {
		t.Fatalf("check = %+v %v", res, err)
	}
	f.client.check = CheckResult{Available: true, TargetVersion: "4.5.0", RequestID: "req-cli"}
	res, err := f.u.Update(context.Background())
	if err != nil || res.RequestID != "req-cli" || !f.client.checks[1] || f.state(t).RequestID != "req-cli" {
		t.Fatalf("update = %+v %v", res, err)
	}
	f2 := newFixture(t, "4.5.0", debP)
	f2.client.check = CheckResult{Reason: "up_to_date"}
	if res, err := f2.u.Update(context.Background()); err != nil || res.Available || len(f2.client.reports) != 0 {
		t.Fatalf("up to date = %+v %v", res, err)
	}
}
