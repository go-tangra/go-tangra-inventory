//go:build windows

package upgrader

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

type fakeSCM struct {
	calls   []string
	stopErr error
}

func (f *fakeSCM) Stop(context.Context) error { f.calls = append(f.calls, "stop"); return f.stopErr }
func (f *fakeSCM) Start() error               { f.calls = append(f.calls, "start"); return nil }

func TestWindowsInstaller(t *testing.T) {
	scm := &fakeSCM{}
	var started *exec.Cmd
	w := &Windows{SCM: scm, Start: func(c *exec.Cmd) error { started = c; return nil },
		Swap:    func(n, t string) (string, error) { scm.calls = append(scm.calls, "swap"); return t + ".prev", nil },
		Restore: func(p, t string) error { scm.calls = append(scm.calls, "restore"); return nil }}
	if w.Supported("binary") != nil || w.Supported("deb") == nil {
		t.Fatal("supported")
	}
	if err := w.StartHelper(context.Background(), `C:\stage\helper.exe`, `C:\stage\state.json`, "r"); err != nil {
		t.Fatal(err)
	}
	if started.SysProcAttr.CreationFlags != windows.DETACHED_PROCESS|windows.CREATE_NEW_PROCESS_GROUP|windows.CREATE_BREAKAWAY_FROM_JOB ||
		strings.Join(started.Args[1:], " ") != `upgrade-apply -state C:\stage\state.json` {
		t.Fatalf("helper = %v %+v", started.Args, started.SysProcAttr)
	}
	if _, err := w.SwapBinary(context.Background(), "new.exe", `C:\agent\inventory-agent.exe`); err != nil {
		t.Fatal(err)
	}
	if err := w.RestartService(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.RestoreBinary(context.Background(), "p", "t"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(scm.calls, ","); got != "stop,swap,stop,start,stop,restore" {
		t.Fatalf("SCM sequence = %s", got)
	}
	if w.InstallPackage(context.Background(), "deb", "x", false) == nil {
		t.Fatal("packages on Windows")
	}
	scm.stopErr = errors.New("access denied")
	if _, err := w.SwapBinary(context.Background(), "n", "t"); err == nil {
		t.Fatal("stop error swallowed")
	}
}
