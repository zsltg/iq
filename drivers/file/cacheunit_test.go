package file

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// cacheBytes assembles a whole cache file — magic, version, header, the CBOR
// records paged at pageSize, the page index, and the index-offset trailer — so a
// test can hand the readers a file whose every field it controls. junk is spliced
// into the record region after the records, standing in for a corrupted record.
func cacheBytes(t *testing.T, h cacheHeader, recs []query.Record, indexed bool, junk []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, writeHeader(&buf, h))
	var pages []pageIndex
	for i := 0; i < len(recs); i += pageSize {
		end := min(i+pageSize, len(recs))
		offset := int64(buf.Len())
		filter := newBloom(end - i)
		for _, r := range recs[i:end] {
			b, err := encodeRecord(r)
			require.NoError(t, err)
			buf.Write(b)
			bloomAdd(filter, r.Key)
		}
		if indexed {
			pages = append(pages, pageIndex{Offset: offset, Filter: filter})
		}
	}
	buf.Write(junk)
	indexOffset := int64(buf.Len())
	blk, err := cborEnc.Marshal(indexBlock{Pages: pages})
	require.NoError(t, err)
	buf.Write(blk)
	var trailer [trailerLen]byte
	binary.LittleEndian.PutUint64(trailer[:], uint64(indexOffset))
	buf.Write(trailer[:])
	return buf.Bytes()
}

// cacheFixture builds a small dump, a store configured to cache it, the meta the
// store keys that dump by, and the header a matching cache file carries.
func cacheFixture(t *testing.T) (*Store, cacheMeta, cacheHeader) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	st, err := Open(URL(path), numfmt.DecimalAuto, cacheCfg(t.TempDir()))
	require.NoError(t, err)
	m, ok := st.cacheable()
	require.True(t, ok)
	return st, m, cacheHeader{Path: m.path, Size: m.size, MTimeNano: m.mtime, Format: int(st.format), Decimal: int(st.dec)}
}

// plantCache writes assembled cache bytes at the exact path the store keys m by,
// so a later read finds a cache whose header the test chose.
func plantCache(t *testing.T, s *Store, m cacheMeta, content []byte) string {
	t.Helper()
	path := s.cacheFile(m)
	require.NoError(t, os.MkdirAll(s.cache.Dir, 0o700))
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}

// nRecords builds n distinct string records, enough to cross a page boundary.
func nRecords(n int) []query.Record {
	out := make([]query.Record, n)
	for i := range out {
		out[i] = query.Record{Key: fmt.Sprintf("k%d", i), Type: "string", Value: fmt.Sprintf("v%d", i)}
	}
	return out
}

// requireWrapped asserts an error carries its cause: every boundary in this package
// wraps with %w so a caller can still classify the failure with errors.Is/As, and a
// message-only wrap would silently break that.
func requireWrapped(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Error(t, errors.Unwrap(err), "the underlying cause must stay reachable")
}

// TestCacheMinSize pins the size floor's fallback: only a positive MinSize is a
// policy, so a zero or negative one selects the built-in default rather than
// caching every one-byte dump.
func TestCacheMinSize(t *testing.T) {
	tests := []struct {
		name string
		cfg  CacheConfig
		want int64
	}{
		{name: "zero selects the default floor", cfg: CacheConfig{}, want: defaultCacheMinSize},
		{name: "negative selects the default floor", cfg: CacheConfig{MinSize: -1}, want: defaultCacheMinSize},
		{name: "one byte is honored", cfg: CacheConfig{MinSize: 1}, want: 1},
		{name: "an explicit floor is honored", cfg: CacheConfig{MinSize: 5 << 20}, want: 5 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.cfg.minSize())
		})
	}
}

// TestCacheableRules covers every reason a dump is not cacheable and the identity
// a cacheable one reports. Each guard is exercised alone, so dropping any one of
// them either starts caching something that must never be cached or panics on the
// nil FileInfo a failed stat returns.
func TestCacheableRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	fi, err := os.Stat(path)
	require.NoError(t, err)

	tests := []struct {
		name  string
		store *Store
		want  bool
	}{
		{name: "a stdin buffer has no stable path", store: &Store{data: []byte("x"), path: path, cache: cacheCfg(dir)}, want: false},
		{name: "a disabled policy never caches", store: &Store{path: path, cache: CacheConfig{Dir: dir, MinSize: 1}}, want: false},
		{name: "an empty dir never caches", store: &Store{path: path, cache: CacheConfig{Enabled: true, MinSize: 1}}, want: false},
		{name: "an unstattable path never caches", store: &Store{path: filepath.Join(dir, "gone.rdb"), cache: cacheCfg(dir)}, want: false},
		{name: "a directory is never a dump", store: &Store{path: dir, cache: cacheCfg(dir)}, want: false},
		{name: "a dump below the floor is not cached", store: &Store{path: path, cache: CacheConfig{Dir: dir, Enabled: true, MinSize: fi.Size() + 1}}, want: false},
		{name: "a dump exactly at the floor is cached", store: &Store{path: path, cache: CacheConfig{Dir: dir, Enabled: true, MinSize: fi.Size()}}, want: true},
		{name: "a dump above the floor is cached", store: &Store{path: path, cache: cacheCfg(dir)}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := tt.store.cacheable()
			require.Equal(t, tt.want, ok)
			if !tt.want {
				require.Equal(t, cacheMeta{}, m)
				return
			}
			require.Equal(t, path, m.path)
			require.Equal(t, fi.Size(), m.size)
			require.Equal(t, fi.ModTime().UnixNano(), m.mtime)
		})
	}
}

// TestCacheSourceIgnoresAnUncacheableStore plants a cache file at the path a
// zero-identity lookup would key, so a store that must not consult the cache is
// caught reading one anyway rather than merely finding nothing.
func TestCacheSourceIgnoresAnUncacheableStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	st := &Store{path: path, format: FormatRDB, cache: CacheConfig{Dir: dir, MinSize: 1}} // disabled.
	zero := cacheMeta{}
	plantCache(t, st, zero, cacheBytes(t, cacheHeader{Format: int(st.format), Decimal: int(st.dec)}, nRecords(1), true, nil))

	_, ok := st.cacheable()
	require.False(t, ok)
	src, ok := st.cacheSource()
	require.False(t, ok, "a store that cannot be cached must never resolve a cache source")
	require.Nil(t, src)
}

// TestCacheFresh pins every field of the freshness check. A cache is fresh only
// when all four recorded facts still match, so each is perturbed alone: dropping
// or loosening any one would serve records decoded from a different dump, a
// different format, or a different decimal mode.
func TestCacheFresh(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cacheHeader)
		want   bool
	}{
		{name: "an untouched header is fresh", mutate: func(*cacheHeader) {}, want: true},
		{name: "a changed size is stale", mutate: func(h *cacheHeader) { h.Size++ }},
		{name: "a changed mtime is stale", mutate: func(h *cacheHeader) { h.MTimeNano++ }},
		{name: "a changed format is stale", mutate: func(h *cacheHeader) { h.Format++ }},
		{name: "a changed decimal mode is stale", mutate: func(h *cacheHeader) { h.Decimal++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, m, h := cacheFixture(t)
			tt.mutate(&h)
			path := plantCache(t, st, m, cacheBytes(t, h, nRecords(1), true, nil))
			require.Equal(t, tt.want, st.cacheFresh(path, m))
		})
	}
	t.Run("a missing file is not fresh", func(t *testing.T) {
		st, m, _ := cacheFixture(t)
		require.False(t, st.cacheFresh(filepath.Join(t.TempDir(), "none.cbor"), m))
	})
	t.Run("a foreign file is not fresh", func(t *testing.T) {
		st, m, _ := cacheFixture(t)
		path := plantCache(t, st, m, []byte("not a cache file at all"))
		require.False(t, st.cacheFresh(path, m))
	})
}

// TestReadCacheRejectsAMismatchedHeader covers readCache's own re-check, which
// guards a cache file swapped between the freshness probe and the read. Each
// recorded fact is perturbed alone, so no term of the check can be dropped.
func TestReadCacheRejectsAMismatchedHeader(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cacheHeader)
	}{
		{name: "a changed size is refused", mutate: func(h *cacheHeader) { h.Size++ }},
		{name: "a changed mtime is refused", mutate: func(h *cacheHeader) { h.MTimeNano++ }},
		{name: "a changed format is refused", mutate: func(h *cacheHeader) { h.Format++ }},
		{name: "a changed decimal mode is refused", mutate: func(h *cacheHeader) { h.Decimal++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, m, h := cacheFixture(t)
			tt.mutate(&h)
			path := plantCache(t, st, m, cacheBytes(t, h, nRecords(1), true, nil))
			err := st.readCache(context.Background(), path, m, func([]query.Record) error { return nil })
			require.ErrorContains(t, err, "no longer matches its dump")
		})
	}
	t.Run("a matching header streams its records", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		var got []query.Record
		require.NoError(t, st.readCache(context.Background(), path, m, func(b []query.Record) error {
			got = append(got, b...)
			return nil
		}))
		require.Equal(t, nRecords(3), got)
	})
}

// TestReadCacheStreamFailures covers the streaming loop's error and paging paths:
// a cancelled scan stops, a corrupt record is a real error rather than a silent
// zero record, a consumer error propagates, and a run that ends exactly on a page
// boundary never hands the consumer an empty page.
func TestReadCacheStreamFailures(t *testing.T) {
	t.Run("a cancelled context stops the stream", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := st.readCache(ctx, path, m, func([]query.Record) error { called = true; return nil })
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, called)
	})
	t.Run("a corrupt record is an error", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		junk, err := cborEnc.Marshal("not a record")
		require.NoError(t, err)
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(1), true, junk))
		err = st.readCache(context.Background(), path, m, func([]query.Record) error { return nil })
		require.ErrorContains(t, err, "decode cache record")
		requireWrapped(t, err)
	})
	t.Run("a consumer error propagates from a full page", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(pageSize), true, nil))
		boom := errors.New("consumer said no")
		err := st.readCache(context.Background(), path, m, func([]query.Record) error { return boom })
		require.ErrorIs(t, err, boom)
	})
	t.Run("an exact page multiple emits no trailing empty page", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(pageSize), true, nil))
		var sizes []int
		require.NoError(t, st.readCache(context.Background(), path, m, func(b []query.Record) error {
			sizes = append(sizes, len(b))
			return nil
		}))
		require.Equal(t, []int{pageSize}, sizes)
	})
	t.Run("a partial final page is emitted", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(pageSize+1), true, nil))
		var sizes []int
		require.NoError(t, st.readCache(context.Background(), path, m, func(b []query.Record) error {
			sizes = append(sizes, len(b))
			return nil
		}))
		require.Equal(t, []int{pageSize, 1}, sizes)
	})
	t.Run("a missing cache file is an open error", func(t *testing.T) {
		st, m, _ := cacheFixture(t)
		err := st.readCache(context.Background(), filepath.Join(t.TempDir(), "none.cbor"), m, func([]query.Record) error { return nil })
		require.ErrorContains(t, err, "open cache")
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
}

// errWriteBoom is the sentinel a deliberately failing writer returns.
var errWriteBoom = errors.New("write refused")

// failAfter accepts n writes and then fails, so a test can aim a write failure at
// one step of the header without a full filesystem.
type failAfter struct{ n int }

func (w *failAfter) Write(b []byte) (int, error) {
	if w.n == 0 {
		return 0, errWriteBoom
	}
	w.n--
	return len(b), nil
}

// TestWriteHeaderReportsEachWriteFailure walks the header's three writes and fails
// each in turn, so every one of them keeps its own guard and its own message: a
// dropped guard would install a cache whose header was never written.
func TestWriteHeaderReportsEachWriteFailure(t *testing.T) {
	tests := []struct {
		name string
		ok   int
		want string
	}{
		{name: "magic", ok: 0, want: "write cache magic"},
		{name: "version", ok: 1, want: "write cache version"},
		{name: "header", ok: 2, want: "write cache header"},
	}
	for _, tt := range tests {
		t.Run(tt.name+" write failure is reported", func(t *testing.T) {
			err := writeHeader(&failAfter{n: tt.ok}, cacheHeader{Path: "p", Size: 1})
			require.ErrorIs(t, err, errWriteBoom)
			require.ErrorContains(t, err, tt.want)
		})
	}
	t.Run("a writer that accepts everything succeeds", func(t *testing.T) {
		require.NoError(t, writeHeader(&failAfter{n: 3}, cacheHeader{Path: "p", Size: 1}))
	})
}

// TestReadHeader covers each way a file fails to be a current-version iq cache, so
// a truncated, foreign, or old-layout file is a miss with its own message rather
// than a decoder fed arbitrary bytes.
func TestReadHeader(t *testing.T) {
	good := cacheBytes(t, cacheHeader{Path: "p", Size: 7, MTimeNano: 9, Format: 3, Decimal: 1}, nRecords(1), false, nil)
	badVersion := bytes.Clone(good)
	binary.LittleEndian.PutUint32(badVersion[8:12], cacheVersion+1)

	tests := []struct {
		name    string
		in      []byte
		want    string
		wrapped bool
	}{
		{name: "a truncated magic is a read failure", in: good[:3], want: "read cache magic", wrapped: true},
		{name: "a foreign magic is refused", in: append(bytes.Repeat([]byte("X"), 8), good[8:]...), want: "not an iq cache file"},
		{name: "a truncated version is a read failure", in: good[:10], want: "read cache version", wrapped: true},
		{name: "a different layout version is refused", in: badVersion, want: "cache version mismatch"},
		{name: "an undecodable header is a decode failure", in: append(bytes.Clone(good[:12]), 0x85), want: "decode cache header", wrapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, dec, err := readHeader(bytes.NewReader(tt.in))
			require.ErrorContains(t, err, tt.want)
			if tt.wrapped {
				requireWrapped(t, err)
			}
			require.Equal(t, cacheHeader{}, h)
			require.Nil(t, dec)
		})
	}
	t.Run("a valid prefix yields the header and a decoder at the first record", func(t *testing.T) {
		h, dec, err := readHeader(bytes.NewReader(good))
		require.NoError(t, err)
		require.Equal(t, cacheHeader{Path: "p", Size: 7, MTimeNano: 9, Format: 3, Decimal: 1}, h)
		require.NotNil(t, dec)
		rec, err := decodeRecord(dec)
		require.NoError(t, err)
		require.Equal(t, nRecords(1)[0], rec)
	})
}

// trailerOver builds a size-byte buffer whose last eight bytes carry off, so a
// test can aim the recorded index offset anywhere relative to the file.
func trailerOver(size int, off uint64) []byte {
	b := make([]byte, size)
	binary.LittleEndian.PutUint64(b[size-trailerLen:], off)
	return b
}

// TestReadTrailer pins the index-offset trailer's bounds. The offset is read back
// from stored bytes, so it is untrusted: it must lie inside the file and never
// name a position inside the trailer itself, and a file too small to hold one is
// refused before any read.
func TestReadTrailer(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		size    int64
		want    int64
		wantErr string
		wrapped bool
	}{
		{name: "a file too small for a trailer is refused", data: make([]byte, 7), size: 7, wantErr: "too small for trailer"},
		{name: "a trailer-only file records offset zero", data: trailerOver(8, 0), size: 8, want: 0},
		{name: "an offset at the record region's end is in range", data: trailerOver(16, 8), size: 16, want: 8},
		{name: "an offset one past the record region is refused", data: trailerOver(16, 9), size: 16, wantErr: "out of range"},
		{name: "a negative stored offset is refused", data: trailerOver(16, ^uint64(0)), size: 16, wantErr: "out of range"},
		{name: "a short read is reported", data: make([]byte, 4), size: 100, wantErr: "read trailer", wrapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readTrailer(bytes.NewReader(tt.data), tt.size)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Zero(t, got)
				if tt.wrapped {
					requireWrapped(t, err)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestReadIndex pins the index block's span. The block ends where the trailer
// begins, so a reader that overshoots that bound would decode bytes belonging to
// the trailer (or past the file) and accept an index the writer never finished.
func TestReadIndex(t *testing.T) {
	blk, err := cborEnc.Marshal(indexBlock{Pages: []pageIndex{{Offset: 12, Filter: []byte{1, 2, 3}}}})
	require.NoError(t, err)
	const off = 2
	data := append(append(make([]byte, off), blk...), make([]byte, trailerLen)...)

	t.Run("a complete block decodes its pages", func(t *testing.T) {
		got, err := readIndex(bytes.NewReader(data), off, int64(len(data)))
		require.NoError(t, err)
		require.Equal(t, []pageIndex{{Offset: 12, Filter: []byte{1, 2, 3}}}, got.Pages)
	})
	t.Run("a block cut short by the trailer bound is refused", func(t *testing.T) {
		// One byte short of the whole block: only a reader honouring the exact
		// [indexOffset, size-trailerLen) span sees the truncation.
		_, err := readIndex(bytes.NewReader(data), off, int64(off+len(blk)-1+trailerLen))
		require.ErrorContains(t, err, "decode index")
		requireWrapped(t, err)
	})
	t.Run("undecodable bytes are refused", func(t *testing.T) {
		junk := append(append(make([]byte, off), 0xff, 0xff, 0xff), make([]byte, trailerLen)...)
		_, err := readIndex(bytes.NewReader(junk), off, int64(len(junk)))
		require.ErrorContains(t, err, "decode index")
	})
}

// TestPageMayHold pins the per-page skip decision: a page is worth decoding only
// for a key that is still outstanding and that its filter admits, so neither the
// found check nor the filter test may be dropped.
func TestPageMayHold(t *testing.T) {
	filter := newBloom(4)
	bloomAdd(filter, "a")

	tests := []struct {
		name  string
		want  map[string]struct{}
		found map[string]bool
		hold  bool
	}{
		{name: "an outstanding key the filter admits", want: map[string]struct{}{"a": {}}, found: map[string]bool{}, hold: true},
		{name: "an outstanding key the filter excludes", want: map[string]struct{}{"zzz": {}}, found: map[string]bool{}, hold: false},
		{name: "a key already found does not reopen the page", want: map[string]struct{}{"a": {}}, found: map[string]bool{"a": true}, hold: false},
		{name: "one outstanding key among found ones", want: map[string]struct{}{"a": {}, "zzz": {}}, found: map[string]bool{"zzz": true}, hold: true},
		{name: "no wanted keys at all", want: map[string]struct{}{}, found: map[string]bool{}, hold: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.hold, pageMayHold(filter, tt.want, tt.found))
		})
	}
	t.Run("an empty filter admits nothing", func(t *testing.T) {
		require.False(t, pageMayHold(nil, map[string]struct{}{"a": {}}, map[string]bool{}))
	})
}

// TestDecodePage confirms a decoded page copies exactly the wanted keys into the
// result and records each as found — the bookkeeping that lets the caller stop
// early and skip later pages.
func TestDecodePage(t *testing.T) {
	var body bytes.Buffer
	for _, r := range nRecords(3) {
		b, err := encodeRecord(r)
		require.NoError(t, err)
		body.Write(b)
	}
	want := map[string]struct{}{"k0": {}, "k2": {}, "absent": {}}
	out := map[string]any{}
	found := map[string]bool{}
	require.NoError(t, decodePage(bytes.NewReader(body.Bytes()), 0, int64(body.Len()), want, out, found))
	require.Equal(t, map[string]any{"k0": "v0", "k2": "v2"}, out)
	require.Equal(t, map[string]bool{"k0": true, "k2": true}, found)

	t.Run("a corrupt record is reported", func(t *testing.T) {
		junk, err := cborEnc.Marshal("not a record")
		require.NoError(t, err)
		err = decodePage(bytes.NewReader(junk), 0, int64(len(junk)), want, map[string]any{}, map[string]bool{})
		require.Error(t, err)
	})
}

// TestCacheGetIndexPolicy pins when the bounded read consults the page index. The
// index is only read when the policy asks for one and the file actually carries
// pages, so a flat cache falls back to streaming and an indexed cache read under a
// disabled policy does too — even though its pages are right there.
func TestCacheGetIndexPolicy(t *testing.T) {
	t.Run("a disabled index never consults the pages", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		st.cache.Index = false
		out, ok := st.cacheGet(context.Background(), []string{"k1"})
		require.False(t, ok)
		require.Nil(t, out)
	})
	t.Run("a flat cache carries no pages to consult", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(3), false, nil))
		out, ok := st.cacheGet(context.Background(), []string{"k1"})
		require.False(t, ok)
		require.Nil(t, out)
	})
	t.Run("a single-page index is used", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		out, ok := st.cacheGet(context.Background(), []string{"k1", "nope"})
		require.True(t, ok)
		require.Equal(t, map[string]any{"k1": "v1"}, out)
	})
	t.Run("a multi-page index resolves keys from every page", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(2*pageSize+1), true, nil))
		out, ok := st.cacheGet(context.Background(), []string{"k0", "k700", "k1000", "nope"})
		require.True(t, ok)
		require.Equal(t, map[string]any{"k0": "v0", "k700": "v700", "k1000": "v1000"}, out)
	})
	t.Run("a page the filters exclude is skipped, not a stopping point", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(2*pageSize+1), true, nil))
		f, err := os.Open(st.cacheFile(m))
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		size, err := fileSize(f)
		require.NoError(t, err)
		indexOffset, err := readTrailer(f, size)
		require.NoError(t, err)
		idx, err := readIndex(f, indexOffset, size)
		require.NoError(t, err)
		require.Len(t, idx.Pages, 3)
		// The key lives only in the last page, and the first page's filter really does
		// exclude it, so a read that stopped at the first non-candidate would lose it.
		require.False(t, bloomHas(idx.Pages[0].Filter, "k1000"))

		out, ok := st.cacheGet(context.Background(), []string{"k1000"})
		require.True(t, ok)
		require.Equal(t, map[string]any{"k1000": "v1000"}, out)
	})
	t.Run("a cancelled context abandons the index read", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, ok := st.cacheGet(ctx, []string{"k1"})
		require.False(t, ok)
	})
	t.Run("a missing cache file falls back", func(t *testing.T) {
		st, _, _ := cacheFixture(t)
		_, ok := st.cacheGet(context.Background(), []string{"k1"})
		require.False(t, ok)
	})
	t.Run("an out-of-range trailer falls back", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		content := cacheBytes(t, h, nRecords(3), true, nil)
		binary.LittleEndian.PutUint64(content[len(content)-trailerLen:], ^uint64(0))
		plantCache(t, st, m, content)
		_, ok := st.cacheGet(context.Background(), []string{"k1"})
		require.False(t, ok)
	})
}

// TestCacheGetRejectsAMismatchedHeader covers the bounded read's own freshness
// check. Every recorded fact is perturbed alone so no term can be dropped: a
// loosened check would answer a bounded query from a cache built for a different
// dump, format, or decimal mode.
func TestCacheGetRejectsAMismatchedHeader(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*cacheHeader)
		corrupt bool
	}{
		{name: "a changed size is refused", mutate: func(h *cacheHeader) { h.Size++ }},
		{name: "a changed mtime is refused", mutate: func(h *cacheHeader) { h.MTimeNano++ }},
		{name: "a changed format is refused", mutate: func(h *cacheHeader) { h.Format++ }},
		{name: "a changed decimal mode is refused", mutate: func(h *cacheHeader) { h.Decimal++ }},
		{name: "an unreadable header is refused", mutate: func(*cacheHeader) {}, corrupt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, m, h := cacheFixture(t)
			tt.mutate(&h)
			content := cacheBytes(t, h, nRecords(3), true, nil)
			if tt.corrupt {
				copy(content, "XXXXXXXX")
			}
			plantCache(t, st, m, content)
			out, ok := st.cacheGet(context.Background(), []string{"k1"})
			require.False(t, ok)
			require.Nil(t, out)
		})
	}
	t.Run("a matching header is used", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		_, ok := st.cacheGet(context.Background(), []string{"k1"})
		require.True(t, ok)
	})
}

// TestCacheWriterWriteReportsBytesWritten pins the io.Writer contract the header,
// record, and index writers all share: the count they see is the count the file
// took, and the tracked position advances by exactly that much so each page's
// recorded start offset is real.
func TestCacheWriterWriteReportsBytesWritten(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "w-*.tmp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	w := &cacheWriter{f: f, tmp: f.Name()}

	n, err := w.Write([]byte("abcde"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, int64(5), w.pos)

	n, err = w.Write([]byte("fg"))
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, int64(7), w.pos)
}

// TestNewCacheWriterCannotCreateTheCacheDir names the failure that stops a cache
// before any temp file exists, so it is reported as a directory problem rather
// than surfacing later as a confusing temp-file failure.
func TestNewCacheWriterCannotCreateTheCacheDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	st := &Store{cache: CacheConfig{Dir: filepath.Join(blocker, "cache"), Enabled: true, MinSize: 1}}
	w, err := st.newCacheWriter(cacheMeta{path: "p", size: 1})
	require.ErrorContains(t, err, "create cache dir")
	requireWrapped(t, err)
	require.Nil(t, w)
}

// TestPopulateDiscardsAnIncompleteDecode is the cache's central safety rule: a
// decode that fails partway must leave nothing installed, so a later run re-reads
// the dump instead of serving a truncated one.
func TestPopulateDiscardsAnIncompleteDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, corruptBody(content), 0o600))

	st, err := Open(URL(path), numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	require.Error(t, st.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	require.Empty(t, cacheFiles(t, dir), "a failed decode installs no cache")
	require.Empty(t, tempFiles(t, dir), "and leaves no temp behind")
}

// tempFiles lists the leftover temp cache files in dir.
func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, d := range des {
		if filepath.Ext(d.Name()) == ".tmp" {
			out = append(out, d.Name())
		}
	}
	return out
}

// TestPopulateDiscardsWhenTheInstallFails blocks the atomic install by occupying
// the final cache path with a directory. The commit cannot rename over it, so the
// run must report failure and clean its temp up rather than leaving a stray one
// behind on every query.
func TestPopulateDiscardsWhenTheInstallFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	st, err := Open(URL(path), numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)
	m, ok := st.cacheable()
	require.True(t, ok)
	require.NoError(t, os.MkdirAll(st.cacheFile(m), 0o700)) // occupy the install target.

	require.NoError(t, st.TypedScan(context.Background(), func([]query.Record) error { return nil }))
	require.Empty(t, tempFiles(t, dir), "a failed install discards its temp")
	_, fresh := st.cacheSource()
	require.False(t, fresh, "and installs nothing a later read could serve")
}

// TestCacheHeaderRecordsTheDecimalMode confirms the decimal mode reaches the file
// it keys: a header that omitted it would let a run under one mode serve records
// normalized under another.
func TestCacheHeaderRecordsTheDecimalMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "d.rdb")
	writeRDBWithString(t, path, "s", []byte("hello"))
	st, err := Open(URL(path), numfmt.DecimalString, cacheCfg(dir))
	require.NoError(t, err)
	require.NotZero(t, int(st.dec), "the mode under test must differ from the zero value")
	require.NoError(t, st.TypedScan(context.Background(), func([]query.Record) error { return nil }))

	files := cacheFiles(t, dir)
	require.Len(t, files, 1)
	h, ok := headerOf(filepath.Join(dir, files[0]))
	require.True(t, ok)
	require.Equal(t, int(numfmt.DecimalString), h.Decimal)
	require.Equal(t, int(FormatRDB), h.Format)
}

// plantNamedCache writes a valid cache file called name into dir, recording dump
// as the dump it caches, so a listing test controls both the file name and the
// dump path the entries sort by.
func plantNamedCache(t *testing.T, dir, name, dump string) {
	t.Helper()
	h := cacheHeader{Path: dump, Size: 4096, MTimeNano: 7, Format: int(FormatRDB)}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), cacheBytes(t, h, nRecords(1), true, nil), 0o600))
}

// TestListCacheSelectsAndSorts pins what `iq cache stat` shows: only readable
// current-version .cbor files count, a directory or a foreign file in the cache
// dir is skipped rather than fatal, each entry reports the dump size its header
// recorded, and the listing is ordered by dump path rather than by file name.
func TestListCacheSelectsAndSorts(t *testing.T) {
	dir := t.TempDir()
	plantNamedCache(t, dir, "a.cbor", "/dumps/z.rdb")
	plantNamedCache(t, dir, "e.cbor", "/dumps/a.rdb")
	plantNamedCache(t, dir, "b.txt", "/dumps/skipped-by-extension.rdb")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "c.cbor"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "d.cbor"), []byte("not a cache"), 0o600))

	entries, err := ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "/dumps/a.rdb", entries[0].DumpPath, "ordered by dump path, not file name")
	require.Equal(t, "e.cbor", entries[0].File)
	require.Equal(t, "/dumps/z.rdb", entries[1].DumpPath)
	require.Equal(t, "a.cbor", entries[1].File)
	for _, e := range entries {
		require.Equal(t, int64(4096), e.DumpSize, "the dump size the header recorded")
		require.Positive(t, e.CacheSize)
	}
}

// TestListCacheTieBreaksByFileName covers the secondary sort: several caches for
// one dump (an edited dump leaves the old entries behind) order by cache file name.
func TestListCacheTieBreaksByFileName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"1.cbor", "2.cbor", "3.cbor"} {
		plantNamedCache(t, dir, name, "/dumps/same.rdb")
	}
	entries, err := ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, []string{"1.cbor", "2.cbor", "3.cbor"}, []string{entries[0].File, entries[1].File, entries[2].File})
}

// TestListCacheUnreadableDir reports a dir that exists but cannot be read as an
// error, distinct from the missing dir that simply means nothing is cached.
func TestListCacheUnreadableDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := ListCache(dir)
	require.ErrorContains(t, err, "read cache dir")
	require.ErrorIs(t, err, fs.ErrPermission)

	n, err := RemoveCache(dir, "")
	require.ErrorContains(t, err, "read cache dir")
	require.ErrorIs(t, err, fs.ErrPermission)
	require.Zero(t, n, "nothing was removed")
}

// TestRemoveCacheResolvesARelativeDumpPath confirms `iq cache clear ./dump.rdb`
// matches the absolute path a cache header records; matching the argument verbatim
// would silently remove nothing.
func TestRemoveCacheResolvesARelativeDumpPath(t *testing.T) {
	dir := t.TempDir()
	dumpDir := t.TempDir()
	plantNamedCache(t, dir, "a.cbor", "/elsewhere/other.rdb") // read first, and not the target.
	plantNamedCache(t, dir, "b.cbor", filepath.Join(dumpDir, "d.rdb"))

	t.Chdir(dumpDir)
	n, err := RemoveCache(dir, "d.rdb")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	entries, err := ListCache(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "/elsewhere/other.rdb", entries[0].DumpPath)
}

// TestRemoveCacheReportsARemoveFailure surfaces an undeletable cache file as an
// error with the count removed so far, rather than reporting a clear that never
// happened.
func TestRemoveCacheReportsARemoveFailure(t *testing.T) {
	dir := t.TempDir()
	plantNamedCache(t, dir, "a.cbor", "/dumps/one.rdb")
	require.NoError(t, os.Chmod(dir, 0o500)) // readable and listable, but not writable.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	n, err := RemoveCache(dir, "")
	require.ErrorContains(t, err, "remove cache")
	require.ErrorIs(t, err, fs.ErrPermission)
	require.Zero(t, n)
}

// TestGetUsesTheIndexWithoutDecodingTheCache proves the bounded read is really
// answered from the page index rather than falling through to a streaming pass that
// happens to agree. The planted cache ends in corrupt bytes, and the requested key is
// one every page filter excludes: the index answers it without decoding a page, while
// streaming the same file runs into the corruption.
func TestGetUsesTheIndexWithoutDecodingTheCache(t *testing.T) {
	st, m, h := cacheFixture(t)
	junk, err := cborEnc.Marshal("not a record")
	require.NoError(t, err)
	plantCache(t, st, m, cacheBytes(t, h, nRecords(pageSize+1), true, junk))

	got, err := st.Get(context.Background(), []string{"no-such-key"})
	require.NoError(t, err)
	require.Empty(t, got)

	err = st.TypedScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "decode cache record", "streaming the same cache does reach the corruption")
}
