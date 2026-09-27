package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

type fakeUpgrader struct {
	mu        sync.Mutex
	upgrades  []selfupdate.Command
	resumes   int
	resumeErr error
	done      chan struct{}
}

func (f *fakeUpgrader) Upgrade(_ context.Context, c selfupdate.Command) error {
	f.mu.Lock()
	f.upgrades = append(f.upgrades, c)
	f.mu.Unlock()
	if f.done != nil {
		close(f.done)
	}
	return errors.New("refused")
}

func (f *fakeUpgrader) Resume(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
	return f.resumeErr
}

var deb = agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}

func newDaemon(enabled bool, u *fakeUpgrader) *Daemon {
	cfg := config.DefaultAgent()
	cfg.Upgrade.Enabled = enabled
	d := New(cfg, "4.4.0").WithUpgrades(deb, func(string, string) Upgrader { return u })
	d.agentID = "a1"
	d.upg = d.newUpgrader("a1", "cred")
	return d
}

func TestStreamRequestCarriesPlatformAndCapability(t *testing.T) {
	d := newDaemon(true, &fakeUpgrader{})
	req := d.streamRequest()
	if req.GetAgentVersion() != "4.4.0" || req.GetPlatform().GetInstallType() != "deb" || req.GetPlatform().GetOs() != "linux" ||
		len(req.GetCapabilities()) != 1 || req.GetCapabilities()[0] != "upgrade.v1" {
		t.Fatalf("request = %v", req)
	}
	// Disabled server-pushed upgrades: no capability (the fleet shows manual).
	if req := newDaemon(false, &fakeUpgrader{}).streamRequest(); len(req.GetCapabilities()) != 0 || req.GetPlatform() == nil {
		t.Fatalf("disabled = %v", req)
	}
	// Without a detectable platform nothing is announced.
	plain := New(config.DefaultAgent(), "4.4.0")
	if req := plain.streamRequest(); req.GetPlatform() != nil || len(req.GetCapabilities()) != 0 || plain.Sender() == nil {
		t.Fatalf("plain = %v", req)
	}
}

func TestUpgradeCommandDispatch(t *testing.T) {
	u := &fakeUpgrader{done: make(chan struct{})}
	d := newDaemon(true, u)
	d.handleCommand(context.Background(), &invv1.Command{CommandId: "c1", Type: invv1.CommandType_COMMAND_TYPE_UPGRADE,
		Upgrade: &invv1.UpgradeCommand{RequestId: "r1", TargetVersion: "4.5.0", AllowDowngrade: true}})
	select {
	case <-u.done:
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade not dispatched")
	}
	u.mu.Lock()
	got := u.upgrades[0]
	u.mu.Unlock()
	if got != (selfupdate.Command{RequestID: "r1", TargetVersion: "4.5.0", AllowDowngrade: true}) {
		t.Fatalf("command = %+v", got)
	}
	// Disabled: ignored.
	u2 := &fakeUpgrader{}
	newDaemon(false, u2).handleCommand(context.Background(), &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_UPGRADE,
		Upgrade: &invv1.UpgradeCommand{RequestId: "r2", TargetVersion: "4.5.0"}})
	// Missing payload: ignored.
	newDaemon(true, u2).handleCommand(context.Background(), &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_UPGRADE})
	time.Sleep(50 * time.Millisecond)
	if len(u2.upgrades) != 0 {
		t.Fatalf("ignored upgrades dispatched: %+v", u2.upgrades)
	}
}

func TestRefreshUnchanged(t *testing.T) {
	d := newDaemon(true, &fakeUpgrader{})
	calls := 0
	d.refresh = func(context.Context) error { calls++; return nil }
	d.handleCommand(context.Background(), &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_REFRESH})
	d.refresh = func(context.Context) error { calls++; return errors.New("offline") }
	d.handleCommand(context.Background(), &invv1.Command{Type: invv1.CommandType_COMMAND_TYPE_REFRESH})
	d.handleCommand(context.Background(), &invv1.Command{Type: invv1.CommandType(99)}) // unknown: ignored
	if calls != 2 {
		t.Fatalf("refresh calls = %d", calls)
	}
}

func TestResumeAfterConnectAndSubmit(t *testing.T) {
	u := &fakeUpgrader{resumeErr: errors.New("server busy")}
	d := newDaemon(true, u)
	d.maybeResume(context.Background())
	d.connected.Store(true)
	d.maybeResume(context.Background())
	if u.resumes != 0 {
		t.Fatal("resume before the first submit")
	}
	d.submitted.Store(true)
	d.maybeResume(context.Background()) // fails: retried
	u.resumeErr = nil
	d.maybeResume(context.Background())
	d.maybeResume(context.Background()) // done: not again
	if u.resumes != 2 {
		t.Fatalf("resumes = %d", u.resumes)
	}
}
