package agentcerts

import (
	"context"
	"errors"
	"io/fs"
	"time"
)

// ErrUnsupported is returned by NewOS on platforms without a certificate
// store (everything but Linux; research D15). Such agents never announce
// cert.v1, so the inventory marks their items unsupported.
var ErrUnsupported = errors.New("agentcerts: certificate delivery is supported on Linux only")

// FileInfo is what the store needs to know about a directory entry. It
// always describes the entry itself (symlinks are not followed).
type FileInfo struct {
	Mode fs.FileMode // type bits and permissions
	UID  int
	GID  int
	Size int64
}

// IsDir reports a directory (never a symlink to one).
func (i FileInfo) IsDir() bool { return i.Mode.IsDir() }

// IsSymlink reports a symbolic link.
func (i FileInfo) IsSymlink() bool { return i.Mode&fs.ModeSymlink != 0 }

// IsRegular reports a regular file (never a symlink to one).
func (i FileInfo) IsRegular() bool { return i.Mode.IsRegular() }

// FS is the store's filesystem, confined to the configured directory: every
// path is relative to it, slash-separated and never contains "..". An
// implementation must not let a path escape the directory (the Linux one is
// built on os.Root) and must never follow a symlink in the final component
// of Lstat, ReadFile, WriteFile, Chmod or Lchown.
type FS interface {
	// Lstat describes rel without following a final symlink.
	Lstat(rel string) (FileInfo, error)
	// Mkdir creates the directory rel (the caller sets mode and owner).
	Mkdir(rel string, perm fs.FileMode) error
	// WriteFile creates rel exclusively (O_CREAT|O_EXCL|O_NOFOLLOW), writes
	// data, sets perm and owner on the open file and fsyncs it.
	WriteFile(rel string, data []byte, perm fs.FileMode, uid, gid int) error
	// ReadFile reads a regular file of at most max bytes (a symlink or a
	// larger file is an error).
	ReadFile(rel string, max int64) ([]byte, error)
	// Chmod sets the permissions of rel (not a symlink).
	Chmod(rel string, perm fs.FileMode) error
	// Lchown sets the owner of rel itself.
	Lchown(rel string, uid, gid int) error
	// Symlink creates rel as a symlink with the given (relative) target.
	Symlink(target, rel string) error
	// Readlink returns the target of the symlink rel.
	Readlink(rel string) (string, error)
	// Rename atomically renames oldRel to newRel (rename(2)).
	Rename(oldRel, newRel string) error
	// RemoveAll removes rel and everything below it (no error if absent).
	RemoveAll(rel string) error
	// ReadDir lists the entry names of the directory rel.
	ReadDir(rel string) ([]string, error)
	// SyncDir fsyncs the directory rel.
	SyncDir(rel string) error
	// Free returns the bytes available to the agent on the store's filesystem.
	Free() (uint64, error)
}

// Accounts resolves the configured owner and group names (on every install,
// research D10).
type Accounts interface {
	LookupUser(name string) (int, error)
	LookupGroup(name string) (int, error)
}

// HookSpec is one deploy hook run: the file itself is executed (no shell, no
// arguments) in Dir with exactly Env.
type HookSpec struct {
	Path    string
	Dir     string
	Env     []string
	Timeout time.Duration
}

// HookOutcome is the result of a hook run. Output is at most
// MaxHookOutput bytes of combined stdout and stderr (local log only). Err is
// set when the hook could not be started.
type HookOutcome struct {
	ExitCode int
	TimedOut bool
	Output   []byte
	Err      error
}

// MaxHookOutput bounds the hook output kept for the local log.
const MaxHookOutput = 4 << 10

// HookRunner inspects and runs the deploy hook (absolute host paths, outside
// the store).
type HookRunner interface {
	// Lstat describes path without following a final symlink.
	Lstat(path string) (FileInfo, error)
	// Stat describes path, following symlinks (the hook's directory may be
	// reached through a merged-/usr link such as /sbin).
	Stat(path string) (FileInfo, error)
	// Run executes the hook in a new process group; on timeout the whole
	// group gets SIGTERM and, after a grace period, SIGKILL.
	Run(ctx context.Context, spec HookSpec) HookOutcome
}
