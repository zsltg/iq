package file

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseFileURLReadsEveryHint gives each query field its own value, so a field read
// from the wrong name, or not read at all, shows in the result.
func TestParseFileURLReadsEveryHint(t *testing.T) {
	_, _, hints, err := parseFileURL("file:///d.x?types=t&keys=k&columns=c&label=l&rel=r&key=y")
	require.NoError(t, err)
	require.Equal(t, Hints{Types: "t", Keys: "k", Columns: "c", Label: "l", Rel: "r", Key: "y"}, hints)
}

// TestParseFileURLKeepsTheHintsOfAnOpaqueURL reads the hints of the file:relative form,
// where the path comes from the opaque part and not from the path.
func TestParseFileURLKeepsTheHintsOfAnOpaqueURL(t *testing.T) {
	p, _, hints, err := parseFileURL("file:sub/d.csv?columns=a,b")
	require.NoError(t, err)
	require.Equal(t, "sub/d.csv", p)
	require.Equal(t, "a,b", hints.Columns)
}

// TestRemoveCacheKeepsTheRawPathWhenItCannotBeMadeAbsolute removes the working
// directory, so filepath.Abs fails for a relative dump path. The raw path is then
// the path to match, and only the entry that names it goes.
func TestRemoveCacheKeepsTheRawPathWhenItCannotBeMadeAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot remove the working directory")
	}
	dir := t.TempDir()
	plantNamedCache(t, dir, "a.cbor", "rel.rdb")
	plantNamedCache(t, dir, "b.cbor", filepath.Join(string(filepath.Separator), "other", "rel.rdb"))
	gone := t.TempDir()
	t.Chdir(gone)
	require.NoError(t, os.Remove(gone))
	if _, err := os.Getwd(); err == nil {
		t.Skip("this platform still reports a removed working directory")
	}

	n, err := RemoveCache(dir, "rel.rdb")

	require.NoError(t, err)
	require.Equal(t, 1, n)
	entries, err := ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "b.cbor", entries[0].File)
}
