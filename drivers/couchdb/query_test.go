package couchdb

import (
	"context"
	"testing"

	"github.com/go-kivik/kivik/v4/driver"
	"github.com/go-kivik/kivik/v4/mockdb"
	"github.com/stretchr/testify/require"
)

// queryRecorder answers one _find request and records it.
func queryRecorder(t *testing.T, mdb *mockdb.DB, reply driver.Rows) *any {
	t.Helper()
	var got any
	mdb.ExpectFind().WillExecute(func(ctx context.Context, query any, _ driver.Options) (driver.Rows, error) {
		requireKeyed(t, ctx)
		got = query
		return reply, nil
	})
	return &got
}

func TestQuerySendsTheRequestAsGiven(t *testing.T) {
	// A bare selector is wrapped as {"selector": ...}. A request that already has a
	// selector goes out unchanged. Both carry the caller's context.
	tests := []struct {
		name string
		arg  string
		want map[string]any
	}{
		{
			name: "bare selector",
			arg:  `{"year":2017}`,
			want: map[string]any{"selector": map[string]any{"year": float64(2017)}},
		},
		{
			name: "full request",
			arg:  `{"selector":{"year":2017},"limit":5}`,
			want: map[string]any{"selector": map[string]any{"year": float64(2017)}, "limit": float64(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, mdb := newMockStore(t, 10, 1)
			got := queryRecorder(t, mdb, mockdb.NewRows().Final())

			_, err := st.Query(keyedCtx(), []string{tt.arg})

			require.NoError(t, err)
			require.Equal(t, tt.want, queryMap(t, *got))
		})
	}
}

func TestQueryRejectsAWrongArgumentCount(t *testing.T) {
	// Exactly one argument is required. No request is sent for zero or two.
	for _, args := range [][]string{nil, {`{"a":1}`, `{"b":2}`}} {
		st, mock, _ := newMockStore(t, 10, 0)

		res, err := st.Query(context.Background(), args)

		require.Nil(t, res)
		require.EqualError(t, err, "couchdb raw expects one JSON Mango query document")
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

func TestQueryReturnsAnEmptyDocsListAndTheBookmark(t *testing.T) {
	// No match gives an empty list, not nil. The bookmark of the reply is surfaced.
	st, _, mdb := newMockStore(t, 10, 1)
	queryRecorder(t, mdb, withBookmark(mockdb.NewRows(), "bm-9"))

	res, err := st.Query(keyedCtx(), []string{`{"a":1}`})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"docs": []any{}, "bookmark": "bm-9"}, res)
}
