package upgrader

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

// Linux is the Linux installer: systemd (required for self-upgrade) runs the
// staged helper as a transient unit, packages are installed with dpkg/rpm
// directly (the artifact is local and verified; apt/dnf would touch
// repository metadata and the network), binaries are swapped atomically.
type Linux struct {
	Run         Runner
	SystemdDir  string              // /run/systemd/system: present when systemd is PID 1
	Sleep       func(time.Duration) // between dpkg lock retries
	Swap        func(string, string) (string, error)
	Restore     func(string, string) error
	LockRetries int    // attempts when the dpkg lock is busy (3 over 60 s)
	ConfigPath  string // agent config handed to the helper ("" = defaults)
}

var _ selfupdate.Installer = (*Linux)(nil)

// NewLinux returns the installer for the host.
func NewLinux(r Runner) *Linux {
	return &Linux{Run: r, SystemdDir: "/run/systemd/system", Sleep: time.Sleep, Swap: swapBinary, Restore: restoreBinary, LockRetries: 3}
}

// Supported requires systemd: without it no process survives the agent's
// restart to finish or roll back an upgrade (non-systemd hosts upgrade
// manually).
func (l *Linux) Supported(installType string) error {
	if st, err := os.Stat(l.SystemdDir); err != nil || !st.IsDir() {
		return fmt.Errorf("%w: systemd is not running (upgrade manually)", ErrUnsupported)
	}
	switch installType {
	case agentrelease.InstallDeb, agentrelease.InstallRPM, agentrelease.InstallBinary:
		return nil
	}
	return fmt.Errorf("%w: install type %q", ErrUnsupported, installType)
}

// StartHelper runs the staged helper as a transient systemd unit outside
// the agent's cgroup, so the package scripts' service restart does not kill
// the installer.
func (l *Linux) StartHelper(ctx context.Context, helper, stateFile, requestID string) error {
	args := []string{"--unit", "inventory-agent-upgrade-" + req8(requestID), "--collect", "--property=Type=exec", "--quiet",
		helper, "upgrade-apply", "-state", stateFile}
	if l.ConfigPath != "" {
		args = append(args, "-config", l.ConfigPath)
	}
	out, err := l.Run.Run(ctx, nil, "systemd-run", args...)
	if err != nil {
		return fmt.Errorf("upgrader: systemd-run: %w: %s", err, clip(out))
	}
	return nil
}

// InstallPackage installs a deb (dpkg -i --force-confold, non-interactive,
// retried while the dpkg lock is busy) or an rpm (rpm -U --replacepkgs,
// --oldpackage for a downgrade).
func (l *Linux) InstallPackage(ctx context.Context, installType, artifact string, downgrade bool) error {
	switch installType {
	case agentrelease.InstallDeb:
		var out []byte
		var err error
		for attempt := 1; attempt <= l.LockRetries; attempt++ {
			out, err = l.Run.Run(ctx, []string{"DEBIAN_FRONTEND=noninteractive"}, "dpkg", "-i", "--force-confold", artifact)
			if err == nil || !dpkgLocked(out) || attempt == l.LockRetries {
				break
			}
			l.Sleep(20 * time.Second)
		}
		if err != nil {
			return fmt.Errorf("upgrader: dpkg: %w: %s", err, clip(out))
		}
		return nil
	case agentrelease.InstallRPM:
		args := []string{"-U", "--replacepkgs"}
		if downgrade {
			args = append(args, "--oldpackage")
		}
		out, err := l.Run.Run(ctx, nil, "rpm", append(args, artifact)...)
		if err != nil {
			return fmt.Errorf("upgrader: rpm: %w: %s", err, clip(out))
		}
		return nil
	}
	return fmt.Errorf("%w: install type %q", ErrUnsupported, installType)
}

func dpkgLocked(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "dpkg frontend lock") || strings.Contains(s, "dpkg status database is locked") ||
		strings.Contains(s, "Could not get lock")
}

// SwapBinary replaces the agent binary atomically, keeping .prev.
func (l *Linux) SwapBinary(_ context.Context, newBinary, target string) (string, error) {
	return l.Swap(newBinary, target)
}

// RestoreBinary puts the previous binary back.
func (l *Linux) RestoreBinary(_ context.Context, previous, target string) error {
	return l.Restore(previous, target)
}

// RestartService restarts the agent unit.
func (l *Linux) RestartService(ctx context.Context) error {
	out, err := l.Run.Run(ctx, nil, "systemctl", "restart", SystemdUnit)
	if err != nil {
		return fmt.Errorf("upgrader: systemctl restart: %w: %s", err, clip(out))
	}
	return nil
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
