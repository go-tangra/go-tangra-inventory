//go:build linux

package collector

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	// commandTimeout bounds every package-manager command.
	commandTimeout = 60 * time.Second
	// defaultUpdateTimeout bounds the whole update collection.
	defaultUpdateTimeout = 120 * time.Second
	// refreshInterval: with refresh_package_lists the lists are refreshed at
	// most this often.
	refreshInterval  = 24 * time.Hour
	refreshStampFile = "package-lists-refreshed"
	maxCommandOutput = 32 << 20
	maxOutputLine    = 1 << 20
)

// binDirs is the fixed search path for package-manager commands (the agent
// never inherits PATH into them).
var binDirs = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// cmdRunner runs one command without a shell and returns its stdout and exit
// code; err is set when it could not run or was cancelled.
type cmdRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, exit int, err error)
}

// execRunner runs commands with exec.CommandContext, LANG=C/LC_ALL=C, a fixed
// PATH, bounded stdout and no stdin.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed package-manager commands, no user input, no shell
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "PATH=" + strings.Join(binDirs, ":")}
	out := &limitedBuffer{max: maxCommandOutput}
	cmd.Stdout = out
	err := cmd.Run()
	text := dropLongLines(out.String())
	if err == nil {
		return text, 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return text, ee.ExitCode(), nil
	}
	return text, -1, err
}

type limitedBuffer struct {
	b   strings.Builder
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.b.String() }

// dropLongLines removes lines longer than maxOutputLine.
func dropLongLines(s string) string {
	if len(s) <= maxOutputLine {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if len(l) <= maxOutputLine {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// updatesEnv is the OS access of the update collector (faked in tests).
type updatesEnv struct {
	run      cmdRunner
	lookPath func(name string) bool
	exists   func(path string) bool
	now      func() time.Time
	stateDir string

	mu          sync.Mutex
	lastRefresh time.Time
}

// hostUpdates is the process-wide environment, so the in-memory refresh
// stamp survives between collections of the daemon.
var hostUpdates = &updatesEnv{run: execRunner{}, lookPath: inBinDirs, exists: exists, now: time.Now}

func inBinDirs(name string) bool {
	for _, d := range binDirs {
		if st, err := os.Stat(filepath.Join(d, name)); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// collectUpdates reads the package update state and merges available
// versions into programs.
func collectUpdates(ctx context.Context, opts Options, programs []store.Program) ([]store.Program, store.UpdateState, uint32) {
	hostUpdates.mu.Lock()
	hostUpdates.stateDir = opts.StateDir
	hostUpdates.mu.Unlock()
	return hostUpdates.collect(ctx, opts, programs)
}

func (e *updatesEnv) cmd(ctx context.Context, name string, args ...string) (string, int, error) {
	c, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return e.run.Run(c, name, args...)
}

func (e *updatesEnv) collect(ctx context.Context, opts Options, programs []store.Program) ([]store.Program, store.UpdateState, uint32) {
	if !opts.CollectUpdates {
		return programs, store.UpdateState{Status: store.UpdateUnknown, RebootRequired: store.TriUnknown, AutomaticUpdates: store.TriUnknown}, 0
	}
	budget := opts.UpdateTimeout
	if budget <= 0 {
		budget = defaultUpdateTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	st := store.UpdateState{CheckedAt: e.now().UTC()}
	mgr, supported := e.detect()
	st.PackageManager = mgr
	if !supported {
		mgr = ""
	}
	if mgr != "" && opts.RefreshPackageLists {
		e.maybeRefresh(ctx, mgr)
	}
	pend, sec, classified, err := e.pending(ctx, mgr)
	st.RebootRequired = e.reboot(ctx, mgr)
	st.AutomaticUpdates = e.automatic(ctx, mgr)
	out, counts, dropped := agentfacts.MergePending(programs, pend, sec)
	st.PendingCount, st.SecurityCount = counts.PendingCount, counts.SecurityCount
	st.SecurityClassified = classified && err == nil
	st.Status = agentfacts.UpdateStatus(mgr, err, len(pend))
	return out, st, dropped
}

// detect returns the first installed manager; pacman needs pacman-contrib's
// checkupdates (which never touches the system package database).
func (e *updatesEnv) detect() (string, bool) {
	for _, m := range agentfacts.Managers {
		bin := m
		if m == "apt" {
			bin = "apt-get"
		}
		if !e.lookPath(bin) {
			continue
		}
		if m == "pacman" && !e.lookPath("checkupdates") {
			return m, false
		}
		return m, true
	}
	return "", false
}

var errUpdateCommand = errors.New("collector: package manager command failed")

func (e *updatesEnv) pending(ctx context.Context, mgr string) ([]agentfacts.Pending, map[string]bool, bool, error) {
	switch mgr {
	case "apt":
		out, exit, err := e.cmd(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "dist-upgrade")
		if err != nil || exit != 0 {
			return nil, nil, false, errUpdateCommand
		}
		return agentfacts.ParseAptSimulate(out), nil, true, nil
	case "dnf", "yum":
		out, exit, err := e.cmd(ctx, mgr, "-q", "-C", "check-update")
		if err != nil || (exit != 0 && exit != 100) {
			return nil, nil, false, errUpdateCommand
		}
		var pend []agentfacts.Pending
		if exit == 100 {
			pend = agentfacts.ParseDnfCheckUpdate(out)
		}
		sout, sexit, serr := e.cmd(ctx, mgr, "-q", "-C", "updateinfo", "list", "--updates", "security")
		if serr != nil || sexit != 0 {
			return pend, nil, false, nil
		}
		return pend, agentfacts.ParseDnfSecurity(sout), true, nil
	case "apk":
		out, exit, err := e.cmd(ctx, "apk", "-u", "list")
		if err != nil || exit != 0 {
			return nil, nil, false, errUpdateCommand
		}
		return agentfacts.ParseApkUpgradable(out), nil, false, nil
	case "pacman":
		out, exit, err := e.cmd(ctx, "checkupdates")
		switch {
		case err != nil || (exit != 0 && exit != 2):
			return nil, nil, false, errUpdateCommand
		case exit == 2:
			return nil, nil, false, nil
		}
		return agentfacts.ParseCheckupdates(out), nil, false, nil
	}
	return nil, nil, false, nil
}

func (e *updatesEnv) reboot(ctx context.Context, mgr string) string {
	flag := e.exists("/run/reboot-required") || e.exists("/var/run/reboot-required")
	nrExit := -1
	if (mgr == "dnf" || mgr == "yum") && e.lookPath("needs-restarting") {
		if _, exit, err := e.cmd(ctx, "needs-restarting", "-r"); err == nil {
			nrExit = exit
		}
	}
	needrestart := ""
	if !flag && nrExit < 0 && e.lookPath("needrestart") {
		needrestart, _, _ = e.cmd(ctx, "needrestart", "-b", "-k")
	}
	return agentfacts.RebootRequired(flag, nrExit, needrestart)
}

var autoUpdateUnits = map[string][]string{
	"apt": {"apt-daily-upgrade.timer"},
	"dnf": {"dnf-automatic.timer", "dnf-automatic-install.timer"},
	"yum": {"yum-cron.service"},
}

func (e *updatesEnv) automatic(ctx context.Context, mgr string) string {
	aptCfg := ""
	if mgr == "apt" && e.lookPath("apt-config") {
		aptCfg, _, _ = e.cmd(ctx, "apt-config", "dump", "APT::Periodic::Unattended-Upgrade")
	}
	enabled := map[string]bool{}
	if e.lookPath("systemctl") {
		for _, u := range autoUpdateUnits[mgr] {
			out, _, err := e.cmd(ctx, "systemctl", "is-enabled", u)
			enabled[u] = err == nil && strings.TrimSpace(out) == "enabled"
		}
	}
	return agentfacts.AutomaticUpdates(mgr, aptCfg, enabled)
}

var refreshCommands = map[string][]string{
	"apt": {"apt-get", "update", "-q"},
	"dnf": {"dnf", "makecache", "-q"},
	"yum": {"yum", "makecache", "-q"},
	"apk": {"apk", "update", "-q"},
}

// maybeRefresh refreshes the package lists when the last refresh (in memory
// or recorded in the state directory) is older than refreshInterval.
func (e *updatesEnv) maybeRefresh(ctx context.Context, mgr string) {
	argv, ok := refreshCommands[mgr]
	if !ok {
		return
	}
	now := e.now()
	e.mu.Lock()
	last := e.lastRefresh
	stamp := ""
	if e.stateDir != "" {
		stamp = filepath.Join(e.stateDir, refreshStampFile)
	}
	e.mu.Unlock()
	if stamp != "" {
		if b, err := os.ReadFile(stamp); err == nil { // #nosec G304 -- agent state directory
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil && t.After(last) {
				last = t
			}
		}
	}
	if !last.IsZero() && now.Sub(last) < refreshInterval {
		return
	}
	_, _, _ = e.cmd(ctx, argv[0], argv[1:]...)
	e.mu.Lock()
	e.lastRefresh = now
	e.mu.Unlock()
	if stamp != "" {
		_ = os.WriteFile(stamp, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
	}
}
