package file

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/rawpred"
)

// TestRecordPager checks the page rules: a full page goes to fn at once, the tail goes
// on flush, an empty tail goes nowhere, one slice backs every page, and an error of fn
// comes back from the call that handed the page over.
func TestRecordPager(t *testing.T) {
	boom := errors.New("page refused")
	t.Run("full pages, then the tail", func(t *testing.T) {
		var sizes []int
		var firsts []*query.Record
		pg := newRecordPager(2, func(b []query.Record) error {
			sizes = append(sizes, len(b))
			firsts = append(firsts, &b[0])
			return nil
		})
		for _, r := range nRecords(5) {
			require.NoError(t, pg.add(r))
		}
		require.Equal(t, []int{2, 2}, sizes, "the tail waits for flush")
		require.NoError(t, pg.flush())
		require.Equal(t, []int{2, 2, 1}, sizes)
		require.Same(t, firsts[0], firsts[1])
		require.Same(t, firsts[0], firsts[2])
	})
	t.Run("an exact multiple leaves no tail", func(t *testing.T) {
		calls := 0
		pg := newRecordPager(2, func([]query.Record) error { calls++; return nil })
		for _, r := range nRecords(2) {
			require.NoError(t, pg.add(r))
		}
		require.NoError(t, pg.flush())
		require.Equal(t, 1, calls)
	})
	t.Run("an error of a full page comes from add", func(t *testing.T) {
		pg := newRecordPager(1, func([]query.Record) error { return boom })
		require.ErrorIs(t, pg.add(nRecords(1)[0]), boom)
	})
	t.Run("an error of the tail comes from flush", func(t *testing.T) {
		pg := newRecordPager(2, func([]query.Record) error { return boom })
		require.NoError(t, pg.add(nRecords(1)[0]))
		require.ErrorIs(t, pg.flush(), boom)
	})
}

// TestKeyLookupTake checks the lookup rules: an unwanted key is ignored, a repeat of a
// wanted key replaces the value, and take reports completion once every wanted key is
// found. A repeated request key counts once.
func TestKeyLookupTake(t *testing.T) {
	l := newKeyLookup([]string{"a", "b", "a"})
	require.False(t, l.done())
	require.False(t, l.take(query.Record{Key: "x", Value: "no"}))
	require.False(t, l.take(query.Record{Key: "a", Value: "1"}))
	require.False(t, l.take(query.Record{Key: "a", Value: "2"}))
	require.False(t, l.done())
	require.True(t, l.take(query.Record{Key: "b", Value: "3"}))
	require.True(t, l.done())
	require.Equal(t, map[string]any{"a": "2", "b": "3"}, l.out)
	require.Equal(t, map[string]bool{"a": true, "b": true}, l.found)
}

// TestRecordFilterDrop checks that drop counts only a record whose value it can
// extract, and drops only a record that the matcher proves cannot match.
func TestRecordFilterDrop(t *testing.T) {
	var checked, skipped int
	f := recordFilter{
		matcher: rawpred.NewMatcher(predicate.Eq{Path: []string{"n"}, Value: 5.0}),
		checked: &checked, skipped: &skipped,
	}
	require.False(t, f.drop([]byte(`{"key":"a","type":"t","value":{"n":5}}`)))
	require.Equal(t, [2]int{1, 0}, [2]int{checked, skipped})
	require.True(t, f.drop([]byte(`{"key":"b","type":"t","value":{"n":6}}`)))
	require.Equal(t, [2]int{2, 1}, [2]int{checked, skipped})
	require.False(t, f.drop([]byte(`{"key":"c","type":"t"}`)), "a record with no value is left to the decoder")
	require.Equal(t, [2]int{2, 1}, [2]int{checked, skipped})
}

// TestRecordBatchKeepsTheLastValue folds a page into a map. A repeated key keeps its
// last value.
func TestRecordBatchKeepsTheLastValue(t *testing.T) {
	got := recordBatch([]query.Record{{Key: "a", Value: 1}, {Key: "b", Value: 2}, {Key: "a", Value: 3}})
	require.Equal(t, map[string]any{"a": 3, "b": 2}, got)
	require.Empty(t, recordBatch(nil))
}

// TestCacheIndexPageEnd checks that a page ends where the next one starts, and that
// the last page ends where the record region ends.
func TestCacheIndexPageEnd(t *testing.T) {
	ix := cacheIndex{pages: []pageIndex{{Offset: 10}, {Offset: 40}, {Offset: 70}}, end: 99}
	require.Equal(t, int64(40), ix.pageEnd(0))
	require.Equal(t, int64(70), ix.pageEnd(1))
	require.Equal(t, int64(99), ix.pageEnd(2))
}

// TestCacheEntries checks the listing rules shared by ListCache and RemoveCache: a
// missing dir is empty, a directory and a file without the .cbor extension are left
// out, and a read failure names the dir.
func TestCacheEntries(t *testing.T) {
	t.Run("a missing dir has no entries", func(t *testing.T) {
		des, err := cacheEntries(filepath.Join(t.TempDir(), "none"))
		require.NoError(t, err)
		require.Empty(t, des)
	})
	t.Run("only .cbor files count", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.cbor"), nil, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), nil, 0o600))
		require.NoError(t, os.Mkdir(filepath.Join(dir, "c.cbor"), 0o700))
		des, err := cacheEntries(dir)
		require.NoError(t, err)
		require.Len(t, des, 1)
		require.Equal(t, "a.cbor", des[0].Name())
	})
	t.Run("a path that is a file is a read error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "f")
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		_, err := cacheEntries(file)
		require.ErrorContains(t, err, "read cache dir")
		requireWrapped(t, err)
	})
}

// TestAbsOrSelf checks that a relative path becomes absolute and an absolute path stays.
func TestAbsOrSelf(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got := absOrSelf("d.rdb")
	require.True(t, filepath.IsAbs(got))
	require.Equal(t, "d.rdb", filepath.Base(got))
	abs := filepath.Join(dir, "x")
	require.Equal(t, abs, absOrSelf(abs))
}
