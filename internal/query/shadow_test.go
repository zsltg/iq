package query_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// A user definition named select/1 replaces the builtin, so the query keeps
// every record. The store must see a plain scan, not a pushed predicate, or it
// would drop the record with a = 2.
func TestJQEngineShadowedSelectKeepsEveryRecord(t *testing.T) {
	store := &filterKV{
		scanKeys: []string{"1", "2"},
		values: map[string]any{
			"1": map[string]any{"a": 1.0},
			"2": map[string]any{"a": 2.0},
		},
	}

	got, err := collectOpts(t, store, `def select(f): .; .[] | select(.a == 1) | .a`, query.RunOptions{Compile: true})

	require.NoError(t, err)
	require.ElementsMatch(t, []any{1.0, 2.0}, got)
	require.Zero(t, store.filterCalls, "a shadowed select must not push a predicate")
}
