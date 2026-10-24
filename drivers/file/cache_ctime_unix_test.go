//go:build linux || darwin || freebsd || netbsd

package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestChangeIdentityReportsCtimeAndInode checks that the helper reads a real
// change time and inode number from a file on Unix.
func TestChangeIdentityReportsCtimeAndInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	ctime, ino := changeIdentity(fi)
	require.NotZero(t, ctime)
	require.NotZero(t, ino)
}

// TestCacheMissesOnRewriteThatKeepsSizeAndMtime rewrites a dump with different
// content of the same size and restores the old mtime. A user can do this with
// cp -p, tar -x, rsync -t, or touch -d. The change time still moves, so the cache
// must miss and the query must return the new content.
func TestCacheMissesOnRewriteThatKeepsSizeAndMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("first"))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	url := URL(path)
	dir := t.TempDir()

	w, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.Equal(t, "first", collect(t, w)["s"].Value)
	require.Len(t, cacheFiles(t, dir), 1)

	writeRDBWithString(t, path, "s", []byte("other")) // same length as "first".
	require.NoError(t, os.Chtimes(path, fi.ModTime(), fi.ModTime()))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, fi.Size(), after.Size())
	require.Equal(t, fi.ModTime().UnixNano(), after.ModTime().UnixNano())

	w2, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.Equal(t, "other", collect(t, w2)["s"].Value, "must not serve stale content")
	require.Len(t, cacheFiles(t, dir), 2, "a fresh cache is written under a new key")
}
