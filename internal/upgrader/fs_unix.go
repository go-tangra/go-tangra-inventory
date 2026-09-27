//go:build !windows

package upgrader

import (
	"io/fs"
	"os"
	"syscall"
)

// restrictDir sets the directory's mode (an existing directory is tightened too).
func restrictDir(path string, perm fs.FileMode) error { return os.Chmod(path, perm) }

// private: no group or other permission bits.
func private(_ string, st os.FileInfo) bool { return st.Mode().Perm()&0o077 == 0 }

// ownedByAdmin: owned by root.
func ownedByAdmin(_ string, st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && s.Uid == 0
}

func freeBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil // #nosec G115 -- block size is positive
}
