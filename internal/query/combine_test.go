package query_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// runCombine drains the combiner into a slice.
func runCombine(src string, names []string, values []any) ([]any, error) {
	var out []any
	err := query.NewCombiner().Run(context.Background(), src, names, values, func(v any) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

func TestCombinerRun(t *testing.T) {
	t.Run("streams a bound source", func(t *testing.T) {
		out, err := runCombine("$a[]", []string{"$a"}, []any{[]any{"x", "y"}})
		require.NoError(t, err)
		require.Equal(t, []any{"x", "y"}, out)
	})

	t.Run("combines two bound sources", func(t *testing.T) {
		out, err := runCombine("$a[0] + $b[0]", []string{"$a", "$b"}, []any{[]any{1.0}, []any{2.0}})
		require.NoError(t, err)
		require.Equal(t, []any{3.0}, out)
	})

	t.Run("joins across sources", func(t *testing.T) {
		users := []any{map[string]any{"id": "u1", "name": "Ann"}}
		orders := []any{map[string]any{"user": "u1", "total": 10.0}}
		src := `($a | INDEX(.id)) as $u | $b[] | . + {name: $u[.user].name}`
		out, err := runCombine(src, []string{"$a", "$b"}, []any{users, orders})
		require.NoError(t, err)
		require.Equal(t, []any{map[string]any{"user": "u1", "total": 10.0, "name": "Ann"}}, out)
	})

	t.Run("blank program", func(t *testing.T) {
		_, err := runCombine("", nil, nil)
		require.ErrorIs(t, err, query.ErrEmptyExpression)
	})

	t.Run("parse error", func(t *testing.T) {
		_, err := runCombine("@@@ not jq", nil, nil)
		require.ErrorContains(t, err, "parse combine")
	})

	t.Run("runtime error", func(t *testing.T) {
		_, err := runCombine(`$a | error("boom")`, []string{"$a"}, []any{[]any{1.0}})
		require.ErrorContains(t, err, "run combine")
	})
}
