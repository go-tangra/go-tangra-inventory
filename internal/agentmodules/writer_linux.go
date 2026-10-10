//go:build linux

package agentmodules

import "github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"

// NewOS builds the Linux writer: the configured directory is created when
// missing (mode 0700; it must not be a symlink) and opened as an os.Root,
// so no write can leave it. Deps fields left nil get the OS
// implementations shared with the certificate store.
func NewOS(cfg Config, d Deps) (*Writer, error) {
	if d.FS == nil {
		f, err := agentcerts.OpenFS(cfg.Dir)
		if err != nil {
			return nil, err
		}
		d.FS = f
	}
	if d.Accounts == nil {
		d.Accounts = agentcerts.OSAccounts{}
	}
	if d.Hooks == nil {
		d.Hooks = agentcerts.ExecRunner{}
	}
	return New(cfg, d), nil
}
