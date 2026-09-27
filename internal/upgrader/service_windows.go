//go:build windows

package upgrader

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

// Process creation flags of the detached helper: it must outlive the
// service process it replaces (no console, own process group, out of the
// service's job object).
const helperFlags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB

// SCM controls the agent service.
type SCM interface {
	Stop(ctx context.Context) error
	Start() error
}

// Windows is the Windows installer: the verified staged helper runs
// detached, stops the service through the SCM, swaps the executable and
// starts the service again.
type Windows struct {
	SCM        SCM
	Start      func(cmd *exec.Cmd) error
	Swap       func(string, string) (string, error)
	Restore    func(string, string) error
	ConfigPath string // agent config handed to the helper ("" = defaults)
}

var _ selfupdate.Installer = (*Windows)(nil)

// NewInstaller returns the installer of this platform.
func NewInstaller(_ Runner, configPath string) selfupdate.Installer {
	return &Windows{SCM: serviceControl{name: WindowsService}, Start: startDetached, Swap: swapBinary, Restore: restoreBinary, ConfigPath: configPath}
}

// Supported: Windows agents are binary installs.
func (w *Windows) Supported(installType string) error {
	if installType != agentrelease.InstallBinary {
		return fmt.Errorf("%w: install type %q on Windows", ErrUnsupported, installType)
	}
	return nil
}

func helperCommand(helper, stateFile, configPath string) *exec.Cmd {
	args := []string{"upgrade-apply", "-state", stateFile}
	if configPath != "" {
		args = append(args, "-config", configPath)
	}
	cmd := exec.Command(helper, args...) // #nosec G204 -- the verified staged helper
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: helperFlags, HideWindow: true}
	return cmd
}

func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// StartHelper starts the staged helper detached from the service.
func (w *Windows) StartHelper(_ context.Context, helper, stateFile, _ string) error {
	return w.Start(helperCommand(helper, stateFile, w.ConfigPath))
}

// InstallPackage: there are no packages on Windows.
func (w *Windows) InstallPackage(context.Context, string, string, bool) error {
	return fmt.Errorf("%w: packages on Windows", ErrUnsupported)
}

// SwapBinary stops the service and swaps the executable (the service is
// started by RestartService).
func (w *Windows) SwapBinary(ctx context.Context, newBinary, target string) (string, error) {
	if err := w.SCM.Stop(ctx); err != nil {
		return "", err
	}
	return w.Swap(newBinary, target)
}

// RestoreBinary stops the service and restores the previous executable.
func (w *Windows) RestoreBinary(ctx context.Context, previous, target string) error {
	if err := w.SCM.Stop(ctx); err != nil {
		return err
	}
	return w.Restore(previous, target)
}

// RestartService stops (if running) and starts the service.
func (w *Windows) RestartService(ctx context.Context) error {
	if err := w.SCM.Stop(ctx); err != nil {
		return err
	}
	return w.SCM.Start()
}

// serviceControl is the SCM implementation.
type serviceControl struct{ name string }

func (s serviceControl) open() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	sv, err := m.OpenService(s.name)
	if err != nil {
		_ = m.Disconnect()
		return nil, nil, err
	}
	return m, sv, nil
}

// Stop stops the service and waits (<= 60 s) until it is stopped.
func (s serviceControl) Stop(ctx context.Context) error {
	m, sv, err := s.open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer sv.Close()
	st, err := sv.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := sv.Control(svc.Stop); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if st, err = sv.Query(); err == nil && st.State == svc.Stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("upgrader: service %s did not stop", s.name)
}

// Start starts the service.
func (s serviceControl) Start() error {
	m, sv, err := s.open()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer sv.Close()
	return sv.Start()
}
