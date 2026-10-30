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
