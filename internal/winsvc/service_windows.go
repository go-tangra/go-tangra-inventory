//go:build windows

// Package winsvc integrates the endpoint agent with the Windows Service Control
// Manager: install/uninstall, running under the SCM, and routing the standard
// logger to the Windows Event Log.
package winsvc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

// eventLogWriter routes standard log output to the Windows Event Log.
type eventLogWriter struct {
	elog *eventlog.Log
}

func (w *eventLogWriter) Write(p []byte) (int, error) {
	if err := w.elog.Info(1, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// SetupEventLog registers (idempotently) and opens the named event source, then
// redirects the standard logger to it. Falls back to stderr on failure.
func SetupEventLog(name string) {
	_ = eventlog.InstallAsEventCreate(name, eventlog.Error|eventlog.Warning|eventlog.Info)
	elog, err := eventlog.Open(name)
	if err != nil {
		return
	}
	log.SetOutput(&eventLogWriter{elog: elog})
	log.SetFlags(0)
}

// IsWindowsService reports whether the process is running under the SCM.
func IsWindowsService() bool {
	ok, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return ok
}

// serviceHandler adapts a long-running function to svc.Handler.
type serviceHandler struct {
	name string
	run  func(ctx context.Context) error
}

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.run(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-errCh:
			status <- svc.Status{State: svc.StopPending}
			if err != nil {
				log.Printf("service %s stopped with error: %v", h.name, err)
				return false, 1
			}
			return false, 0
		case cr := <-req:
			switch cr.Cmd {
			case svc.Interrogate:
				status <- cr.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-errCh:
				case <-time.After(30 * time.Second):
					log.Printf("service %s: timed out waiting for graceful shutdown", h.name)
				}
				return false, 0
			}
		}
	}
}

// RunService runs the named service, blocking until it stops. The run function
// receives a context canceled when the SCM requests a stop.
func RunService(name string, run func(ctx context.Context) error) error {
	return svc.Run(name, &serviceHandler{name: name, run: run})
}

// Install registers the service with the SCM and creates an event log source.
func Install(name, displayName, description, exePath string, args []string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(name); err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", name)
	}

	s, err := m.CreateService(name, exePath, mgr.Config{
		DisplayName: displayName,
		Description: description,
		StartType:   mgr.StartAutomatic,
	}, args...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.NoAction},
	}, 86400)

	if err := eventlog.InstallAsEventCreate(name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		log.Printf("warning: could not install event log source: %v", err)
	}
	return nil
}

// Uninstall stops (if running) and removes the named service and its event log
// source.
func Uninstall(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service %s: %w", name, err)
	}
	defer s.Close()

	if status, err := s.Query(); err == nil && status.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		for range 10 {
			time.Sleep(500 * time.Millisecond)
			if status, err = s.Query(); err != nil || status.State == svc.Stopped {
				break
			}
		}
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(name)
	return nil
}

// ExePath returns the path to the currently running executable.
func ExePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", errors.New("cannot determine executable path")
	}
	return p, nil
}
