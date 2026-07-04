package query_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// fakeOpener resolves names to fake KVStores for the source() function.
type fakeOpener struct {
	stores map[string]query.KVStore
}

func (o fakeOpener) Open(_ context.Context, name string) (query.KVStore, error) {
	st, ok := o.stores[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q", name)
	}
	return st, nil
}

func runCross(opener query.SourceOpener, filter string, opts query.RunOptions) ([]any, error) {
	var out []any
	err := query.NewCrossEngine(opener).Run(context.Background(), filter, opts, func(v any) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

func TestUsesSource(t *testing.T) {
	tests := []struct {
		filter string
		want   bool
	}{
		{`source("a"; ".x")`, true},
		{`INDEX(source("a";".[]"); .id) as $u | source("b";".[]")`, true},
		{`.foo | source("b")`, true},
		{`.foo | .bar`, false},
		{`keys`, false},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			got, err := query.UsesSource(tt.filter)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	t.Run("parse error", func(t *testing.T) {
		_, err := query.UsesSource("@@@ not jq")
		require.Error(t, err)
	})
}

func TestSourceFunction(t *testing.T) {
	opener := fakeOpener{stores: map[string]query.KVStore{
		"users": &fakeKV{
			values: map[string]any{
				"u1": map[string]any{"id": "u1", "name": "Ann"},
				"u2": map[string]any{"id": "u2", "name": "Bob"},
			},
			scanKeys: []string{"u1", "u2"},
		},
		"orders": &fakeKV{
			values:   map[string]any{"o1": map[string]any{"userId": "u1", "total": 10}},
			scanKeys: []string{"o1"},
		},
	}}

	t.Run("streams a named source", func(t *testing.T) {
		out, err := runCross(opener, `source("users"; ".[]") | .name`, query.RunOptions{})
		require.NoError(t, err)
		require.Equal(t, []any{"Ann", "Bob"}, out) // .[] over an object is key-sorted
	})

	t.Run("bounded read from a named source", func(t *testing.T) {
		out, err := runCross(opener, `source("users"; ".u1.name")`, query.RunOptions{})
		require.NoError(t, err)
		require.Equal(t, []any{"Ann"}, out)
	})

	t.Run("cross-source join in one filter", func(t *testing.T) {
		filter := `INDEX(source("users"; ".[]"); .id) as $u
			| source("orders"; ".[]")
			| {name: $u[.userId].name, total}`
		out, err := runCross(opener, filter, query.RunOptions{})
		require.NoError(t, err)
		require.Equal(t, []any{map[string]any{"name": "Ann", "total": 10}}, out)
	})

	t.Run("whole source needs unbounded", func(t *testing.T) {
		_, err := runCross(opener, `source("users")`, query.RunOptions{})
		require.ErrorIs(t, err, query.ErrScanNotAllowed)

		out, err := runCross(opener, `source("users") | keys`, query.RunOptions{Unbounded: true})
		require.NoError(t, err)
		require.Equal(t, []any{[]any{"u1", "u2"}}, out)
	})

	t.Run("unknown source errors", func(t *testing.T) {
		_, err := runCross(opener, `source("nope"; ".")`, query.RunOptions{})
		require.ErrorContains(t, err, `source "nope"`)
		require.ErrorContains(t, err, "unknown source")
	})

	t.Run("non-string filter argument errors", func(t *testing.T) {
		_, err := runCross(opener, `source("users"; .x)`, query.RunOptions{})
		require.ErrorContains(t, err, "filter must be a string")
	})

	t.Run("non-string name errors", func(t *testing.T) {
		_, err := runCross(opener, `source(1; ".")`, query.RunOptions{})
		require.ErrorContains(t, err, "name must be a string")
	})
}

func TestCrossEngineErrors(t *testing.T) {
	opener := fakeOpener{stores: map[string]query.KVStore{}}

	t.Run("blank filter", func(t *testing.T) {
		_, err := runCross(opener, "", query.RunOptions{})
		require.ErrorIs(t, err, query.ErrEmptyExpression)
	})

	t.Run("parse error", func(t *testing.T) {
		_, err := runCross(opener, "@@@ not jq", query.RunOptions{})
		require.ErrorContains(t, err, "parse expression")
	})
}
