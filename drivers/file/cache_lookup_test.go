package file

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

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
			l := &keyLookup{want: tt.want, found: tt.found}
			require.Equal(t, tt.hold, l.mayHold(filter))
		})
	}
	t.Run("an empty filter admits nothing", func(t *testing.T) {
		l := &keyLookup{want: map[string]struct{}{"a": {}}, found: map[string]bool{}}
		require.False(t, l.mayHold(nil))
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
	l := newKeyLookup([]string{"k0", "k2", "absent"})
	require.NoError(t, decodePage(bytes.NewReader(body.Bytes()), 0, int64(body.Len()), l))
	require.Equal(t, map[string]any{"k0": "v0", "k2": "v2"}, l.out)
	require.Equal(t, map[string]bool{"k0": true, "k2": true}, l.found)

	t.Run("a corrupt record is reported", func(t *testing.T) {
		junk, err := cborEnc.Marshal("not a record")
		require.NoError(t, err)
		err = decodePage(bytes.NewReader(junk), 0, int64(len(junk)), newKeyLookup([]string{"k0", "k2", "absent"}))
		require.Error(t, err)
	})
}

// TestDecodePageKeepsTheLastValueOfARepeatedKey puts one wanted key twice in a page.
// The page copies every wanted record, so the later value replaces the earlier one.
func TestDecodePageKeepsTheLastValueOfARepeatedKey(t *testing.T) {
	var body bytes.Buffer
	for _, r := range []query.Record{
		{Key: "a", Type: "string", Value: "first"},
		{Key: "b", Type: "string", Value: "other"},
		{Key: "a", Type: "string", Value: "last"},
	} {
		b, err := encodeRecord(r)
		require.NoError(t, err)
		body.Write(b)
	}
	l := newKeyLookup([]string{"a"})
	require.NoError(t, decodePage(bytes.NewReader(body.Bytes()), 0, int64(body.Len()), l))
	require.Equal(t, map[string]any{"a": "last"}, l.out)
	require.Equal(t, map[string]bool{"a": true}, l.found)
}

// TestDecodePageReadsToTheEndOfThePage puts a corrupt record after the only wanted
// key. The page does not stop when every key is found, so it reports the corruption.
func TestDecodePageReadsToTheEndOfThePage(t *testing.T) {
	first, err := encodeRecord(query.Record{Key: "a", Type: "string", Value: "x"})
	require.NoError(t, err)
	junk, err := cborEnc.Marshal("not a record")
	require.NoError(t, err)
	body := append(first, junk...)
	err = decodePage(bytes.NewReader(body), 0, int64(len(body)), newKeyLookup([]string{"a"}))
	require.Error(t, err)
}

// TestCacheKeyIgnoresTheRecordedPath changes only the Path of the header. The dump
// identity is its size, mtime, format and decimal mode, so the cache is still fresh
// for every reader that checks the header.
func TestCacheKeyIgnoresTheRecordedPath(t *testing.T) {
	t.Run("cacheFresh", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		h.Path = "/somewhere/else.rdb"
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(1), true, nil))
		require.True(t, st.cacheFresh(path, m))
	})
	t.Run("readCache", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		h.Path = "/somewhere/else.rdb"
		path := plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		var got []query.Record
		require.NoError(t, st.readCache(context.Background(), path, m, func(b []query.Record) error {
			got = append(got, b...)
			return nil
		}))
		require.Equal(t, nRecords(3), got)
	})
	t.Run("cacheGet", func(t *testing.T) {
		st, m, h := cacheFixture(t)
		h.Path = "/somewhere/else.rdb"
		plantCache(t, st, m, cacheBytes(t, h, nRecords(3), true, nil))
		out, ok := st.cacheGet(context.Background(), []string{"k1"})
		require.True(t, ok)
		require.Equal(t, map[string]any{"k1": "v1"}, out)
	})
}

// TestCacheGetCountsDistinctKeys asks for the same key twice. The request is complete
// when the one distinct key is found, so the read stops at the page boundary and
// never reaches the second page, where the canceled context would fail it.
func TestCacheGetCountsDistinctKeys(t *testing.T) {
	st, m, h := cacheFixture(t)
	plantCache(t, st, m, cacheBytes(t, h, nRecords(pageSize+1), true, nil))
	ctx := &cancelLaterCtx{Context: context.Background(), ok: 1}
	out, ok := st.cacheGet(ctx, []string{"k0", "k0"})
	require.True(t, ok)
	require.Equal(t, map[string]any{"k0": "v0"}, out)
}

// TestCacheGetStopsBetweenPagesNotInsideOne asks for two keys that sit on the first
// page, with a repeated record of the first key after the second one. The page is read
// to its end, so the later value wins. The read then stops before the second page.
func TestCacheGetStopsBetweenPagesNotInsideOne(t *testing.T) {
	st, m, h := cacheFixture(t)
	recs := nRecords(pageSize + 1)
	recs[pageSize-1] = query.Record{Key: "k0", Type: "string", Value: "late"}
	plantCache(t, st, m, cacheBytes(t, h, recs, true, nil))
	ctx := &cancelLaterCtx{Context: context.Background(), ok: 1}
	out, ok := st.cacheGet(ctx, []string{"k0", "k1"})
	require.True(t, ok)
	require.Equal(t, map[string]any{"k0": "late", "k1": "v1"}, out)
}

// TestCacheGetFallsBackOnAnUnreadablePage plants a corrupt record in the page of the
// wanted key. The read reports a miss and never returns an error or a partial answer.
func TestCacheGetFallsBackOnAnUnreadablePage(t *testing.T) {
	st, m, h := cacheFixture(t)
	junk, err := cborEnc.Marshal("not a record")
	require.NoError(t, err)
	plantCache(t, st, m, cacheBytes(t, h, nRecords(2), true, junk))
	out, ok := st.cacheGet(context.Background(), []string{"k0"})
	require.False(t, ok)
	require.Nil(t, out)
}

// TestGetKeepsTheLaterValueUntilEveryKeyIsFound streams a dump where a wanted key
// repeats before the last wanted key shows up. Get stores every wanted record inside
// a page and stops at the record that completes the request, so a repeat before that
// record wins and a repeat after it is never read.
func TestGetKeepsTheLaterValueUntilEveryKeyIsFound(t *testing.T) {
	body := strings.Join([]string{
		`{"key":"a","type":"string","value":"1"}`,
		`{"key":"a","type":"string","value":"2"}`,
		`{"key":"b","type":"string","value":"3"}`,
		`{"key":"a","type":"string","value":"4"}`,
	}, "\n") + "\n"
	st := jsonlStore(t, body, CacheConfig{})
	got, err := st.Get(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": "2", "b": "3"}, got)

	got, err = st.Get(context.Background(), []string{"a"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": "1"}, got, "the first record completes a one-key request")

	got, err = st.Get(context.Background(), []string{"a", "a", "b"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": "2", "b": "3"}, got, "a repeated request key counts once")
}

// TestReadCacheReusesThePageSlice gives the consumer each page and compares the
// backing array. The pages share one slice, so a caller must copy what it keeps.
func TestReadCacheReusesThePageSlice(t *testing.T) {
	st, m, h := cacheFixture(t)
	path := plantCache(t, st, m, cacheBytes(t, h, nRecords(2*pageSize+1), true, nil))
	var firsts []*query.Record
	var sizes []int
	require.NoError(t, st.readCache(context.Background(), path, m, func(b []query.Record) error {
		firsts = append(firsts, &b[0])
		sizes = append(sizes, len(b))
		return nil
	}))
	require.Equal(t, []int{pageSize, pageSize, 1}, sizes)
	require.Same(t, firsts[0], firsts[1])
	require.Same(t, firsts[0], firsts[2])
}

// TestRemoveCacheTakesTheRemoveErrorFromTheOS compares the whole error text with the
// text of a real failed os.Remove, because the cause text differs per platform.
func TestRemoveCacheTakesTheRemoveErrorFromTheOS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not stop writes on Windows, so this failure cannot be forced there")
	}
	dir := t.TempDir()
	plantNamedCache(t, dir, "a.cbor", "/dumps/one.rdb")
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "a.cbor")
	osErr := os.Remove(path)
	require.Error(t, osErr)

	n, err := RemoveCache(dir, "")
	require.EqualError(t, err, fmt.Sprintf("remove cache %q: %v", path, osErr))
	require.Zero(t, n)
}
