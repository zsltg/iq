package couchbase

import (
	"context"
	"maps"
	"regexp"
	"sort"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// TestExactWhere pins the exactness gate: exactWhere is true only when toWhere pushes the
// whole predicate into a WHERE that returns precisely jq's matching set, so the
// client-side prefilter can be bypassed as pure overhead. Unlike Elasticsearch, Couchbase
// pushes equality (including a null literal), the ISTYPE-widened range, and existence all
// exactly; exactness is lost only when a conjunct is dropped, a node is refused (!=,
// regex, length), or a field path cannot be referenced.
func TestExactWhere(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
		want bool
	}{
		{"equality is exact", predicate.Eq{Path: []string{"year"}, Value: 2017}, true},
		{"null equality is exact", predicate.Eq{Path: []string{"a"}, Value: nil}, true},
		{"nested equality is exact", predicate.Eq{Path: []string{"a", "b"}, Value: "x"}, true},
		{"equality on a backtick path is not exact", predicate.Eq{Path: []string{"a`b"}, Value: 1}, false},
		{"range is exact (ISTYPE-widened)", predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5.0}, true},
		{"existence is exact", predicate.Exists{Path: []string{"a"}}, true},
		{"non-existence is exact", predicate.NotExists{Path: []string{"a"}}, true},
		{"inequality is not exact", predicate.Ne{Path: []string{"a"}, Value: 1}, false},
		{"regex is not exact", predicate.Regex{Path: []string{"a"}, Pattern: "x"}, false},
		{"length is not exact", predicate.Size{Path: []string{"a"}, N: 2}, false},
		{
			name: "and of two exact nodes is exact",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: 1},
				predicate.Cmp{Path: []string{"b"}, Op: predicate.Gt, Value: 5.0},
			},
			want: true,
		},
		{
			name: "and with a dropped regex conjunct is not exact",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: 1},
				predicate.Regex{Path: []string{"b"}, Pattern: "x"},
			},
			want: false,
		},
		{
			name: "and with a refused inequality conjunct is not exact",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: 1},
				predicate.Ne{Path: []string{"b"}, Value: 2},
			},
			want: false,
		},
		{"empty and is not exact", predicate.And{}, false},
		{
			name: "or of two exact equalities is exact",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1},
				predicate.Eq{Path: []string{"a"}, Value: 2},
			},
			want: true,
		},
		{
			name: "or with a non-exact branch is not exact",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: 1},
				predicate.Ne{Path: []string{"b"}, Value: 2},
			},
			want: false,
		},
		{"empty or is not exact", predicate.Or{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, exactWhere(tt.pred))
		})
	}
}

// prefilterDocs is the mixed corpus the prefilter equivalence test seeds, keyed by
// document ID. lang is a plain string (equality-pushable), year is type-mixed (an int in
// most documents, a string in "4") to exercise the ISTYPE-widened range, and note is
// absent from "4" so the absent-field class reads a clean jq null.
func prefilterDocs() map[string]map[string]any {
	return map[string]map[string]any{
		"1": {"lang": "go", "year": 2015, "note": "a"},
		"2": {"lang": "c", "year": 1978, "note": "b"},
		"3": {"lang": "go", "year": 1970, "note": "a"},
		"4": {"lang": "rust", "year": "recent"},
		"5": {"lang": "go", "year": 2020, "note": "b"},
	}
}

// TestScanFilteredPrefilterEquivalence is the driver-level invariant: for each predicate
// class, re-applying the full predicate to ScanFiltered's output selects exactly the same
// keys as applying it to a plain ScanBatches, and the prefilter's checked/skipped counts
// match whether the class is expected to engage it. Classes: a control equality and a
// type-mixed range (both exact pushes, prefilter bypassed — checked 0); a partial-push And
// whose regex conjunct the server drops (prefilter catches the residual); an inequality
// and an inequality on an absent field (both fall back to a full scan, the prefilter
// engages over every row). It runs against the couchbase container only, skipping under
// -short.
func TestScanFilteredPrefilterEquivalence(t *testing.T) {
	fixture := prefilterDocs()
	st := seedCollection(t, fixture)
	ctx := skipShort(t)
	// A page size of one so a row the prefilter drops produces an empty page, exercising
	// (and guarding) the "never hand fn an empty batch" path.
	st.pageSize = 1

	baseline := collectScan(ctx, t, st.ScanBatches)
	require.Len(t, baseline, len(fixture))

	tests := []struct {
		name string
		pred predicate.Node
		// wantKeys is the set the full predicate selects; wantChecked is how many rows the
		// prefilter must evaluate (0 exactly when the scan bypasses it); wantSkipped is how
		// many of those it must drop.
		wantKeys    []string
		wantChecked int
		wantSkipped int
	}{
		{
			// Exact push: the WHERE returns only matches, so the prefilter is bypassed
			// entirely — it evaluates nothing (checked 0), not merely skips nothing.
			name:        "control eq (exact push, prefilter bypassed)",
			pred:        predicate.Eq{Path: []string{"lang"}, Value: "go"},
			wantKeys:    []string{"1", "3", "5"},
			wantChecked: 0,
			wantSkipped: 0,
		},
		{
			// The ISTYPE-widened range captures jq's cross-type ordering exactly (the
			// string year in "4" ranks above every number), so it too bypasses the prefilter.
			name:        "type-mixed range (exact widened push, prefilter bypassed)",
			pred:        predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2000.0},
			wantKeys:    []string{"1", "4", "5"},
			wantChecked: 0,
			wantSkipped: 0,
		},
		{
			// Partial push: the server narrows on lang to three rows, and the prefilter
			// evaluates exactly those three and drops the one failing the regex residual.
			name: "partial-push and (server narrows, prefilter catches the residual)",
			pred: predicate.And{
				predicate.Eq{Path: []string{"lang"}, Value: "go"},
				predicate.Regex{Path: []string{"note"}, Pattern: "^a$"},
			},
			wantKeys:    []string{"1", "3"},
			wantChecked: 3,
			wantSkipped: 1,
		},
		{
			// Refused node: != cannot push, so the scan falls back to a full walk and the
			// prefilter evaluates all five rows, dropping the one that equals the value.
			name:        "inequality (fallback + prefilter)",
			pred:        predicate.Ne{Path: []string{"year"}, Value: 2015.0},
			wantKeys:    []string{"2", "3", "4", "5"},
			wantChecked: 5,
			wantSkipped: 1,
		},
		{
			// Absent field: note is missing from "4"; jq reads it as null (!= "b" holds),
			// which the prefilter must keep — it drops only the two rows that equal "b".
			name:        "inequality on an absent field (fallback + prefilter)",
			pred:        predicate.Ne{Path: []string{"note"}, Value: "b"},
			wantKeys:    []string{"1", "3", "4"},
			wantChecked: 5,
			wantSkipped: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkedBefore, skippedBefore := st.prefilterChecked, st.prefilterSkipped
			filtered := collectScan(ctx, t, func(c context.Context, fn func(map[string]any) error) error {
				return st.ScanFiltered(c, tt.pred, fn)
			})
			require.Equal(t, tt.wantChecked, st.prefilterChecked-checkedBefore,
				"prefilter must evaluate exactly the expected number of rows (0 iff bypassed)")
			require.Equal(t, tt.wantSkipped, st.prefilterSkipped-skippedBefore,
				"prefilter must skip exactly the expected number of documents for this class")

			// Every surviving key is in the full scan and decodes identically.
			for k, v := range filtered {
				require.Contains(t, baseline, k)
				require.Equal(t, baseline[k], v, "prefilter must not change a surviving decode")
			}
			// The prefilter drops every provable non-match the WHERE let through and keeps
			// every match, so the delivered survivors are exactly the matching set — a
			// leaked skip (a dropped row still delivered) or an over-drop fails here.
			require.Equal(t, tt.wantKeys, sortedAnyKeys(filtered),
				"the prefilter's survivors are exactly the matching set")
			// The load-bearing equivalence: the full predicate over each scan's output
			// selects the same keys, and that set is what the corpus dictates.
			require.Equal(t, matchingKeys(baseline, tt.pred), matchingKeys(filtered, tt.pred),
				"re-filtering ScanFiltered and ScanBatches must agree")
			require.Equal(t, tt.wantKeys, sortedKeys(matchingKeys(filtered, tt.pred)))
		})
	}
}

// TestScanFilteredSkipsMidPage pins that a row the prefilter drops only drops that row:
// the walk must read the rows behind it in the same page. The equivalence test above
// pages one row at a time, where dropping the row and ending the page have the same
// result, so this case puts every document in one page and drops the first of them.
func TestScanFilteredSkipsMidPage(t *testing.T) {
	fixture := prefilterDocs()
	st := seedCollection(t, fixture)
	ctx := skipShort(t)
	require.GreaterOrEqual(t, st.pageSize, len(fixture), "the whole corpus must land in one page")

	// != 2015 drops document "1", the first by ID, and keeps the four behind it.
	got := collectScan(ctx, t, func(c context.Context, fn func(map[string]any) error) error {
		return st.ScanFiltered(c, predicate.Ne{Path: []string{"year"}, Value: 2015.0}, fn)
	})
	require.Equal(t, []string{"2", "3", "4", "5"}, sortedAnyKeys(got),
		"the rows behind a dropped row must still be delivered")
	require.Equal(t, len(fixture), st.prefilterChecked, "every row is evaluated")
	require.Equal(t, 1, st.prefilterSkipped, "exactly the one provable non-match is dropped")
}

// collectScan drains a scan into one {key: value} map, asserting the driver never hands a
// caller an empty batch — the guard that a page emptied by the prefilter is dropped.
func collectScan(ctx context.Context, t *testing.T, scan func(context.Context, func(map[string]any) error) error) map[string]any {
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
// engine's full-jq re-filter over a scan's output.
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
// must never diverge from. It uses gojq.Compare as the oracle for jq's cross-type
// ordering, so the type-mixed and absent-field classes are judged exactly as jq would.
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
	case predicate.Regex:
		// jq test() matches only over a string field; a non-string reads as a non-match
		// for the corpus this oracle judges (flags are unused by the test predicates).
		s, ok := lookup(v, n.Path).(string)
		if !ok {
			return false
		}
		return regexp.MustCompile(n.Pattern).MatchString(s)
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

// sortedAnyKeys returns a scan output's keys in sorted order for a stable assertion.
func sortedAnyKeys(batch map[string]any) []string {
	out := make([]string, 0, len(batch))
	for k := range batch {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
