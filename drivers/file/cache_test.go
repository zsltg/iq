package file

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zsltg/rdb/encoder"
	"github.com/zsltg/rdb/model"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// cacheCfg is an indexed cache policy with a 1-byte floor, so any test dump is
// cached (with a per-page index) without building a multi-megabyte file.
func cacheCfg(dir string) CacheConfig {
	return CacheConfig{Dir: dir, Enabled: true, Index: true, MinSize: 1}
}

// flatCfg is cacheCfg without the per-page index, exercising the flat-cache path.
func flatCfg(dir string) CacheConfig {
	return CacheConfig{Dir: dir, Enabled: true, Index: false, MinSize: 1}
}

// cacheFiles lists the cache files in dir, or nil if it does not exist yet.
func cacheFiles(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, d := range des {
		if filepath.Ext(d.Name()) == ".cbor" {
			out = append(out, d.Name())
		}
	}
	return out
}

// corruptBody returns a same-length copy of an RDB dump with its body zeroed but
// its leading signature ("REDIS" + version) intact, so format detection still
// classifies it as RDB while any attempt to decode its contents fails.
func corruptBody(content []byte) []byte {
	out := make([]byte, len(content))
	const sig = 9 // "REDIS" magic (5) + 4-byte version.
	if len(content) >= sig {
		copy(out, content[:sig])
	}
	return out
}

// writeRDBWithString builds an RDB holding one string key, so a test can vary its
// value (including binary bytes) and its size.
func writeRDBWithString(t *testing.T, path, key string, value []byte) {
	t.Helper()
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, 1, 0))
	require.NoError(t, enc.WriteStringObject(key, value))
	require.NoError(t, enc.WriteEnd())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

// buildBigRDB builds an RDB of n string keys k0..k{n-1} in insertion order, so it
// spans multiple cache pages (page = pageSize records).
func buildBigRDB(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, uint64(n), 0))
	for i := range n {
		require.NoError(t, enc.WriteStringObject(fmt.Sprintf("k%d", i), fmt.Appendf(nil, "v%d", i)))
	}
	require.NoError(t, enc.WriteEnd())
	return buf.Bytes()
}

// TestCacheIndexedGet confirms a warm indexed Get is handled by the index path
// (ok=true), returns the right values, and omits a missing key.
func TestCacheIndexedGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.rdb")
	require.NoError(t, os.WriteFile(path, buildBigRDB(t, bigCount), 0o600))
	url := URL(path)
	dir := t.TempDir()

	warm, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.NoError(t, warm.TypedScan(context.Background(), func([]query.Record) error { return nil }))

	st, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	out, ok := st.cacheGet(context.Background(), []string{"k7", "k599", "nope"})
	require.True(t, ok)
	require.Equal(t, "v7", out["k7"])
	require.Equal(t, "v599", out["k599"])
	require.NotContains(t, out, "nope", "a missing key is absent from the map")
}

// TestCacheGetMatchesStreamedGet pins the two bounded-read paths against each
// other. The indexed cache and the streaming pass build their result maps
// independently, so a presence rule applied to one and forgotten in the other
// would make the same query answer differently depending on whether a cache
// happened to be warm.
func TestCacheGetMatchesStreamedGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.rdb")
	require.NoError(t, os.WriteFile(path, buildBigRDB(t, bigCount), 0o600))
	url := URL(path)
	dir := t.TempDir()
	keys := []string{"k7", "k599", "nope"}

	// Cold store, cache disabled: the streaming path answers.
	cold, err := Open(url, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	streamed, err := cold.Get(context.Background(), keys)
	require.NoError(t, err)

	// Warm the index, then read the same keys through the cache path.
	warm, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.NoError(t, warm.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	st, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	cached, ok := st.cacheGet(context.Background(), keys)
	require.True(t, ok, "the warm index must serve this read")

	require.Equal(t, streamed, cached)
	require.NotContains(t, streamed, "nope")
}

// TestCacheFlatModeFallsBack confirms flat mode writes a cache with no usable
// index, so cacheGet declines and Get resolves via the streaming fallback.
func TestCacheFlatModeFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.rdb")
	require.NoError(t, os.WriteFile(path, buildBigRDB(t, bigCount), 0o600))
	url := URL(path)
	dir := t.TempDir()

	warm, err := Open(url, numfmt.DecimalAuto, flatCfg(dir))
	require.NoError(t, err)
	require.NoError(t, warm.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	require.Len(t, cacheFiles(t, dir), 1)

	st, err := Open(url, numfmt.DecimalAuto, flatCfg(dir))
	require.NoError(t, err)
	_, ok := st.cacheGet(context.Background(), []string{"k7"})
	require.False(t, ok, "a flat cache carries no index, so cacheGet must decline")

	got, err := st.Get(context.Background(), []string{"k7"})
	require.NoError(t, err)
	require.Equal(t, "v7", got["k7"])
}

// TestCacheGetSkipsNonCandidatePages is the behavioral proof the index prunes: a
// page that cannot hold the wanted key is corrupted on disk, yet the Get still
// succeeds — which is only possible if that page was never decoded. If pruning
// failed, the streaming fallback would hit the corrupt bytes and error.
func TestCacheGetSkipsNonCandidatePages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.rdb")
	require.NoError(t, os.WriteFile(path, buildBigRDB(t, bigCount), 0o600))
	url := URL(path)
	dir := t.TempDir()

	warm, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.NoError(t, warm.TypedScan(context.Background(), func([]query.Record) error { return nil }))

	st, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	m, ok := st.cacheable()
	require.True(t, ok)
	cf := st.cacheFile(m)

	// Read the index and find a page whose filter does not hold "k0" (deterministic
	// — we inspect the real filters, so no false-positive flakiness).
	f, err := os.Open(cf)
	require.NoError(t, err)
	size, err := fileSize(f)
	require.NoError(t, err)
	indexOffset, err := readTrailer(f, size)
	require.NoError(t, err)
	idx, err := readIndex(f, indexOffset, size)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Greater(t, len(idx.Pages), 1, "need a multi-page cache")

	target := -1
	for i, p := range idx.Pages {
		if !bloomHas(p.Filter, "k0") {
			target = i
			break
		}
	}
	require.GreaterOrEqual(t, target, 0, "expected a page that does not hold k0")
	end := indexOffset
	if target+1 < len(idx.Pages) {
		end = idx.Pages[target+1].Offset
	}
	corruptFileRange(t, cf, idx.Pages[target].Offset, end)

	got, err := st.Get(context.Background(), []string{"k0"})
	require.NoError(t, err, "the corrupt non-candidate page must have been skipped")
	require.Equal(t, "v0", got["k0"])
}

// corruptFileRange overwrites [off, end) of a file with zero bytes (invalid as a
// record), keeping its length so offsets and the trailer stay valid.
func corruptFileRange(t *testing.T, path string, off, end int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	_, err = f.WriteAt(make([]byte, end-off), off)
	require.NoError(t, err)
}

// TestCacheDifferential proves a warm-cache decode yields the exact same records
// as a cold decode, for every dump format — the drift backstop. Each case is
// decoded cold (no cache), then populated and re-read warm, and the two record
// sets must be deep-equal, types included.
func TestCacheDifferential(t *testing.T) {
	binVal := string([]byte{0xff, 0xfe, 0x00, 0x41}) // non-UTF-8 Redis string.
	tests := []struct {
		name    string
		file    string
		q       string
		content []byte
	}{
		{"rdb", "d.rdb", "", buildRDB(t)},
		{"rdb-binary", "b.rdb", "", func() []byte {
			var buf bytes.Buffer
			enc := encoder.NewEncoder(&buf)
			require.NoError(t, enc.WriteHeader())
			require.NoError(t, enc.WriteDBHeader(0, 2, 0))
			require.NoError(t, enc.WriteStringObject("bin", []byte(binVal)))
			require.NoError(t, enc.WriteZSetObject("z", []*model.ZSetEntry{{Member: "m", Score: 1.5}}))
			require.NoError(t, enc.WriteEnd())
			return buf.Bytes()
		}()},
		{"bson", "d.bson", "format=bson", func() []byte {
			var data []byte
			data = append(data, mustBSON(t, bson.M{"_id": "a", "n": int64(3), "f": 2.5})...)
			data = append(data, mustBSON(t, bson.M{"_id": "b", "arr": bson.A{int32(1), "x"}})...)
			return data
		}()},
		{"mongoexport", "d.json", "format=mongoexport", []byte("{\"_id\":\"a\",\"v\":1}\n{\"_id\":\"b\",\"v\":2}\n")},
		{"typed-jsonl", "d.jsonl", "format=jsonl", []byte("{\"key\":\"a\",\"type\":\"string\",\"value\":\"x\"}\n")},
		{"yaml", "d.yaml", "", []byte("key: a\ntype: string\nvalue: y\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.file)
			require.NoError(t, os.WriteFile(path, tt.content, 0o600))
			url := URL(path)
			if tt.q != "" {
				url += "?" + tt.q
			}

			cold, err := Open(url, numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			want := collect(t, cold)

			dir := t.TempDir()
			warm, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
			require.NoError(t, err)
			require.Equal(t, want, collect(t, warm)) // populates.
			require.Len(t, cacheFiles(t, dir), 1)

			reread, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
			require.NoError(t, err)
			require.Equal(t, want, collect(t, reread)) // served from cache.
		})
	}
}

// TestCacheServedFromCacheFile proves the warm path actually reads the cache and
// not the original: after populating, the original dump is overwritten with
// garbage of the same length and its mtime restored, so the cache key still
// matches. Decoding the garbage would fail; returning the original records proves
// the cache file was used.
func TestCacheServedFromCacheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	url := URL(path)
	dir := t.TempDir()

	w, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	want := collect(t, w)

	// Corrupt the body but keep the RDB signature (so Open still detects the
	// format) and the length and mtime (so the cache key still matches). Decoding
	// this would fail; returning the original records proves the cache was read.
	require.NoError(t, os.WriteFile(path, corruptBody(content), 0o600))
	require.NoError(t, os.Chtimes(path, fi.ModTime(), fi.ModTime()))

	w2, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.Equal(t, want, collect(t, w2))
	require.Equal(t, "hello", collect(t, w2)["s"].Value)
}

// TestCacheGetDoesNotPopulate confirms a bounded Get never writes a cache — its
// early stop would persist a partial dump — while a full scan does.
func TestCacheGetDoesNotPopulate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	url := URL(path)
	dir := t.TempDir()

	st, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)

	got, err := st.Get(context.Background(), []string{"missing"})
	require.NoError(t, err)
	require.NotContains(t, got, "missing", "a missing key is absent from the map")
	require.Empty(t, cacheFiles(t, dir), "Get must not populate the cache")

	found, err := st.Get(context.Background(), []string{"s"})
	require.NoError(t, err)
	require.Equal(t, "hello", found["s"])
	require.Empty(t, cacheFiles(t, dir), "even a hitting Get must not populate")

	require.NoError(t, st.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	require.Len(t, cacheFiles(t, dir), 1, "a full scan populates")
}

// TestCacheWarmGet confirms a Get is served from a warm cache and still filters
// to the requested keys.
func TestCacheWarmGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	url := URL(path)
	dir := t.TempDir()

	warm, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.NoError(t, warm.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	require.Len(t, cacheFiles(t, dir), 1)

	// Corrupt the original, same length and mtime, so a Get that decoded it would
	// fail; a correct value proves the Get read the cache.
	fi, err := os.Stat(path)
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, corruptBody(content), 0o600))
	require.NoError(t, os.Chtimes(path, fi.ModTime(), fi.ModTime()))

	st, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	got, err := st.Get(context.Background(), []string{"s"})
	require.NoError(t, err)
	require.Equal(t, "hello", got["s"])
}

// TestCacheInvalidatesOnChange confirms an edited dump is never served from a
// stale cache: after the file changes size, a query reflects the new content and
// a fresh cache is written alongside the now-orphaned one.
func TestCacheInvalidatesOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("first"))
	url := URL(path)
	dir := t.TempDir()

	w, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	_ = collect(t, w)
	require.Len(t, cacheFiles(t, dir), 1)

	// Rewrite with a longer value (different size), invalidating the cache key.
	writeRDBWithString(t, path, "s", []byte("second-and-longer"))
	w2, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	got := collect(t, w2)
	require.Equal(t, "second-and-longer", got["s"].Value, "must not serve stale content")
	require.Len(t, cacheFiles(t, dir), 2, "a fresh cache is written under a new key")
}

// TestCacheCorruptFallsBack confirms a cache file that is not a valid iq cache
// (wrong magic) is ignored: the query decodes the original rather than failing.
func TestCacheCorruptFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	url := URL(path)
	dir := t.TempDir()

	st, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	m, ok := st.cacheable()
	require.True(t, ok)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(st.cacheFile(m), []byte("not an iq cache file at all"), 0o600))

	require.Equal(t, "hello", collect(t, st)["s"].Value)
}

// TestCacheableGuards checks the conditions under which a store refuses to cache:
// stdin (no stable path), a disabled policy, no cache dir, and a dump below the
// size floor.
func TestCacheableGuards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	fi, err := os.Stat(path)
	require.NoError(t, err)

	t.Run("stdin never caches", func(t *testing.T) {
		s := &Store{data: []byte("x"), cache: CacheConfig{Dir: dir, Enabled: true, MinSize: 1}}
		_, ok := s.cacheable()
		require.False(t, ok)
	})
	t.Run("disabled", func(t *testing.T) {
		s := &Store{path: path, cache: CacheConfig{Dir: dir, Enabled: false, MinSize: 1}}
		_, ok := s.cacheable()
		require.False(t, ok)
	})
	t.Run("no dir", func(t *testing.T) {
		s := &Store{path: path, cache: CacheConfig{Enabled: true, MinSize: 1}}
		_, ok := s.cacheable()
		require.False(t, ok)
	})
	t.Run("below floor", func(t *testing.T) {
		s := &Store{path: path, cache: CacheConfig{Dir: dir, Enabled: true, MinSize: fi.Size() + 1}}
		_, ok := s.cacheable()
		require.False(t, ok)
	})
	t.Run("cacheable", func(t *testing.T) {
		s := &Store{path: path, cache: cacheCfg(dir)}
		_, ok := s.cacheable()
		require.True(t, ok)
	})
}

// TestListAndRemoveCache exercises the stat/clear API cmd builds on.
func TestListAndRemoveCache(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(t.TempDir(), "one.rdb")
	p2 := filepath.Join(t.TempDir(), "two.rdb")
	writeRDBWithString(t, p1, "s", []byte("one"))
	writeRDBWithString(t, p2, "s", []byte("two"))
	for _, p := range []string{p1, p2} {
		st, err := Open(URL(p), numfmt.DecimalAuto, cacheCfg(dir))
		require.NoError(t, err)
		_ = collect(t, st)
	}

	entries, err := ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, p1, entries[0].DumpPath) // sorted by dump path.
	require.Positive(t, entries[0].CacheSize)

	n, err := RemoveCache(dir, p1)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	entries, err = ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, p2, entries[0].DumpPath)

	n, err = RemoveCache(dir, "")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	entries, err = ListCache(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestListCacheSameDumpSorted covers the listing's secondary sort: after an edit
// leaves a stale and a fresh cache for the same dump path, both entries share a
// DumpPath, so they order by cache file name deterministically.
func TestListCacheSameDumpSorted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("first"))
	st1, err := Open(URL(path), numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	_ = collect(t, st1)

	writeRDBWithString(t, path, "s", []byte("second-and-longer"))
	st2, err := Open(URL(path), numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	_ = collect(t, st2)

	entries, err := ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, path, entries[0].DumpPath)
	require.Equal(t, path, entries[1].DumpPath)
	require.Less(t, entries[0].File, entries[1].File) // tie broken by file name.
}

// TestListCacheMissingDir returns no entries and no error for a dir that was
// never created (nothing cached yet).
func TestListCacheMissingDir(t *testing.T) {
	entries, err := ListCache(filepath.Join(t.TempDir(), "never"))
	require.NoError(t, err)
	require.Empty(t, entries)
	n, err := RemoveCache(filepath.Join(t.TempDir(), "never"), "")
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestCachePagesMatch confirms the warm read pages at the same boundary as a live
// scan, so a streaming consumer sees the same batch shape.
func TestCachePagesMatch(t *testing.T) {
	var b strings.Builder
	for i := range bigCount {
		fmt.Fprintf(&b, "{\"_id\":\"k%d\",\"i\":%d}\n", i, i)
	}
	path := filepath.Join(t.TempDir(), "big.json")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	url := URL(path) + "?format=mongoexport"
	dir := t.TempDir()

	warm, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, warm)) // populate.

	reread, err := Open(url, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, reread)) // from cache.
}
