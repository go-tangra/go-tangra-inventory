//go:build !windows

package upgrader

import (
	"os"
	"syscall"
)

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
