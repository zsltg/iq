package file

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zsltg/rdb/encoder"
	"github.com/zsltg/rdb/model"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// keyLimit is the lowered limit of the tests. Each key below holds one more element.
const keyLimit = 6

// oneKeyRDB builds an RDB that holds the single key "k", written by write.
func oneKeyRDB(t *testing.T, write func(enc *encoder.Encoder) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, 1, 0))
	require.NoError(t, write(enc))
	require.NoError(t, enc.WriteEnd())
	return buf.Bytes()
}

func elementsOf(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte("e" + strconv.Itoa(i))
	}
	return out
}

func streamOf(n int) *model.StreamObject {
	id := &model.StreamId{Ms: 100}
	entry := &model.StreamEntry{FirstMsgId: id, Fields: []string{"a"}}
	for i := range n {
		entry.Msgs = append(entry.Msgs, &model.StreamMessage{
			Id:     &model.StreamId{Ms: 100, Sequence: uint64(i)},
			Fields: map[string]string{"a": "1"},
		})
	}
	return &model.StreamObject{Version: 1, Length: uint64(n), LastId: id, Entries: []*model.StreamEntry{entry}}
}

func TestRDBSourceRefusesLargeKey(t *testing.T) {
	over := keyLimit + 1
	tests := []struct {
		name  string
		write func(n int) func(enc *encoder.Encoder) error
		want  string // the text of the error for a key of over elements
	}{
		{"list", func(n int) func(*encoder.Encoder) error {
			return func(enc *encoder.Encoder) error { return enc.WriteListObject("k", elementsOf(n)) }
		}, "the list has 7 elements and the limit is 6"},
		{"set", func(n int) func(*encoder.Encoder) error {
			return func(enc *encoder.Encoder) error { return enc.WriteSetObject("k", elementsOf(n)) }
		}, "the set has 7 elements and the limit is 6"},
		{"hash", func(n int) func(*encoder.Encoder) error {
			return func(enc *encoder.Encoder) error {
				m := map[string][]byte{}
				for i := range n {
					m["f"+strconv.Itoa(i)] = []byte("v")
				}
				return enc.WriteHashMapObject("k", m)
			}
		}, "the hash has 7 elements and the limit is 6"},
		{"zset", func(n int) func(*encoder.Encoder) error {
			return func(enc *encoder.Encoder) error {
				var entries []*model.ZSetEntry
				for i := range n {
					entries = append(entries, &model.ZSetEntry{Member: "m" + strconv.Itoa(i), Score: float64(i)})
				}
				return enc.WriteZSetObject("k", entries)
			}
		}, "the zset has 7 elements and the limit is 6"},
		{"stream", func(n int) func(*encoder.Encoder) error {
			return func(enc *encoder.Encoder) error { return enc.WriteStreamObject("k", streamOf(n)) }
		}, "the stream has more than 6 elements and the limit is 6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			discard := func([]query.Record) error { return nil }

			t.Run("refused over the limit", func(t *testing.T) {
				data := oneKeyRDB(t, tt.write(over))
				err := rdbSourceLimit(bytes.NewReader(data), pageSize, keyLimit)(ctx, discard)
				require.ErrorIs(t, err, ErrKeyTooLarge)
				require.EqualError(t, err, `read key "k": too many elements: `+tt.want+`, so read it with --unbounded`)
			})
			t.Run("read at the limit", func(t *testing.T) {
				data := oneKeyRDB(t, tt.write(keyLimit))
				recs := readAll(t, ctx, rdbSourceLimit(bytes.NewReader(data), pageSize, keyLimit))
				require.Len(t, recs, 1)
			})
			t.Run("read over the limit when unbounded", func(t *testing.T) {
				data := oneKeyRDB(t, tt.write(over))
				recs := readAll(t, query.WithUnbounded(ctx), rdbSourceLimit(bytes.NewReader(data), pageSize, keyLimit))
				require.Len(t, recs, 1)
			})
		})
	}
}

// A key over the limit stops the scan, and the keys before it are still delivered.
func TestRDBSourceLimitNamesTheLargeKey(t *testing.T) {
	data := oneKeyRDB(t, func(enc *encoder.Encoder) error {
		if err := enc.WriteSetObject("small", elementsOf(2)); err != nil {
			return err
		}
		return enc.WriteSetObject("big", elementsOf(keyLimit+1))
	})
	var got []string
	err := rdbSourceLimit(bytes.NewReader(data), 1, keyLimit)(context.Background(), func(batch []query.Record) error {
		for _, r := range batch {
			got = append(got, r.Key)
		}
		return nil
	})
	require.ErrorIs(t, err, ErrKeyTooLarge)
	require.ErrorContains(t, err, `read key "big"`)
	require.Equal(t, []string{"small"}, got)
}

// TestFileStoreRefusesLargeKey reads a set of one million and one members through the
// file store. The error that reaches the caller is the clean one, and the default
// limit is query.MaxValueElements.
func TestFileStoreRefusesLargeKey(t *testing.T) {
	const members = query.MaxValueElements + 1
	items := make([][]byte, members)
	for i := range items {
		items[i] = strconv.AppendInt(nil, int64(i), 10) // integers, so the set is an intset.
	}
	data := oneKeyRDB(t, func(enc *encoder.Encoder) error { return enc.WriteSetObject("k", items) })
	st, err := Open(writeDump(t, "large.rdb", data, ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	err = st.TypedScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorIs(t, err, ErrKeyTooLarge)
	require.EqualError(t, err, fmt.Sprintf(
		`read key "k": too many elements: the set has %d elements and the limit is %d, so read it with --unbounded`,
		members, query.MaxValueElements))
}

// TestUnboundedReadDoesNotWarmTheCache proves that a bounded read never gets a
// large key from a cache that an unbounded read could have written. The unbounded
// scan reads the key and writes no cache. The bounded scan after it still refuses.
func TestUnboundedReadDoesNotWarmTheCache(t *testing.T) {
	items := make([][]byte, query.MaxValueElements+1)
	for i := range items {
		items[i] = strconv.AppendInt(nil, int64(i), 10) // integers, so the set is an intset.
	}
	data := oneKeyRDB(t, func(enc *encoder.Encoder) error { return enc.WriteSetObject("k", items) })
	path := writeDump(t, "large.rdb", data, "")
	dir := t.TempDir()
	st, err := Open(path, numfmt.DecimalAuto, cacheCfg(dir))
	require.NoError(t, err)

	var got int
	require.NoError(t, st.TypedScan(query.WithUnbounded(context.Background()), func(b []query.Record) error {
		got += len(b)
		return nil
	}))
	require.Equal(t, 1, got)
	require.Empty(t, cacheFiles(t, dir), "an unbounded read must not write the cache")

	err = st.TypedScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorIs(t, err, ErrKeyTooLarge)
}

// TestRDBSourceLimitNamesTheHashTemplate reads a Redis 8.10 dump whose hash template
// has more fields than the limit. A template is not a key, so the message names the
// template, and the unbounded opt-in still reads the dump.
func TestRDBSourceLimitNamesTheHashTemplate(t *testing.T) {
	// The RDB 15 file holds hash template 3 with the fields a and b, and the hash
	// "h" that uses it.
	data := []byte("REDIS0015\xf2\x03\x02\x01a\x01b\xfe\x00\x20\x01h\x03\x01\x31\x01\x32\xff")
	tests := []struct {
		name    string
		ctx     context.Context
		wantErr string
	}{
		{
			name: "bounded",
			ctx:  context.Background(),
			wantErr: "read a hash template: too many elements: it has 2 fields and the limit is 1, " +
				"so read the dump with --unbounded",
		},
		{name: "unbounded", ctx: query.WithUnbounded(context.Background())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []query.Record
			err := rdbSourceLimit(bytes.NewReader(data), 1, 1)(tt.ctx, func(batch []query.Record) error {
				got = append(got, batch...)
				return nil
			})
			if tt.wantErr != "" {
				require.ErrorIs(t, err, ErrKeyTooLarge)
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []query.Record{{Key: "h", Type: "hash", Value: map[string]any{"a": "1", "b": "2"}}}, got)
		})
	}
}
