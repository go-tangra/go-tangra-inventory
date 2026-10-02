//go:build linux

package agentcerts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecRunnerEnvAndExit(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env.txt")
	p := script(t, `env > "$OUT"; pwd >> "$OUT"; read x; echo "stdin:[$x] args:$#"; exit 3`)
	dir := t.TempDir()
	res := ExecRunner{}.Run(context.Background(), HookSpec{Path: p, Dir: dir, Env: []string{"PATH=/usr/bin:/bin", "OUT=" + out, "LCM_X=1"},
		Timeout: 10 * time.Second})
	if res.Err != nil || res.TimedOut || res.ExitCode != 3 || strings.TrimSpace(string(res.Output)) != "stdin:[] args:0" {
		t.Fatalf("result = %+v %q", res, res.Output)
	}
	env, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(env)), "\n")
	got := map[string]bool{}
	for _, l := range lines {
		got[l] = true
	}
	if !got["LCM_X=1"] || !got["OUT="+out] || !got[dir] {
		t.Fatalf("env = %q", env)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "HOME=") || strings.HasPrefix(l, "USER=") || strings.HasPrefix(l, "GOPATH=") {
			t.Fatalf("inherited %s", l)
		}
	}
}

func TestExecRunnerBoundsOutput(t *testing.T) {
	p := script(t, `i=0; while [ $i -lt 600 ]; do echo 0123456789abcdef; i=$((i+1)); done; echo ERR >&2`)
	res := ExecRunner{}.Run(context.Background(), HookSpec{Path: p, Dir: "/", Env: []string{"PATH=/usr/bin:/bin"}, Timeout: 10 * time.Second})
	if res.ExitCode != 0 || len(res.Output) != MaxHookOutput {
		t.Fatalf("output %d bytes, code %d", len(res.Output), res.ExitCode)
	}
}

// TestExecRunnerTimeoutKillsGroup: at the timeout the hook and its children
// are killed, also when they ignore SIGTERM.
func TestExecRunnerTimeoutKillsGroup(t *testing.T) {
	for _, body := range []string{
		`sleep 300 & echo $! > "$PIDF"; wait`,
		`trap '' TERM; sleep 300 & echo $! > "$PIDF"; wait; wait; sleep 300`,
	} {
		pidf := filepath.Join(t.TempDir(), "pid")
		p := script(t, body)
		start := time.Now()
		res := ExecRunner{KillGrace: 300 * time.Millisecond}.Run(context.Background(), HookSpec{Path: p, Dir: "/",
			Env: []string{"PATH=/usr/bin:/bin", "PIDF=" + pidf}, Timeout: 500 * time.Millisecond})
		if !res.TimedOut || res.ExitCode != HookTimedOut || time.Since(start) > 10*time.Second {
			t.Fatalf("result = %+v after %s", res, time.Since(start))
		}
		raw, _ := os.ReadFile(pidf)
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatalf("pid file: %q", raw)
		}
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			t.Fatalf("child %d survived the timeout (%s)", pid, body)
		}
	}
}

func TestExecRunnerCancelAndSignals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := ExecRunner{KillGrace: 100 * time.Millisecond}.Run(ctx, HookSpec{Path: script(t, "sleep 300"), Dir: "/", Timeout: time.Minute})
	if !res.TimedOut {
		t.Fatalf("cancel: %+v", res)
	}
	res = ExecRunner{}.Run(context.Background(), HookSpec{Path: script(t, "kill -9 $$"), Dir: "/", Timeout: time.Minute})
	if res.ExitCode != 128+9 {
		t.Fatalf("signal: %+v", res)
	}
	res = ExecRunner{}.Run(context.Background(), HookSpec{Path: "/nonexistent/hook", Dir: "/", Timeout: time.Minute})
	if res.Err == nil || res.ExitCode != hookNoStart {
		t.Fatalf("start: %+v", res)
	}
	noshebang := filepath.Join(t.TempDir(), "x")
	_ = os.WriteFile(noshebang, []byte("echo hi"), 0o700)
	if res := (ExecRunner{}).Run(context.Background(), HookSpec{Path: noshebang, Dir: "/", Timeout: time.Minute}); res.Err == nil {
		t.Fatalf("no shell fallback expected: %+v", res)
	}
	if exitCode(nil) != 255 {
		t.Fatal("nil state")
	}
}

func TestExecRunnerStat(t *testing.T) {
	p := script(t, "true")
	fi, err := ExecRunner{}.Lstat(p)
	if err != nil || !fi.IsRegular() || fi.UID != os.Getuid() || fi.Mode.Perm() != 0o700 {
		t.Fatalf("lstat %+v %v", fi, err)
	}
	link := p + ".link"
	_ = os.Symlink(p, link)
	if fi, _ := (ExecRunner{}).Lstat(link); !fi.IsSymlink() {
		t.Fatal("lstat followed")
	}
	if fi, _ := (ExecRunner{}).Stat(link); !fi.IsRegular() {
		t.Fatal("stat did not follow")
	}
	if _, err := (ExecRunner{}).Stat("/nonexistent"); err == nil {
		t.Fatal("stat missing")
	}
}

func TestBoundedBuffer(t *testing.T) {
	b := &boundedBuffer{max: 4}
	if n, _ := b.Write([]byte("ab")); n != 2 {
		t.Fatal(n)
	}
	if n, _ := b.Write([]byte("cdef")); n != 4 {
		t.Fatal(n)
	}
	_, _ = b.Write([]byte("g"))
	if !bytes.Equal(b.Bytes(), []byte("abcd")) {
		t.Fatalf("%q", b.Bytes())
	}
}
