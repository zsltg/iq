//go:build darwin || freebsd || netbsd

package file

import (
	"os"
	"syscall"
)

// changeIdentity returns the inode change time in nanoseconds and the inode
// number of a file. It returns zeros when the platform data is not available.
// These systems name the field Ctimespec. Linux and OpenBSD name it Ctim.
func changeIdentity(fi os.FileInfo) (ctime int64, ino uint64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return int64(st.Ctimespec.Sec)*1e9 + int64(st.Ctimespec.Nsec), uint64(st.Ino)
}
