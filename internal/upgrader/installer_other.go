//go:build !linux && !windows

package upgrader

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

// unsupported refuses every upgrade on platforms without release artifacts.
type unsupported struct{}

// NewInstaller returns the installer of this platform.
func NewInstaller(Runner, string) selfupdate.Installer { return unsupported{} }

func (unsupported) Supported(string) error { return ErrUnsupported }
func (unsupported) StartHelper(context.Context, string, string, string) error {
	return ErrUnsupported
}
func (unsupported) InstallPackage(context.Context, string, string, bool) error { return ErrUnsupported }
func (unsupported) SwapBinary(context.Context, string, string) (string, error) {
	return "", ErrUnsupported
}
func (unsupported) RestoreBinary(context.Context, string, string) error { return ErrUnsupported }
func (unsupported) RestartService(context.Context) error                { return ErrUnsupported }
