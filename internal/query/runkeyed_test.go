package query_test

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// collectKeyed runs RunKeyed and gathers the surviving key -> value pairs.
func collectKeyed(t *testing.T, store query.KVStore, src string, opts query.RunOptions) (map[string]any, error) {
	t.Helper()
	got := map[string]any{}
	err := query.NewJQEngine(store).RunKeyed(context.Background(), src, opts, func(k string, v any) error {
		got[k] = v
		return nil
	})
	return got, err
}

// values returns a keyed result's values sorted by key, so a keyed run can be
// compared against Run's value stream.
func values(m map[string]any) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

// TestRunKeyedMatchesRunPerPartition is the invariant the whole design rests on:
// a streamable filter distributes over any partition of the input, so running it
// against one single-entry root per item must give the same values as running it
// over a page, or over the whole keyspace at once. If that ever stops holding,
// keyed reads silently disagree with plain reads.
//
// Each residual exercises a construct that could plausibly reach past its own
// element — an aggregate, a generator-consumer, a binding, a length — and none
// of them may.
func TestRunKeyedMatchesRunPerPartition(t *testing.T) {
	keyspace := map[string]any{
		"a": map[string]any{"n": 1, "tags": []any{"x", "y"}},
		"b": map[string]any{"n": 2, "tags": []any{"z"}},
		"c": map[string]any{"n": 3, "tags": []any{}},
	}
	order := []string{"a", "b", "c"}
	filters := []string{
		`.[]`,
		`.[] | select(.n > 1)`,
		`.[] | .n`,
		`.[] | {n}`,
		`.[] | .tags | length`,
		`.[] | reduce .tags[] as $t (0; . + 1)`,
		`.[] | [foreach .tags[] as $t (0; . + 1; .)] | last`,
		`.[] | [limit(1; .tags[])] `,
		`.[] | try (.tags[0] | ascii_upcase) catch "none"`,
		`.[] | keys`,
		`.[] | .n as $n | {doubled: ($n * 2)}`,
	}
	for _, src := range filters {
		t.Run(src, func(t *testing.T) {
			// One page holding everything, versus one page per item.
			whole := &fakeKV{values: keyspace, scanKeys: order}
			paged := &fakeKV{values: keyspace, scanKeys: order, batchSize: 1}

			streamed, err := collect(t, whole, src, false)
			require.NoError(t, err)
			perPage, err := collect(t, paged, src, false)
			require.NoError(t, err)
			keyed, err := collectKeyed(t, &fakeKV{values: keyspace, scanKeys: order}, src, query.RunOptions{})
			require.NoError(t, err)

			require.Equal(t, streamed, perPage, "a streamable filter must not depend on page boundaries")
			require.Equal(t, streamed, values(keyed), "keyed output must equal the value stream")
		})
	}
}

func TestRunKeyedKeepsKeys(t *testing.T) {
	store := &fakeKV{
		values: map[string]any{
			"a": map[string]any{"n": 1, "active": true},
			"b": map[string]any{"n": 2, "active": false},
		},
		scanKeys: []string{"a", "b"},
	}

	got, err := collectKeyed(t, store, `.[] | select(.active)`, query.RunOptions{})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"n": 1, "active": true}}, got)
}

func TestRunKeyedProjectionKeepsKeys(t *testing.T) {
	store := &fakeKV{
		values:   map[string]any{"a": map[string]any{"n": 1, "s": "x"}},
		scanKeys: []string{"a"},
	}

	got, err := collectKeyed(t, store, `.[] | {n}`, query.RunOptions{})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"n": 1}}, got)
}

// TestRunKeyedRepeatedKeyOverwrites pins the weak scan guarantee: a keyspace
// resized mid-scan may repeat a key across pages, and that must overwrite as a
// plain read does, never look like one key fanning out into two values.
func TestRunKeyedRepeatedKeyOverwrites(t *testing.T) {
	store := &fakeKV{
		values:    map[string]any{"a": map[string]any{"n": 1}},
		scanKeys:  []string{"a", "a"},
		batchSize: 1,
	}

	got, err := collectKeyed(t, store, `.[]`, query.RunOptions{})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"n": 1}}, got)
}

func TestRunKeyedRejectsMultiValued(t *testing.T) {
	store := &fakeKV{
		values:   map[string]any{"a": map[string]any{"tags": []any{"x", "y"}}},
		scanKeys: []string{"a"},
	}

	_, err := collectKeyed(t, store, `.[] | .tags[]`, query.RunOptions{})

	require.ErrorIs(t, err, query.ErrMultiValued)
	require.ErrorContains(t, err, `key "a"`, "the offending key must be named")
}

func TestRunKeyedBounded(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		values map[string]any
		want   map[string]any
	}{
		{"present", `.["a"]`, map[string]any{"a": map[string]any{"n": 1}}, map[string]any{"a": map[string]any{"n": 1}}},
		{"projection", `.["a"].n`, map[string]any{"a": map[string]any{"n": 1}}, map[string]any{"a": 1}},
		{"select drops it", `.["a"] | select(.n > 5)`, map[string]any{"a": map[string]any{"n": 1}}, map[string]any{}},
		{"present holding null", `.["a"]`, map[string]any{"a": nil}, map[string]any{"a": nil}},
		{"absent emits nothing", `.["a"]`, map[string]any{}, map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeKV{values: tt.values}

			got, err := collectKeyed(t, store, tt.src, query.RunOptions{})

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestRunKeyedBoundedDistinguishesAbsentFromNull is the case the KVStore
// presence contract exists for: a key holding null is a value, a key that is not
// there is not, and a keyed read must be able to tell them apart even though jq
// renders both as null.
func TestRunKeyedBoundedDistinguishesAbsentFromNull(t *testing.T) {
	held, err := collectKeyed(t, &fakeKV{values: map[string]any{"a": nil}}, `.["a"]`, query.RunOptions{})
	require.NoError(t, err)
	absent, err := collectKeyed(t, &fakeKV{values: map[string]any{}}, `.["a"]`, query.RunOptions{})
	require.NoError(t, err)

	require.Contains(t, held, "a")
	require.NotContains(t, absent, "a")
}

func TestRunKeyedRejectsUnkeyable(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"holistic root", `.`},
		{"keys", `keys`},
		{"map collapses", `map(.n)`},
		{"binding sees the root", `.[] as $x | $x`},
		{"several keys", `.["a"] + .["b"]`},
		{"comma over keys", `.["a"], .["b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeKV{
				values:   map[string]any{"a": map[string]any{"n": 1}, "b": map[string]any{"n": 2}},
				scanKeys: []string{"a", "b"},
			}

			_, err := collectKeyed(t, store, tt.src, query.RunOptions{})

			require.ErrorIs(t, err, query.ErrNotKeyed)
		})
	}
}

// TestRunKeyedForwardsCtx pins that the caller's context reaches the store on
// both routes. A keyed read is the one a diff makes, and a diff is bounded by
// --timeout; a dropped context there would turn a bounded read into an
// unbounded one that no test would otherwise notice.
func TestRunKeyedForwardsCtx(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxMarker{}, "marker")

	t.Run("bounded route reaches Get", func(t *testing.T) {
		store := &fakeKV{values: map[string]any{"a": map[string]any{"n": 1}}}

		err := query.NewJQEngine(store).RunKeyed(ctx, `.["a"]`, query.RunOptions{}, func(string, any) error { return nil })

		require.NoError(t, err)
		require.NotNil(t, store.gotGetCtx, "Get must receive a non-nil context")
		require.Equal(t, "marker", store.gotGetCtx.Value(ctxMarker{}), "the caller's context must reach Get")
	})

	t.Run("pushed scan route reaches ScanFiltered", func(t *testing.T) {
		store := &filterKV{
			scanKeys: []string{"a"},
			values:   map[string]any{"a": map[string]any{"n": 1}},
		}

		err := query.NewJQEngine(store).RunKeyed(ctx, `.[] | select(.n == 1)`,
			query.RunOptions{Compile: true}, func(string, any) error { return nil })

		require.NoError(t, err)
		require.Equal(t, 1, store.filterCalls, "the predicate must be pushed")
		require.NotNil(t, store.gotCtx, "ScanFiltered must receive a non-nil context")
		require.Equal(t, "marker", store.gotCtx.Value(ctxMarker{}), "the caller's context must reach ScanFiltered")
	})
}

func TestRunKeyedRejectsEmptyExpression(t *testing.T) {
	_, err := collectKeyed(t, &fakeKV{}, "", query.RunOptions{})

	require.ErrorIs(t, err, query.ErrEmptyExpression)
}

// TestRunKeyedPushesPredicate pins that a keyed read still narrows the scan at
// the store: filtering inside a keyed read is meant to read less, not merely
// report less.
func TestRunKeyedPushesPredicate(t *testing.T) {
	store := &filterKV{
		values:   map[string]any{"a": map[string]any{"n": 1}, "b": map[string]any{"n": 2}},
		scanKeys: []string{"a", "b"},
	}

	got, err := collectKeyed(t, store, `.[] | select(.n == 1)`, query.RunOptions{Compile: true})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"n": 1}}, got)
	require.Equal(t, predicate.Eq{Path: []string{"n"}, Value: 1.0}, store.gotPred)
}
