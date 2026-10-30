package couchbase

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

func TestParseQueryArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStmt   string
		wantParams map[string]any
		wantErr    string
	}{
		{name: "no args", args: nil, wantErr: "couchbase raw expects"},
		{name: "three args", args: []string{"a", "{}", "c"}, wantErr: "couchbase raw expects"},
		{name: "statement only", args: []string{"SELECT 1"}, wantStmt: "SELECT 1"},
		{name: "with params", args: []string{"SELECT $x", `{"x":1}`}, wantStmt: "SELECT $x", wantParams: map[string]any{"x": float64(1)}},
		{name: "bad params", args: []string{"SELECT 1", "{"}, wantErr: "parse couchbase query parameters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt, params, err := parseQueryArgs(tt.args)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantStmt, stmt)
			require.Equal(t, tt.wantParams, params)
		})
	}
}

func TestCollectRows(t *testing.T) {
	st := &Store{}
	out, err := st.collectRows(context.Background(), &fakeRows{})
	require.NoError(t, err)
	require.NotNil(t, out, "no rows give an empty slice, not nil")
	require.Empty(t, out)

	out, err = st.collectRows(context.Background(), &fakeRows{rows: []string{`{"n":1}`, `2`}})
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{"n": 1}, 2}, out)

	errRow := errors.New("row failed")
	_, err = st.collectRows(context.Background(), &fakeRows{rows: []string{`1`}, rowErr: errRow})
	require.ErrorIs(t, err, errRow)
	require.ErrorContains(t, err, "couchbase query")

	errStream := errors.New("stream failed")
	_, err = st.collectRows(context.Background(), &fakeRows{rows: []string{`1`}, streamErr: errStream})
	require.ErrorIs(t, err, errStream)
	require.ErrorContains(t, err, "couchbase query")
}

func TestCollectRowsLimit(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		wantRows  int
		wantReads int
	}{
		{"no limit reads all", 0, 5, 5},
		{"limit stops early", 2, 2, 2},
		{"limit above the result", 9, 5, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := &fakeRows{rows: []string{`1`, `2`, `3`, `4`, `5`}}
			ctx := query.WithRowLimit(context.Background(), tt.limit)

			got, err := (&Store{}).collectRows(ctx, rows)

			require.NoError(t, err)
			require.Len(t, got, tt.wantRows)
			require.Equal(t, tt.wantReads, rows.next)
		})
	}
}
