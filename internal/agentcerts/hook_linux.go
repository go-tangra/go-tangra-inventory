//go:build linux

package agentcerts

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// defaultKillGrace is the time between SIGTERM and SIGKILL at a timeout.
const defaultKillGrace = 5 * time.Second

// ExecRunner runs the deploy hook directly (no shell, no arguments) in its
// own process group with stdin from /dev/null and bounded output capture.
// KillGrace overrides the SIGTERM→SIGKILL delay (default 5 s).
type ExecRunner struct {
	KillGrace time.Duration
}

func osInfo(fi os.FileInfo, err error) (FileInfo, error) {
	if err != nil {
		return FileInfo{}, err
	}
	return infoOf(fi), nil
}

// Lstat describes path without following a final symlink.
func (ExecRunner) Lstat(path string) (FileInfo, error) { return osInfo(os.Lstat(path)) }

// Stat describes path, following symlinks.
func (ExecRunner) Stat(path string) (FileInfo, error) { return osInfo(os.Stat(path)) }

// Run executes the hook and waits for it; at the timeout (or when ctx ends)
// the whole process group gets SIGTERM, then SIGKILL after the grace period.
func (r ExecRunner) Run(ctx context.Context, spec HookSpec) HookOutcome {
	grace := r.KillGrace
	if grace <= 0 {
		grace = defaultKillGrace
	}
	out := &boundedBuffer{max: MaxHookOutput}
	cmd := &exec.Cmd{Path: spec.Path, Args: []string{spec.Path}, Dir: spec.Dir, Env: spec.Env,
		Stdout: out, Stderr: out, SysProcAttr: &syscall.SysProcAttr{Setpgid: true}, WaitDelay: grace}
	if err := cmd.Start(); err != nil {
		return HookOutcome{ExitCode: hookNoStart, Err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(spec.Timeout)
	defer timer.Stop()
	timedOut := false
	select {
	case <-done:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
		timedOut = true
	}
	if timedOut {
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(grace):
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return HookOutcome{ExitCode: HookTimedOut, TimedOut: true, Output: out.Bytes()}
	}
	return HookOutcome{ExitCode: exitCode(cmd.ProcessState), Output: out.Bytes()}
}

// exitCode maps the wait result: the exit status, 128+signal for a hook
// killed by a signal, 255 when no status is available.
func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return 255
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// boundedBuffer keeps the first max bytes written and discards the rest.
type boundedBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}
