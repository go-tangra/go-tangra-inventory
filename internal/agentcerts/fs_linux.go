//go:build linux

package agentcerts

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// NewOS builds the Linux store: the configured directory is created when
// missing (it must not be a symlink) and opened as an os.Root, so no store
// path can leave it. Deps fields left nil get the OS implementations.
func NewOS(cfg Config, d Deps) (*Store, error) {
	if d.FS == nil {
		f, err := OpenFS(cfg.Dir)
		if err != nil {
			return nil, err
		}
		d.FS = f
	}
	if d.Accounts == nil {
		d.Accounts = OSAccounts{}
	}
	if d.Hooks == nil {
		d.Hooks = ExecRunner{}
	}
	return New(cfg, d), nil
}

// osFS is FS over an os.Root of the store directory.
type osFS struct {
	dir  string
	root *os.Root
}

// OpenFS opens (creating it with mode 0700 when missing) the store
// directory. The directory itself must not be a symlink.
func OpenFS(dir string) (FS, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return nil, fmt.Errorf("agentcerts: create parent of %s: %w", dir, err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("agentcerts: create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("agentcerts: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("agentcerts: %s is not a directory: %w", dir, errUnsafe)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("agentcerts: open %s: %w", dir, err)
	}
	return &osFS{dir: dir, root: r}, nil
}

func infoOf(fi fs.FileInfo) FileInfo {
	out := FileInfo{Mode: fi.Mode(), Size: fi.Size(), UID: -1, GID: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		out.UID, out.GID = int(st.Uid), int(st.Gid)
	}
	return out
}

func (f *osFS) Lstat(rel string) (FileInfo, error) {
	fi, err := f.root.Lstat(rel)
	if err != nil {
		return FileInfo{}, err
	}
	return infoOf(fi), nil
}

func (f *osFS) Mkdir(rel string, perm fs.FileMode) error { return f.root.Mkdir(rel, perm) }

func (f *osFS) WriteFile(rel string, data []byte, perm fs.FileMode, uid, gid int) error {
	fh, err := f.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err := writeSync(fh, data, perm, uid, gid); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

// writeSync sets mode and owner before writing (a key is never readable with
// a wider mode), writes and fsyncs.
func writeSync(fh *os.File, data []byte, perm fs.FileMode, uid, gid int) error {
	if err := fh.Chmod(perm); err != nil {
		return err
	}
	if err := fh.Chown(uid, gid); err != nil {
		return err
	}
	if _, err := fh.Write(data); err != nil {
		return err
	}
	return fh.Sync()
}

func (f *osFS) ReadFile(rel string, max int64) ([]byte, error) {
	fh, err := f.root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("agentcerts: %s is not a regular file", rel)
	}
	data, err := io.ReadAll(io.LimitReader(fh, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("agentcerts: %s exceeds %d bytes", rel, max)
	}
	return data, nil
}

func (f *osFS) Chmod(rel string, perm fs.FileMode) error {
	fi, err := f.root.Lstat(rel)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("agentcerts: chmod of symlink %s refused", rel)
	}
	return f.root.Chmod(rel, perm)
}

func (f *osFS) Lchown(rel string, uid, gid int) error { return f.root.Lchown(rel, uid, gid) }

func (f *osFS) Symlink(target, rel string) error { return f.root.Symlink(target, rel) }

func (f *osFS) Readlink(rel string) (string, error) { return f.root.Readlink(rel) }

func (f *osFS) Rename(oldRel, newRel string) error { return f.root.Rename(oldRel, newRel) }

func (f *osFS) RemoveAll(rel string) error { return f.root.RemoveAll(rel) }

func (f *osFS) ReadDir(rel string) ([]string, error) {
	d, err := f.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.Readdirnames(-1)
}

func (f *osFS) SyncDir(rel string) error {
	d, err := f.root.Open(rel)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (f *osFS) Free() (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(f.dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil // #nosec G115 -- block size is positive
}

// OSAccounts resolves names with os/user (files and NSS).
type OSAccounts struct{}

// LookupUser returns the uid of a user name.
func (OSAccounts) LookupUser(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

// LookupGroup returns the gid of a group name.
func (OSAccounts) LookupGroup(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}
