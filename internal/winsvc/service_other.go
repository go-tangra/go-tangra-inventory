//go:build !windows

package winsvc

import (
	"context"
	"errors"
)

// IsWindowsService always returns false on non-Windows platforms.
func IsWindowsService() bool { return false }

// RunService is not supported on non-Windows platforms.
func RunService(_ string, _ func(ctx context.Context) error) error {
	return errors.New("windows services are not supported on this platform")
}

// SetupEventLog is a no-op on non-Windows platforms.
func SetupEventLog(_ string) {}

// Install is not supported on non-Windows platforms.
func Install(_, _, _, _ string, _ []string) error {
	return errors.New("windows service install is not supported on this platform")
}

// Uninstall is not supported on non-Windows platforms.
func Uninstall(_ string) error {
	return errors.New("windows service uninstall is not supported on this platform")
}

// ExePath returns the path to the currently running executable.
func ExePath() (string, error) {
	return "", errors.New("ExePath is only used on Windows")
}
