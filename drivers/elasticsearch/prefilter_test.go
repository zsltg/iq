package elasticsearch

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"sort"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// TestExactPush pins the exactness gate: exactPush is true only when the whole
// predicate translates to a query that returns precisely jq's matching set, so the
// client-side prefilter can be bypassed as pure overhead. Only a term-pushed equality
// is exact; existence, ranges, negations, a dropped conjunct, or any non-exact branch
// keep the prefilter.
func TestExactPush(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
		want bool
	}{
		{"eq on a keyword sub-field is exact", predicate.Eq{Path: []string{"author"}, Value: "Herbert"}, true},
		{"eq on a numeric field is exact", predicate.Eq{Path: []string{"year"}, Value: 1965.0}, true},
		{"eq on a boolean field is exact", predicate.Eq{Path: []string{"active"}, Value: true}, true},
		{"eq with a null literal is not exact", predicate.Eq{Path: []string{"author"}, Value: nil}, false},
		{"eq on an unmapped field is not exact", predicate.Eq{Path: []string{"title"}, Value: "Dune"}, false},
		{"eq with a mismatched literal type is not exact", predicate.Eq{Path: []string{"year"}, Value: "1965"}, false},
		{"eq on a nested path is not exact", predicate.Eq{Path: []string{"meta", "author"}, Value: "x"}, false},
		{"existence is not exact", predicate.Exists{Path: []string{"author"}}, false},
		{"non-existence is not exact", predicate.NotExists{Path: []string{"author"}}, false},
		{"range is not exact", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}, false},
		{"inequality is not exact", predicate.Ne{Path: []string{"author"}, Value: "x"}, false},
		{
			name: "and of two exact equalities is exact",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"year"}, Value: 1965.0},
			},
			want: true,
		},
		{
			name: "and with a dropped conjunct is not exact",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			want: false,
		},
		{
			name: "and with a non-exact existence conjunct is not exact",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Exists{Path: []string{"year"}},
			},
			want: false,
		},
		{"empty and is not exact", predicate.And{}, false},
		{
			name: "or of two exact equalities is exact",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"year"}, Value: 1965.0},
			},
			want: true,
		},
		{
			name: "or with a non-exact branch is not exact",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			want: false,
		},
		{"empty or is not exact", predicate.Or{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, testStore().exactPush(tt.pred))
		})
	}
}

// TestReferencesInjectedID pins the _id-trap walker: it is true exactly when some
// field path in the tree is rooted at the injected _id field, which the raw _source
// does not carry, so the prefilter must be disabled for that scan.
func TestReferencesInjectedID(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
		want bool
	}{
		{"eq on _id references it", predicate.Eq{Path: []string{"_id"}, Value: "1"}, true},
		{"eq on a normal field does not", predicate.Eq{Path: []string{"author"}, Value: "x"}, false},
		{"ne on _id references it", predicate.Ne{Path: []string{"_id"}, Value: "1"}, true},
		{"cmp on _id references it", predicate.Cmp{Path: []string{"_id"}, Op: predicate.Gt, Value: "a"}, true},
		{"exists on _id references it", predicate.Exists{Path: []string{"_id"}}, true},
		{"not-exists on _id references it", predicate.NotExists{Path: []string{"_id"}}, true},
		{"regex on _id references it", predicate.Regex{Path: []string{"_id"}, Pattern: "^x"}, true},
		{"size on _id references it", predicate.Size{Path: []string{"_id"}, N: 1}, true},
		{"a deeper path rooted at _id references it", predicate.Eq{Path: []string{"_id", "sub"}, Value: "x"}, true},
		{"_id not as the first segment does not", predicate.Eq{Path: []string{"meta", "_id"}, Value: "x"}, false},
		{"an empty path does not", predicate.Eq{Path: nil, Value: "x"}, false},
		{
			name: "and with an _id conjunct references it",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "x"},
				predicate.Eq{Path: []string{"_id"}, Value: "1"},
			},
			want: true,
		},
		{
			name: "and without an _id conjunct does not",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "x"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 1.0},
			},
			want: false,
		},
		{
			name: "or with an _id branch references it",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"author"}, Value: "x"},
				predicate.Cmp{Path: []string{"_id"}, Op: predicate.Gt, Value: "a"},
			},
			want: true,
		},
		{
			// The Cond disjunct alone: an element condition rooted at _id, with a normal
			// array path.
			name: "elem-match whose condition references _id",
			pred: predicate.ElemMatch{Path: []string{"tags"}, Cond: predicate.Eq{Path: []string{"_id"}, Value: "x"}},
			want: true,
		},
		{
			// The Path disjunct alone: the array path is rooted at _id, the condition is
			// normal — so each side of the ElemMatch `||` is independently load-bearing.
			name: "elem-match whose path is rooted at _id",
			pred: predicate.ElemMatch{Path: []string{"_id"}, Cond: predicate.Eq{Path: []string{"name"}, Value: "x"}},
			want: true,
		},
		{
			// NoneMatch, Path disjunct alone.
			name: "none-match rooted at _id references it",
			pred: predicate.NoneMatch{Path: []string{"_id"}, Cond: predicate.Eq{Path: []string{"x"}, Value: 1.0}},
			want: true,
		},
		{
			// NoneMatch, Cond disjunct alone.
			name: "none-match whose condition references _id",
			pred: predicate.NoneMatch{Path: []string{"tags"}, Cond: predicate.Eq{Path: []string{"_id"}, Value: "x"}},
			want: true,
		},
		{
			name: "elem-match on a normal path and condition does not",
			pred: predicate.ElemMatch{Path: []string{"tags"}, Cond: predicate.Eq{Path: []string{"name"}, Value: "x"}},
			want: false,
		},
		{
			name: "none-match on a normal path and condition does not",
			pred: predicate.NoneMatch{Path: []string{"tags"}, Cond: predicate.Eq{Path: []string{"name"}, Value: "x"}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, referencesInjectedID(tt.pred))
		})
	}
}

// prefilterDocs is the mixed corpus the prefilter equivalence test seeds: lang is an
// exact keyword (term-pushable), year is a long with ignore_malformed so a string year
// still lands in _source (the type-mixed Cmp class), and note is unmapped (its Eq
// cannot push). Every document also carries the injected _id once decoded.
func prefilterDocs() []map[string]any {
	return []map[string]any{
		{"_id": "1", "lang": "go", "year": 2015, "note": "a"},
		{"_id": "2", "lang": "c", "year": 1978, "note": "b"},
		{"_id": "3", "lang": "go", "year": 1970, "note": "a"},
		{"_id": "4", "lang": "rust", "year": "recent", "note": "c"},
		{"_id": "5", "lang": "go", "year": 2020, "note": "b"},
	}
}

// seedPrefilterIndex creates an index with an explicit mapping (lang keyword, year long
// with ignore_malformed, dynamic:false so note stays unmapped), indexes prefilterDocs,
// and returns a store-under-test opened after the seed so it reads the mapping. The
// mapping is what makes lang term-pushable and note not, which the classes below rely
// on.
func seedPrefilterIndex(t *testing.T, baseURL string) *Store {
	t.Helper()
	ctx := skipShort(t)
	name := indexName(t)

	admin, err := Open(ctx, baseURL, name, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	_ = admin.Drop(ctx)
	t.Cleanup(func() { _ = admin.Drop(context.Background()) })

	mapping, err := json.Marshal(map[string]any{
		"mappings": map[string]any{
			"dynamic": false,
			"properties": map[string]any{
				"lang": map[string]any{"type": "keyword"},
				"year": map[string]any{"type": "long", "ignore_malformed": true},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, admin.request(ctx, "create index", http.MethodPut, "/"+name, mapping, nil))

	docs := prefilterDocs()
	recs := make([]query.Record, len(docs))
	for i, d := range docs {
		id, _ := d["_id"].(string)
		recs[i] = query.Record{Key: id, Type: "document", Value: d}
	}
	_, err = admin.Put(ctx, recs, query.Upsert)
	require.NoError(t, err)

	st, err := Open(ctx, baseURL, name, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	// A page size of one so a document the prefilter drops produces an empty page,
	// exercising (and guarding) the "never hand fn an empty batch" path.
	st.pageSize = 1
	return st
}

// runPrefilterEquivalence is the shared driver-level invariant for both backends: for
// each predicate class, re-applying the full predicate to ScanFiltered's output selects
// exactly the same keys as applying it to a plain ScanBatches, and the prefilter's skip
// count matches whether the class is expected to engage it. Classes: a control equality
// (exact push, prefilter bypassed), a range on the type-mixed field and an equality on
// the unmapped field (both fall back to a full scan and the prefilter engages), a
// predicate on the injected _id field (prefilter disabled — the raw _source lacks it),
// and a partial-push And (server narrows on the pushable conjunct, the prefilter catches
// the residual).
func runPrefilterEquivalence(t *testing.T, baseURL string) {
	st := seedPrefilterIndex(t, baseURL)
	ctx := skipShort(t)

	baseline := collectFrom(ctx, t, st.ScanBatches)
	require.Len(t, baseline, 5)

	tests := []struct {
		name string
		pred predicate.Node
		// wantKeys is the set the full predicate selects; wantChecked is how many hits
		// the prefilter must evaluate (0 exactly when the scan bypasses it); wantSkipped
		// is how many of those it must drop.
		wantKeys    []string
		wantChecked int
		wantSkipped int
	}{
		// Exact push: the term query returns only matches, so the prefilter is bypassed
		// entirely — it evaluates nothing (checked 0), not merely skips nothing.
		{"control eq (exact push, prefilter bypassed)", predicate.Eq{Path: []string{"lang"}, Value: "go"}, []string{"1", "3", "5"}, 0, 0},
		// Fallbacks: no server narrowing, so the prefilter evaluates all five hits.
		{"cmp on the type-mixed field (fallback + prefilter)", predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2000.0}, []string{"1", "4", "5"}, 5, 2},
		{"eq on the unmapped field (fallback + prefilter)", predicate.Eq{Path: []string{"note"}, Value: "a"}, []string{"1", "3"}, 5, 3},
		// Injected _id: the raw _source lacks it, so the prefilter is disabled (checked 0)
		// or it would wrongly drop the matching document.
		{"predicate on the injected _id (prefilter disabled)", predicate.Eq{Path: []string{"_id"}, Value: "1"}, []string{"1"}, 0, 0},
		{
			// Partial push: the server narrows on lang to three hits, and the prefilter
			// evaluates exactly those three and drops the one failing the year residual.
			name: "partial-push and (server narrows, prefilter catches the residual)",
			pred: predicate.And{
				predicate.Eq{Path: []string{"lang"}, Value: "go"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2000.0},
			},
			wantKeys:    []string{"1", "5"},
			wantChecked: 3,
			wantSkipped: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkedBefore, skippedBefore := st.prefilterChecked, st.prefilterSkipped
			filtered := collectFrom(ctx, t, func(c context.Context, fn func(map[string]any) error) error {
				return st.ScanFiltered(c, tt.pred, fn)
			})
			require.Equal(t, tt.wantChecked, st.prefilterChecked-checkedBefore,
				"prefilter must evaluate exactly the expected number of hits (0 iff bypassed)")
			require.Equal(t, tt.wantSkipped, st.prefilterSkipped-skippedBefore,
				"prefilter must skip exactly the expected number of documents for this class")

			// Every kept key is in the full scan and decodes identically.
			for k, v := range filtered {
				require.Contains(t, baseline, k)
				require.Equal(t, baseline[k], v, "prefilter must not change a surviving decode")
			}
			// The load-bearing equivalence: the full predicate over each scan's output
			// selects the same keys, and that set is what the corpus dictates.
			require.Equal(t, matchingKeys(baseline, tt.pred), matchingKeys(filtered, tt.pred),
				"re-filtering ScanFiltered and ScanBatches must agree")
			require.Equal(t, tt.wantKeys, sortedKeys(matchingKeys(filtered, tt.pred)))
		})
	}
}

func TestScanFilteredPrefilterEquivalence(t *testing.T) {
	runPrefilterEquivalence(t, testURL())
}

func TestOpenSearchScanFilteredPrefilterEquivalence(t *testing.T) {
	runPrefilterEquivalence(t, requireOpenSearch(t))
}

// collectFrom drains a scan into one {key: value} map, asserting the driver never hands
// a caller an empty batch — the guard that a page emptied by the prefilter is dropped.
func collectFrom(ctx context.Context, t *testing.T, scan func(context.Context, func(map[string]any) error) error) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, scan(ctx, func(batch map[string]any) error {
		require.NotEmpty(t, batch, "a scan never yields an empty batch")
		maps.Copy(out, batch)
		return nil
	}))
	return out
}

// matchingKeys returns the keys whose decoded value satisfies pred, standing in for the
// engine's full-jq re-filter over a scan's output. It uses gojq.Compare as the oracle
// for jq's cross-type ordering, so the type-mixed and _id classes are judged exactly as
// jq would.
func matchingKeys(batch map[string]any, pred predicate.Node) map[string]bool {
	out := map[string]bool{}
	for k, v := range batch {
		if refMatch(v, pred) {
			out[k] = true
		}
	}
	return out
}

// refMatch evaluates pred over a decoded document, the reference semantics the prefilter
// must never diverge from.
func refMatch(v any, pred predicate.Node) bool {
	switch n := pred.(type) {
	case predicate.Eq:
		return gojq.Compare(lookup(v, n.Path), n.Value) == 0
	case predicate.Ne:
		return gojq.Compare(lookup(v, n.Path), n.Value) != 0
	case predicate.Cmp:
		c := gojq.Compare(lookup(v, n.Path), n.Value)
		switch n.Op {
		case predicate.Gt:
			return c > 0
		case predicate.Ge:
			return c >= 0
		case predicate.Lt:
			return c < 0
		default:
			return c <= 0
		}
	case predicate.Exists:
		return present(v, n.Path)
	case predicate.NotExists:
		return !present(v, n.Path)
	case predicate.And:
		for _, c := range n {
			if !refMatch(v, c) {
				return false
			}
		}
		return true
	case predicate.Or:
		for _, c := range n {
			if refMatch(v, c) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// lookup returns the value at path or nil (jq's null) when it is absent.
func lookup(v any, path []string) any {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[key]; !ok {
			return nil
		}
	}
	return cur
}

// present reports whether path resolves to a value.
func present(v any, path []string) bool {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[key]; !ok {
			return false
		}
	}
	return true
}

// sortedKeys returns a key set's members in sorted order for a stable assertion.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
