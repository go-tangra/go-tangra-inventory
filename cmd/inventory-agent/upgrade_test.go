package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

type fakeUpdater struct {
	check     selfupdate.CheckResult
	update    selfupdate.CheckResult
	checkErr  error
	updateErr error
}

func (f fakeUpdater) Check(context.Context) (selfupdate.CheckResult, error) {
	return f.check, f.checkErr
}
func (f fakeUpdater) Update(context.Context) (selfupdate.CheckResult, error) {
	return f.update, f.updateErr
}

func deps(u fakeUpdater, enrolled, admin bool) cliDeps {
	return cliDeps{
		isAdmin:     func() bool { return admin },
		credentials: func(config.AgentConfig) (string, string, bool) { return "a1", "c", enrolled },
		newUpdater: func(config.AgentConfig, string, string, string) (updater, agentrelease.Platform, error) {
			return u, agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}, nil
		},
		apply: func(context.Context, config.AgentConfig, string, string) error { return nil },
	}
}

func TestUpdateCheckExitCodes(t *testing.T) {
	var out bytes.Buffer
	if code := runUpdate([]string{"-check"}, &out, deps(fakeUpdater{check: selfupdate.CheckResult{Available: true, TargetVersion: "4.5.0"}}, true, true)); code != 10 ||
		!strings.Contains(out.String(), "available 4.5.0 (linux/amd64 deb)") {
		t.Fatalf("available = %d %q", code, out.String())
	}
	out.Reset()
	if code := runUpdate([]string{"-check"}, &out, deps(fakeUpdater{}, true, true)); code != 0 || !strings.Contains(out.String(), "up to date") {
		t.Fatalf("up to date = %d %q", code, out.String())
	}
	if code := runUpdate([]string{"-check"}, &out, deps(fakeUpdater{checkErr: errors.New("unreachable")}, true, true)); code != 1 {
		t.Fatalf("error = %d", code)
	}
}

func TestUpdateRuns(t *testing.T) {
	var out bytes.Buffer
	started := fakeUpdater{update: selfupdate.CheckResult{Available: true, TargetVersion: "4.5.0", RequestID: "r1"}}
	if code := runUpdate(nil, &out, deps(started, true, true)); code != 0 || !strings.Contains(out.String(), "upgrade to 4.5.0 started (request r1)") {
		t.Fatalf("update = %d %q", code, out.String())
	}
	if code := runUpdate(nil, &out, deps(fakeUpdater{updateErr: selfupdate.ErrBusy}, true, true)); code != 2 {
		t.Fatalf("lock held = %d", code)
	}
	if code := runUpdate(nil, &out, deps(fakeUpdater{updateErr: errors.New("checksum_mismatch")}, true, true)); code != 1 {
		t.Fatalf("refused = %d", code)
	}
	out.Reset()
	if code := runUpdate(nil, &out, deps(fakeUpdater{}, true, true)); code != 0 || !strings.Contains(out.String(), "up to date") {
		t.Fatalf("nothing to do = %d %q", code, out.String())
	}
}

func TestUpdateRefusals(t *testing.T) {
	var out bytes.Buffer
	if code := runUpdate(nil, &out, deps(fakeUpdater{}, false, true)); code != 1 || !strings.Contains(out.String(), "requires an enrolled agent") {
		t.Fatalf("unenrolled = %d %q", code, out.String())
	}
	if code := runUpdate(nil, &out, deps(fakeUpdater{}, true, false)); code != 1 || !strings.Contains(out.String(), "root") {
		t.Fatalf("not root = %d", code)
	}
	if code := runUpdate([]string{"-bogus"}, &out, deps(fakeUpdater{}, true, true)); code != 1 {
		t.Fatalf("bad flag = %d", code)
	}
	if code := runUpdate([]string{"-config", "/nonexistent.yaml"}, &out, deps(fakeUpdater{}, true, true)); code != 1 {
		t.Fatalf("bad config = %d", code)
	}
	d := deps(fakeUpdater{}, true, true)
	d.newUpdater = func(config.AgentConfig, string, string, string) (updater, agentrelease.Platform, error) {
		return nil, agentrelease.Platform{}, errors.New("broken keyring")
	}
	if code := runUpdate(nil, &out, d); code != 1 {
		t.Fatalf("updater error = %d", code)
	}
}

func TestUpgradeApply(t *testing.T) {
	var out bytes.Buffer
	if code := runApply(nil, &out, deps(fakeUpdater{}, true, true)); code != 1 || !strings.Contains(out.String(), "usage") {
		t.Fatalf("no state = %d", code)
	}
	if code := runApply([]string{"-state", "/x/state.json"}, &out, deps(fakeUpdater{}, true, false)); code != 1 {
		t.Fatalf("not root = %d", code)
	}
	if code := runApply([]string{"-state", "/x/state.json"}, &out, deps(fakeUpdater{}, true, true)); code != 0 {
		t.Fatalf("apply = %d", code)
	}
	d := deps(fakeUpdater{}, true, true)
	d.apply = func(context.Context, config.AgentConfig, string, string) error { return errors.New("refused") }
	if code := runApply([]string{"-state", "/x/state.json"}, &out, d); code != 1 {
		t.Fatalf("apply error = %d", code)
	}
	if code := runApply([]string{"-state", "/x", "-config", "/nonexistent.yaml"}, &out, deps(fakeUpdater{}, true, true)); code != 1 {
		t.Fatalf("bad config = %d", code)
	}
	// The real helper refuses a state file outside the staging directory.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.yaml")
	_ = os.WriteFile(cfgPath, []byte("upgrade:\n  staging_dir: "+filepath.Join(dir, "staging")+"\n"), 0o600)
	cfg, _ := config.LoadAgent(cfgPath)
	err := applyUpgrade(context.Background(), cfg, cfgPath, filepath.Join(dir, "elsewhere", "state.json"))
	if !errors.Is(err, selfupdate.ErrStateLocation) {
		t.Fatalf("outside staging = %v", err)
	}
	if stagingDir(config.DefaultAgent()) == "" || stagingDir(cfg) != filepath.Join(dir, "staging") {
		t.Fatal("staging dir")
	}
}
