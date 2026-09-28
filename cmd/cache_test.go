package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{4 << 20, "4.0 MiB"},
		{1 << 30, "1.0 GiB"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, humanBytes(tt.n))
	}
}

// seedCache populates one cache file for a small RDB-shaped dump in dir and
// returns the dump path, so a cache-command test has something to list and clear.
func seedCache(t *testing.T, cacheDir string) string {
	t.Helper()
	// A typed-JSONL dump is the simplest to hand-write and caches like any other.
	dump := filepath.Join(t.TempDir(), "dump.jsonl")
	require.NoError(t, os.WriteFile(dump, []byte("{\"key\":\"a\",\"type\":\"string\",\"value\":\"x\"}\n"), 0o600))
	st, err := iqfile.Open(iqfile.URL(dump)+"?format=jsonl", numfmt.DecimalAuto,
		iqfile.CacheConfig{Dir: cacheDir, Enabled: true, MinSize: 1})
	require.NoError(t, err)
	require.NoError(t, st.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	return dump
}

func TestCacheStatTable(t *testing.T) {
	dir := t.TempDir()
	dump := seedCache(t, dir)

	var buf bytes.Buffer
	require.NoError(t, cacheStat(&buf, dir, false, false))
	out := buf.String()
	require.Contains(t, out, "DUMP")
	require.Contains(t, out, dump)
	require.Contains(t, out, "TOTAL")
}

func TestCacheStatJSON(t *testing.T) {
	dir := t.TempDir()
	dump := seedCache(t, dir)

	var buf bytes.Buffer
	require.NoError(t, cacheStat(&buf, dir, true, false))
	require.Contains(t, buf.String(), "\"dump\"")
	quoted, err := json.Marshal(dump) // the path as JSON encodes it (backslashes escaped on Windows).
	require.NoError(t, err)
	require.Contains(t, buf.String(), string(quoted))
}

func TestCacheStatJSONFields(t *testing.T) {
	dir := t.TempDir()
	dump := seedCache(t, dir)

	var buf bytes.Buffer
	require.NoError(t, cacheStat(&buf, dir, true, false))

	var rows []cacheStatRow
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rows))
	require.Len(t, rows, 1)
	// Every column carries a value: a dropped field would serialize as an empty
	// string or a zero size and still parse.
	require.Equal(t, dump, rows[0].Dump)
	require.Positive(t, rows[0].DumpBytes)
	require.NotEmpty(t, rows[0].CacheFile)
	require.Positive(t, rows[0].CacheByte)
}

// TestCacheStatYAML pins the second half of the structured gate: -y takes the
// same path as -j and emits YAML, not the table.
func TestCacheStatYAML(t *testing.T) {
	dir := t.TempDir()
	dump := seedCache(t, dir)

	var buf bytes.Buffer
	require.NoError(t, cacheStat(&buf, dir, false, true))
	out := buf.String()
	require.Contains(t, out, dump)
	require.NotContains(t, out, "DUMP SIZE", "yaml output is not the table")
	require.NotContains(t, out, "TOTAL", "yaml output is not the table")
}

// TestCacheStatTotalsEveryEntry asserts the TOTAL row is the sum over the
// entries, not the last one and not zero.
func TestCacheStatTotalsEveryEntry(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir)
	seedCache(t, dir)

	entries, err := iqfile.ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var want int64
	for _, e := range entries {
		want += e.CacheSize
	}
	require.Greater(t, want, entries[0].CacheSize, "two entries total more than one")

	var buf bytes.Buffer
	require.NoError(t, cacheStat(&buf, dir, false, false))
	total := lineWith(t, buf.String(), "TOTAL")
	require.Contains(t, total, humanBytes(want))
	// The TOTAL row holds only the label and the size, so a minus sign there can
	// only come from a subtracted total, which "contains" the same digits.
	require.NotContains(t, total, "-", "the total adds the entries")
}

// TestCacheStatReportsAListFailure surfaces an unreadable cache dir as an error
// rather than an empty listing that reads as a clean, empty cache.
func TestCacheStatReportsAListFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
	}
	dir := t.TempDir()
	seedCache(t, dir)
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var buf bytes.Buffer
	require.ErrorContains(t, cacheStat(&buf, dir, false, false), "read cache dir")
	require.Zero(t, buf.Len(), "nothing is rendered when the listing failed")
}

func TestCacheStatEmpty(t *testing.T) {
	// A dir with nothing cached renders a header row and no total, no error.
	var buf bytes.Buffer
	require.NoError(t, cacheStat(&buf, t.TempDir(), false, false))
	require.Contains(t, buf.String(), "DUMP")
	require.NotContains(t, buf.String(), "TOTAL")
}

func TestCacheClearRemovesEntries(t *testing.T) {
	dir := t.TempDir()
	dump := seedCache(t, dir)
	require.Len(t, cacheFilesIn(t, dir), 1)

	// Clear by path removes just that entry.
	n, err := iqfile.RemoveCache(dir, dump)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, cacheFilesIn(t, dir))
}

func TestDumpPathArg(t *testing.T) {
	// A non-source argument is returned as-is (treated as a filesystem path).
	require.Equal(t, "/some/dump.rdb", dumpPathArg("/some/dump.rdb"))
}

func TestFileCacheConfig(t *testing.T) {
	// --no-cache disables regardless of a locatable cache dir.
	require.Equal(t, iqfile.CacheConfig{}, (&config{noCache: true}).fileCacheConfig())

	if cacheDir() == "" {
		require.False(t, (&config{}).fileCacheConfig().Enabled)
		return
	}
	// Default: enabled with the per-page index on.
	def := (&config{}).fileCacheConfig()
	require.True(t, def.Enabled)
	require.True(t, def.Index)
	require.NotEmpty(t, def.Dir)

	// --no-cache-index keeps caching on but drops the index.
	noIdx := (&config{noCacheIndex: true}).fileCacheConfig()
	require.True(t, noIdx.Enabled)
	require.False(t, noIdx.Index)
}

// cacheFilesIn lists the .cbor cache files in dir.
func cacheFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, d := range des {
		if strings.HasSuffix(d.Name(), ".cbor") {
			out = append(out, d.Name())
		}
	}
	return out
}
