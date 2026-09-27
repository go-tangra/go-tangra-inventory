package upgrader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

// ServiceName is the agent's systemd unit (Linux) and SCM service (Windows).
const (
	SystemdUnit    = "inventory-agent.service"
	WindowsService = "FreyaInventoryAgent"
)

// ErrUnsupported refuses an install type this host cannot upgrade.
var ErrUnsupported = errors.New("upgrader: self-upgrade is not supported on this host")

// Runner runs a command with a fixed argument list (never a shell).
type Runner interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// Run implements Runner; env is added to the process environment.
func (ExecRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed command names and argument lists
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// DefaultStagingDir is the private staging directory of the platform.
func DefaultStagingDir() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "go-tangra", "inventory-agent", "upgrade")
	}
	return "/var/lib/inventory-agent/upgrade"
}

// Executable returns the resolved path of the running agent.
func Executable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// Platform detects the running agent's platform and install type (deb/rpm
// when the package owns the executable, binary otherwise).
func Platform(ctx context.Context, r Runner) agentrelease.Platform {
	exe, _ := Executable()
	in := agentfacts.InstallInputs{GOOS: runtime.GOOS, Executable: exe}
	if runtime.GOOS == "linux" && exe == agentfacts.PackagedExecutable {
		out, err := r.Run(ctx, nil, "dpkg-query", "-W", "-f=${Status}", agentfacts.PackageName)
		in.DpkgStatus, in.DpkgOK = string(out), err == nil
		out, err = r.Run(ctx, nil, "rpm", "-q", agentfacts.PackageName)
		in.RPMQuery, in.RPMOK = string(out), err == nil
	}
	return agentrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH, InstallType: agentfacts.DetectInstallType(in)}
}

// OSFS implements selfupdate.FS on the host file system.
type OSFS struct{}

var _ selfupdate.FS = OSFS{}

func (OSFS) MkdirAll(path string, perm fs.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm) // an existing directory is tightened too
}

func (OSFS) Create(path string, perm fs.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm) // #nosec G304 -- staging paths built by selfupdate
}

func (OSFS) CreateExclusive(path string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) // #nosec G304 -- staging lock file
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (OSFS) Open(path string) (io.ReadCloser, error) { return os.Open(path) }     // #nosec G304 -- staging paths
func (OSFS) ReadFile(path string) ([]byte, error)    { return os.ReadFile(path) } // #nosec G304 -- staging paths

// WriteFileAtomic writes a temporary file in the same directory, syncs it
// and renames it over path.
func (OSFS) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm) // #nosec G304 -- staging paths
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (OSFS) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

func (OSFS) Stat(path string) (selfupdate.FileInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return selfupdate.FileInfo{}, err
	}
	if !st.Mode().IsRegular() {
		return selfupdate.FileInfo{}, fmt.Errorf("upgrader: %s is not a regular file", path)
	}
	return selfupdate.FileInfo{Mode: st.Mode(), Size: st.Size(), ModTime: st.ModTime(), OwnerRoot: ownedByAdmin(path, st)}, nil
}

func (OSFS) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}

// CopyFile copies src to dst (mode perm), syncing the result.
func (OSFS) CopyFile(src, dst string, perm fs.FileMode) error {
	return copyFile(src, dst, perm)
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src) // #nosec G304 -- the running executable or a staged artifact
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm) // #nosec G304 -- staging or install paths
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

func (OSFS) Free(path string) (uint64, error) { return freeBytes(path) }

// swapBinary replaces target with newBinary atomically: the new file is
// copied next to target (same file system), target is renamed to .prev and
// the new file renamed into place.
func swapBinary(newBinary, target string) (string, error) {
	next, prev := target+".new", target+".prev"
	if err := copyFile(newBinary, next, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(target, prev); err != nil {
		_ = os.Remove(next)
		return "", err
	}
	if err := os.Rename(next, target); err != nil {
		_ = os.Rename(prev, target)
		return "", err
	}
	return prev, nil
}

// restoreBinary puts previous back in place of target; a previous copy
// outside target's directory (a package install's backup) is copied first.
func restoreBinary(previous, target string) error {
	if filepath.Dir(previous) != filepath.Dir(target) {
		next := target + ".restore"
		if err := copyFile(previous, next, 0o755); err != nil {
			return err
		}
		previous = next
	}
	return os.Rename(previous, target)
}

// req8 is the unit-name suffix of a request id.
func req8(requestID string) string {
	id := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(requestID))
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	if id == "" {
		id = "manual"
	}
	return id
}
