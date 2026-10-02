package file

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// TestCacheKeyCarriesTheDecimalMode opens the store in a decimal mode other than the
// one the cache header recorded. The cache was built under another mode, so a read
// must refuse it.
func TestCacheKeyCarriesTheDecimalMode(t *testing.T) {
	st, m, h := cacheFixture(t)
	st.dec = numfmt.DecimalString
	require.NotEqual(t, h.Decimal, int(st.dec))
	path := plantCache(t, st, m, cacheBytes(t, h, nRecords(1), true, nil))
	err := st.readCache(context.Background(), path, m, func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "no longer matches its dump")
}

// TestRecordPagerBuffersAPageInOneAllocation uses a size that is not a power of two.
// A page that grows by append ends with a larger capacity than the size.
func TestRecordPagerBuffersAPageInOneAllocation(t *testing.T) {
	var caps []int
	p := newRecordPager(5, func(b []query.Record) error {
		caps = append(caps, cap(b))
		return nil
	})
	for _, r := range nRecords(5) {
		require.NoError(t, p.add(r))
	}
	require.Equal(t, []int{5}, caps)
}

// TestStreamRecordsStopsOnAFailedPage fails only the first page. The final flush
// would succeed, so the stream must return the error of the page that filled up.
func TestStreamRecordsStopsOnAFailedPage(t *testing.T) {
	st, m, h := cacheFixture(t)
	path := plantCache(t, st, m, cacheBytes(t, h, nRecords(pageSize), true, nil))
	boom := errors.New("page refused")
	calls := 0
	err := st.readCache(context.Background(), path, m, func([]query.Record) error {
		calls++
		if calls == 1 {
			return boom
		}
		return nil
	})
	require.ErrorIs(t, err, boom)
}

// TestOpenIndexRejectsAnUnreadableHeader plants a cache whose header is garbage but
// whose index and trailer are valid, for a store and dump whose key is all zero. The
// zero header of a failed read equals that key, so only the read error refuses it.
func TestOpenIndexRejectsAnUnreadableHeader(t *testing.T) {
	st := &Store{}
	m := cacheMeta{}
	require.Equal(t, cacheKey{}, st.keyFor(m))
	var buf bytes.Buffer
	buf.WriteString("garbage, not a cache header")
	indexOffset := int64(buf.Len())
	blk, err := cborEnc.Marshal(indexBlock{Pages: []pageIndex{{Offset: 0, Filter: []byte{1}}}})
	require.NoError(t, err)
	buf.Write(blk)
	var trailer [trailerLen]byte
	binary.LittleEndian.PutUint64(trailer[:], uint64(indexOffset))
	buf.Write(trailer[:])
	path := filepath.Join(t.TempDir(), "c.cbor")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	_, ok := st.openIndex(f, m)
	require.False(t, ok)
}
