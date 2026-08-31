package query_test

import (
	"context"
	"errors"
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
		// Naming the step is done by wrapping, so gojq's own error has to stay
		// reachable underneath; formatting it in reads the same and severs
		// errors.Is/As for every caller.
		require.Error(t, errors.Unwrap(err), "the parse error must stay unwrappable")
	})

	t.Run("runtime error", func(t *testing.T) {
		_, err := runCombine(`$a | error("boom")`, []string{"$a"}, []any{[]any{1.0}})
		require.ErrorContains(t, err, "run combine")
		require.Error(t, errors.Unwrap(err), "the jq runtime error must stay unwrappable")
	})

	t.Run("emit error stops the run", func(t *testing.T) {
		sentinel := errors.New("emit boom")
		var seen int
		err := query.NewCombiner().Run(context.Background(), "$a[]", []string{"$a"}, []any{[]any{"x", "y"}},
			func(any) error {
				seen++
				return sentinel
			})
		require.ErrorIs(t, err, sentinel)
		require.Equal(t, 1, seen, "the run stops at the first refusal, it does not drain the iterator")
	})
}

// TestNewCombinerReturnsCombiner pins the constructor: Run has a pointer receiver
// that never dereferences it, so a nil Combiner would work by accident today and
// break the moment the type gains a field.
func TestNewCombinerReturnsCombiner(t *testing.T) {
	require.NotNil(t, query.NewCombiner())
}
