//go:build windows

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

// eventLogWriter wraps an eventlog.Log so standard log.Printf calls
// are written to the Windows Event Log as informational messages.
type eventLogWriter struct {
	elog *eventlog.Log
}

func (w *eventLogWriter) Write(p []byte) (int, error) {
	err := w.elog.Info(1, string(p))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// SetupEventLog opens the named event log source and redirects the
// standard logger output to it.  Event log entries carry their own
// timestamps, so log flags are cleared.
func SetupEventLog(name string) {
	elog, err := eventlog.Open(name)
	if err != nil {
		return // fall back to default stderr logging
	}
	log.SetOutput(&eventLogWriter{elog: elog})
	log.SetFlags(0)
}

// IsWindowsService reports whether the process is running as a
// Windows service.
func IsWindowsService() bool {
	ok, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return ok
}

// serviceHandler implements svc.Handler for a long-running function.
type serviceHandler struct {
	name string
	run  func(ctx context.Context) error
}

func (h *serviceHandler) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.run(ctx)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-errCh:
			// run function returned on its own.
			status <- svc.Status{State: svc.StopPending}
			if err != nil {
				log.Printf("Service %s stopped with error: %v", h.name, err)
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
				// Wait for run to finish (with a generous timeout).
				select {
				case <-errCh:
				case <-time.After(30 * time.Second):
					log.Printf("Service %s: timed out waiting for graceful shutdown", h.name)
				}
				return false, 0
			}
		}
	}
}

// RunService runs the named Windows service, blocking until the
// service stops.  The run function receives a context that is
// cancelled when the SCM requests a stop.
func RunService(name string, run func(ctx context.Context) error) error {
	return svc.Run(name, &serviceHandler{name: name, run: run})
}

// Install registers a Windows service with the Service Control
// Manager and creates an event log source.
func Install(name, displayName, description, exePath string, args []string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	// Check if service already exists.
	s, err := m.OpenService(name)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", name)
	}

	cfg := mgr.Config{
		DisplayName: displayName,
		Description: description,
		StartType:   mgr.StartAutomatic,
	}

	s, err = m.CreateService(name, exePath, cfg, args...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// Best-effort: set recovery to restart on first two failures.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.NoAction},
	}, 86400) // reset period: 1 day

	// Register event log source.
	if err := eventlog.InstallAsEventCreate(name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// Non-fatal: the service itself is installed.
		log.Printf("Warning: could not install event log source: %v", err)
	}

	return nil
}

// Uninstall removes the named Windows service and its event log
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

	// Stop the service if it is running.
	status, err := s.Query()
	if err == nil && status.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		// Give it a moment to stop.
		for range 10 {
			time.Sleep(500 * time.Millisecond)
			status, err = s.Query()
			if err != nil || status.State == svc.Stopped {
				break
			}
		}
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	// Best-effort: remove event log source.
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
