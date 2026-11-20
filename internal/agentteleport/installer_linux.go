//go:build linux

package agentteleport

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// systemctlPaths: the installer runs only systemctl, from the first of
// these that exists (merged and split /usr).
var systemctlPaths = []string{"/usr/bin/systemctl", "/bin/systemctl"}

// execSystemctl runs systemctl with a fixed argv and environment (no
// shell, no inherited variables); output is discarded so it can never
// reach a report.
type execSystemctl struct{ path string }

func (e execSystemctl) Run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, e.path, args...) // #nosec G204 -- fixed binary, arguments from constants
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "SYSTEMD_PAGER="}
	cmd.Dir = "/"
	return cmd.Run()
}

// NewOS is the production installer: the real filesystem, systemctl,
// https://cdn.teleport.dev and the wall clock. ErrUnsupported without
// systemctl.
func NewOS(timeout time.Duration) (*Installer, error) {
	sys := ""
	for _, p := range systemctlPaths {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			sys = p
			break
		}
	}
	if sys == "" {
		return nil, ErrUnsupported
	}
	return New(Deps{Root: "/", Arch: runtime.GOARCH, HTTP: &http.Client{Timeout: 10 * time.Minute},
		Systemctl: execSystemctl{path: sys}, Hostname: os.Hostname, Now: time.Now, Sleep: sleepCtx}, timeout), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
