//go:build unix

package source

import (
	"os"
	"syscall"
)

// fileKey returns the device and inode that identify fi's file on disk.
func fileKey(fi os.FileInfo) (any, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, false
	}
	return [2]uint64{uint64(st.Dev), uint64(st.Ino)}, true
}
