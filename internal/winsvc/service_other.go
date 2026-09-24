//go:build !windows

// Package winsvc provides Windows Service Control Manager integration for the
// endpoint agent. On non-Windows platforms these are inert stubs: run the agent
// under an init system (systemd, launchd, etc.) instead.
package winsvc

import (
	"context"
	"errors"
)

// errUnsupported is returned by service operations on non-Windows platforms.
var errUnsupported = errors.New("winsvc: Windows services are not supported on this platform; use systemd/launchd")

// IsWindowsService always returns false off Windows.
func IsWindowsService() bool { return false }

// RunService is unsupported off Windows.
func RunService(_ string, _ func(ctx context.Context) error) error { return errUnsupported }

// SetupEventLog is a no-op off Windows.
func SetupEventLog(_ string) {}

// Install is unsupported off Windows.
func Install(_, _, _, _ string, _ []string) error { return errUnsupported }

// Uninstall is unsupported off Windows.
func Uninstall(_ string) error { return errUnsupported }

// ExePath is only used on Windows.
func ExePath() (string, error) { return "", errUnsupported }
