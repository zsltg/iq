package hbase

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// TestTraceLines pins the whole --verbose trace of every traced operation, line for
// line. The comparison is exact rather than a substring, so a dropped, extra or
// reworded line fails: the trace is the only record of what the driver did, and it
// must never carry a cell value.
func TestTraceLines(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, st *Store, fc *fakeClient)
		want string
	}{
		{
			name: "get names the table and the key count",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				fc.seed("1", cell("cf", "title", "Dune"))
				fc.seed("2", cell("cf", "title", "Hyperion"))
				_, err := st.Get(testCtx(), []string{"1", "2"})
				require.NoError(t, err)
			},
			want: "hbase> get books rows=2\n",
		},
		{
			name: "an empty get traces nothing, since it makes no round trip",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				_, err := st.Get(testCtx(), nil)
				require.NoError(t, err)
			},
			want: "",
		},
		{
			name: "scan names the table",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				require.NoError(t, st.ScanBatches(testCtx(), func(map[string]any) error { return nil }))
			},
			want: "hbase> scan books\n",
		},
		{
			name: "a pushed predicate marks the scan filtered",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				pred := predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"}
				require.NoError(t, st.ScanFiltered(testCtx(), pred, func(map[string]any) error { return nil }))
			},
			want: "hbase> scan books (filtered)\n",
		},
		{
			name: "an unpushable predicate traces a plain scan",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				pred := predicate.Cmp{Path: []string{"cf", "year"}, Op: predicate.Gt, Value: float64(1)}
				require.NoError(t, st.ScanFiltered(testCtx(), pred, func(map[string]any) error { return nil }))
			},
			want: "hbase> scan books\n",
		},
		{
			name: "the raw path traces its verb",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				fc.seed("1", cell("cf", "title", "Dune"))
				_, err := st.Query(testCtx(), []string{"get", "books", "1"})
				require.NoError(t, err)
			},
			want: "hbase> exec get\n",
		},
		{
			name: "an upsert traces the pre-read and the write",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				_, err := st.Put(testCtx(),
					[]query.Record{{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}}},
					query.Upsert)
				require.NoError(t, err)
			},
			want: "hbase> exists books\nhbase> put books\n",
		},
		{
			name: "an insert-only write traces the write alone",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				_, err := st.Put(testCtx(),
					[]query.Record{{Key: "1", Value: map[string]any{"cf": map[string]any{"title": "Dune"}}}},
					query.InsertOnly)
				require.NoError(t, err)
			},
			want: "hbase> put books\n",
		},
		{
			name: "a delete traces the pre-read and the removal",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				fc.seed("1", cell("cf", "title", "Dune"))
				_, err := st.Delete(testCtx(), []string{"1"})
				require.NoError(t, err)
			},
			want: "hbase> exists books\nhbase> delete books\n",
		},
		{
			name: "clear traces once, not once per row",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				fc.seed("1", cell("cf", "title", "Dune"))
				fc.seed("2", cell("cf", "title", "Hyperion"))
				require.NoError(t, st.Clear(testCtx()))
			},
			want: "hbase> clear books\n",
		},
		{
			name: "drop traces the table it removes",
			run: func(t *testing.T, st *Store, fc *fakeClient) {
				require.NoError(t, st.Drop(testCtx()))
			},
			want: "hbase> drop books\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			var buf bytes.Buffer
			st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
			st.trace = &buf
			tt.run(t, st, fc)
			require.Equal(t, tt.want, buf.String())
		})
	}
}

func TestTraceStaysSilentWhenDisabled(t *testing.T) {
	fc := newFakeClient()
	fc.seed("1", cell("cf", "title", "Dune"))
	st := newFakeStore(fc, &fakeAdmin{}, "books", typeMap{}, ctAuto)
	// trace is nil: every traced operation must run without writing anywhere.
	require.Nil(t, st.trace)
	_, err := st.Get(testCtx(), []string{"1"})
	require.NoError(t, err)
	require.NoError(t, st.ScanBatches(testCtx(), func(map[string]any) error { return nil }))
}
