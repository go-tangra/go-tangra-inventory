//go:build linux

package upgrader

import "github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"

// NewInstaller returns the installer of this platform; configPath is the
// agent config the helper reads ("" = defaults).
func NewInstaller(r Runner, configPath string) selfupdate.Installer {
	l := NewLinux(r)
	l.ConfigPath = configPath
	return l
}
