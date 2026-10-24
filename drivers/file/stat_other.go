//go:build !linux && !darwin && !freebsd && !netbsd

package file

import "os"

// changeIdentity returns zeros on a platform without a stable change time and
// inode number (Windows) or without a known Stat_t layout (for example OpenBSD).
// The cache key then rests on path, size, and mtime.
func changeIdentity(os.FileInfo) (ctime int64, ino uint64) {
	return 0, 0
}
