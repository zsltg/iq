package couchbase

import (
	"context"
	"maps"
	"sort"
	"testing"

	"github.com/couchbase/gocb/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

func TestToWhere(t *testing.T) {
	tests := []struct {
		name       string
		node       predicate.Node
		wantWhere  string
		wantParams map[string]any
		wantNarrow bool
	}{
		{
			name:       "equality binds a parameter",
			node:       predicate.Eq{Path: []string{"year"}, Value: 2017},
			wantWhere:  "`t`.`year` = $p0",
			wantParams: map[string]any{"p0": 2017},
			wantNarrow: true,
		},
		{
			name:       "nested path is dotted and quoted",
			node:       predicate.Eq{Path: []string{"a", "b"}, Value: "x"},
			wantWhere:  "`t`.`a`.`b` = $p0",
			wantParams: map[string]any{"p0": "x"},
			wantNarrow: true,
		},
		{
			name:       "nil equality widens to null-or-missing",
			node:       predicate.Eq{Path: []string{"a"}, Value: nil},
			wantWhere:  "(`t`.`a` IS NULL OR `t`.`a` IS MISSING)",
			wantParams: map[string]any{},
			wantNarrow: true,
		},
		{
			name:       "greater-than number adds higher-ranked types",
			node:       predicate.Cmp{Path: []string{"a"}, Op: predicate.Gt, Value: 5},
			wantWhere:  "(`t`.`a` > $p0 OR ISSTRING(`t`.`a`) OR ISARRAY(`t`.`a`) OR ISOBJECT(`t`.`a`))",
			wantParams: map[string]any{"p0": 5},
			wantNarrow: true,
		},
		{
			name:       "greater-equal string adds array and object",
			node:       predicate.Cmp{Path: []string{"a"}, Op: predicate.Ge, Value: "m"},
			wantWhere:  "(`t`.`a` >= $p0 OR ISARRAY(`t`.`a`) OR ISOBJECT(`t`.`a`))",
			wantParams: map[string]any{"p0": "m"},
			wantNarrow: true,
		},
		{
			name:       "less-than number adds lower-ranked types and missing",
			node:       predicate.Cmp{Path: []string{"a"}, Op: predicate.Lt, Value: 5},
			wantWhere:  "(`t`.`a` < $p0 OR ISBOOLEAN(`t`.`a`) OR `t`.`a` IS NULL OR `t`.`a` IS MISSING)",
			wantParams: map[string]any{"p0": 5},
			wantNarrow: true,
		},
		{
			name:       "less-equal string adds number, boolean, null, missing",
			node:       predicate.Cmp{Path: []string{"a"}, Op: predicate.Le, Value: "m"},
			wantWhere:  "(`t`.`a` <= $p0 OR ISNUMBER(`t`.`a`) OR ISBOOLEAN(`t`.`a`) OR `t`.`a` IS NULL OR `t`.`a` IS MISSING)",
			wantParams: map[string]any{"p0": "m"},
			wantNarrow: true,
		},
		{
			name:       "exists is not-missing",
			node:       predicate.Exists{Path: []string{"a"}},
			wantWhere:  "`t`.`a` IS NOT MISSING",
			wantParams: map[string]any{},
			wantNarrow: true,
		},
		{
			name:       "not-exists is missing",
			node:       predicate.NotExists{Path: []string{"a"}},
			wantWhere:  "`t`.`a` IS MISSING",
			wantParams: map[string]any{},
			wantNarrow: true,
		},
		{
			name:       "and conjoins pushable children",
			node:       predicate.And{predicate.Eq{Path: []string{"a"}, Value: 1}, predicate.Exists{Path: []string{"b"}}},
			wantWhere:  "(`t`.`a` = $p0 AND `t`.`b` IS NOT MISSING)",
			wantParams: map[string]any{"p0": 1},
			wantNarrow: true,
		},
		{
			name:       "and of two equalities binds two distinct params",
			node:       predicate.And{predicate.Eq{Path: []string{"a"}, Value: 1}, predicate.Eq{Path: []string{"b"}, Value: 2}},
			wantWhere:  "(`t`.`a` = $p0 AND `t`.`b` = $p1)",
			wantParams: map[string]any{"p0": 1, "p1": 2},
			wantNarrow: true,
		},
		{
			name:       "and drops an unpushable conjunct",
			node:       predicate.And{predicate.Eq{Path: []string{"a"}, Value: 1}, predicate.Ne{Path: []string{"b"}, Value: 2}},
			wantWhere:  "`t`.`a` = $p0",
			wantParams: map[string]any{"p0": 1},
			wantNarrow: true,
		},
		{
			name:       "same-field equality or collapses to IN",
			node:       predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1}, predicate.Eq{Path: []string{"a"}, Value: 2}},
			wantWhere:  "`t`.`a` IN $p0",
			wantParams: map[string]any{"p0": []any{1, 2}},
			wantNarrow: true,
		},
		{
			// A null equality is not a value an IN list can hold: it widens to null-or-
			// missing, which the general disjunction renders and the IN collapse must
			// decline.
			name:       "or with a null equality stays a disjunction",
			node:       predicate.Or{predicate.Eq{Path: []string{"a"}, Value: nil}, predicate.Eq{Path: []string{"a"}, Value: 1}},
			wantWhere:  "((`t`.`a` IS NULL OR `t`.`a` IS MISSING) OR `t`.`a` = $p0)",
			wantParams: map[string]any{"p0": 1},
			wantNarrow: true,
		},
		{
			name:       "mixed or disjoins branches",
			node:       predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1}, predicate.Exists{Path: []string{"b"}}},
			wantWhere:  "(`t`.`a` = $p0 OR `t`.`b` IS NOT MISSING)",
			wantParams: map[string]any{"p0": 1},
			wantNarrow: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, params, narrow := toWhere(tt.node)
			require.Equal(t, tt.wantNarrow, narrow)
			require.Equal(t, tt.wantWhere, where)
			require.Equal(t, tt.wantParams, params)
		})
	}
}

func TestToWhereNotNarrowing(t *testing.T) {
	tests := []struct {
		name string
		node predicate.Node
	}{
		{name: "not-equal", node: predicate.Ne{Path: []string{"a"}, Value: 1}},
		{name: "regex", node: predicate.Regex{Path: []string{"a"}, Pattern: "x"}},
		{name: "size", node: predicate.Size{Path: []string{"a"}, N: 2}},
		{name: "and of only unpushable", node: predicate.And{predicate.Ne{Path: []string{"a"}, Value: 1}}},
		{name: "or with an unpushable branch", node: predicate.Or{predicate.Eq{Path: []string{"a"}, Value: 1}, predicate.Ne{Path: []string{"b"}, Value: 2}}},
		{name: "empty or", node: predicate.Or{}},
		{name: "backtick in path is refused", node: predicate.Eq{Path: []string{"a`b"}, Value: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, params, narrow := toWhere(tt.node)
			require.False(t, narrow)
			require.Empty(t, where)
			require.Nil(t, params)
		})
	}
}

// TestScanFilteredContextCancelled pins that the context ScanFiltered is handed flows all
// the way into the gocb query execution — the structural twin of the ScanBatches check,
// on ScanFiltered's delegation to scan. It uses a non-exact predicate (Ne) so the residual
// prefilter path is taken, and an explicit Cancel (never a timeout duration) so the
// cancellation is deterministic and immediate.
func TestScanFilteredContextCancelled(t *testing.T) {
	st := seedCollection(t, books())
	ctx := skipShort(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancel before the scan issues its first query

	err := st.ScanFiltered(cctx, predicate.Ne{Path: []string{"year"}, Value: 2015.0}, func(map[string]any) error { return nil })
	require.Error(t, err, "a cancelled context must surface as an error, not a silent full scan")
	// gocb surfaces a cancelled request as its own sentinel (ErrRequestCanceled), not a
	// wrapped context.Canceled, so match the driver's actual error rather than ctx.Err().
	require.ErrorIs(t, err, gocb.ErrRequestCanceled)
}

// TestScanFilteredParity checks the server-side pushdown returns a superset of the true
// matches (the client re-runs the full jq, so extra rows are safe, missing rows are a
// bug) and that an unpushable predicate falls back to a full scan.
func TestScanFilteredParity(t *testing.T) {
	fixture := books()
	st := seedCollection(t, fixture)
	ctx := skipShort(t)

	tests := []struct {
		name        string
		pred        predicate.Node
		wantPresent []string
	}{
		{"equality", predicate.Eq{Path: []string{"year"}, Value: 2017}, []string{"2"}},
		{"range", predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2017}, []string{"2", "3"}},
		{"existence", predicate.Exists{Path: []string{"tags"}}, []string{"1", "2", "3"}},
		{"or in", predicate.Or{
			predicate.Eq{Path: []string{"year"}, Value: 2015},
			predicate.Eq{Path: []string{"year"}, Value: 2018},
		}, []string{"1", "3"}},
		{"unpushable falls back to full scan", predicate.Ne{Path: []string{"year"}, Value: 2015}, []string{"1", "2", "3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]any{}
			require.NoError(t, st.ScanFiltered(ctx, tt.pred, func(batch map[string]any) error {
				maps.Copy(got, batch)
				return nil
			}))
			ids := make([]string, 0, len(got))
			for k := range got {
				ids = append(ids, k)
			}
			sort.Strings(ids)
			for _, want := range tt.wantPresent {
				require.Contains(t, ids, want, "pushdown dropped a true match")
			}
			require.LessOrEqual(t, len(ids), len(fixture), "pushdown returned more than the whole collection")
		})
	}
}
