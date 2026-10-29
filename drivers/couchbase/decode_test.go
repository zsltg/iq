package couchbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/jqtruth"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// decodeDocs hold fractional and out-of-range numbers, as the query service returns them.
var decodeDocs = map[string]string{
	"frac":  `{"price":1.5}`,
	"trail": `{"price":1.50}`,
	"half":  `{"price":0.5}`,
	"int":   `{"price":7}`,
	"big":   `{"price":2e308}`,
	"neg":   `{"price":-1e999}`,
	"text":  `{"price":"1.5"}`,
}

var decodeFilters = []string{
	`.[] | select(.price == "1.5")`,
	`.[] | select(.price == 1.5)`,
	`.[] | select(.price == "2e308")`,
	`.[] | select(.price > "1")`,
	`.[] | select(.price >= "1.5")`,
	`.[] | select(.price < "2")`,
	`.[] | select(.price > 1)`,
	`.[] | select(.price >= 7)`,
	`.[] | select(.price <= 1)`,
	`.[] | select(.price == "1.5" or .price == "2e308")`,
	`.[] | select(.price > "1" and .price == 7)`,
}

// TestPrefilterMatcherKeepsWhatJQKeeps runs each filter through the matcher of a store
// and through the full jq filter. The matcher never drops a document that jq keeps.
func TestPrefilterMatcherKeepsWhatJQKeeps(t *testing.T) {
	modes := map[string]numfmt.DecimalMode{
		"auto": numfmt.DecimalAuto, "number": numfmt.DecimalNumber, "string": numfmt.DecimalString,
	}
	for name, mode := range modes {
		for _, filter := range decodeFilters {
			t.Run(name+"/"+filter, func(t *testing.T) {
				requirePrefilterKeepsWhatJQKeeps(t, mode, filter)
			})
		}
	}
}

// requirePrefilterKeepsWhatJQKeeps fails when the matcher of a store in the mode drops
// a decodeDocs document that the filter keeps. A store with no matcher drops nothing.
func requirePrefilterKeepsWhatJQKeeps(t *testing.T, mode numfmt.DecimalMode, filter string) {
	t.Helper()
	pred, err := jqtruth.Predicate(filter)
	require.NoError(t, err)
	matcher := (&Store{decimal: mode}).prefilterMatcher(pred)
	if matcher == nil {
		return
	}
	for key, raw := range decodeDocs {
		keeps, err := jqtruth.Keeps(filter, decodeValue([]byte(raw), mode))
		require.NoError(t, err)
		require.Falsef(t, keeps && matcher.Match([]byte(raw)) == rawpred.CannotMatch,
			"the prefilter dropped %s, which jq keeps", key)
	}
}

// TestWhereLeavesOutNodesThatHideDecodedNumbers pins that the WHERE never holds a leaf
// that compares a stored number where jq compares the decoded string. The server holds the
// number, so such a leaf can drop a row that jq keeps.
func TestWhereLeavesOutNodesThatHideDecodedNumbers(t *testing.T) {
	tests := []struct {
		name       string
		node       predicate.Node
		mode       numfmt.DecimalMode
		wantNarrow bool
	}{
		{"eq number text, string mode", predicate.Eq{Path: []string{"price"}, Value: "1.5"}, numfmt.DecimalString, false},
		{"eq number text, auto mode", predicate.Eq{Path: []string{"price"}, Value: "1.5"}, numfmt.DecimalAuto, true},
		{"eq out of range text, auto mode", predicate.Eq{Path: []string{"price"}, Value: "2e308"}, numfmt.DecimalAuto, false},
		{"eq plain text, string mode", predicate.Eq{Path: []string{"name"}, Value: "ada"}, numfmt.DecimalString, true},
		{"gt number, string mode", predicate.Cmp{Path: []string{"price"}, Op: predicate.Gt, Value: 1.0}, numfmt.DecimalString, false},
		{"gt string, string mode", predicate.Cmp{Path: []string{"price"}, Op: predicate.Gt, Value: "1"}, numfmt.DecimalString, false},
		{"gt number, auto mode", predicate.Cmp{Path: []string{"price"}, Op: predicate.Gt, Value: 1.0}, numfmt.DecimalAuto, true},
		{"lt string, string mode", predicate.Cmp{Path: []string{"price"}, Op: predicate.Lt, Value: "1"}, numfmt.DecimalString, true},
		{"or with a hidden leaf", predicate.Or{
			predicate.Eq{Path: []string{"price"}, Value: "1.5"},
			predicate.Eq{Path: []string{"price"}, Value: "ada"},
		}, numfmt.DecimalString, false},
		{"and keeps the safe leaf", predicate.And{
			predicate.Eq{Path: []string{"price"}, Value: "1.5"},
			predicate.Eq{Path: []string{"name"}, Value: "ada"},
		}, numfmt.DecimalString, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, _, narrow := toWhere(tt.node, tt.mode)
			require.Equal(t, tt.wantNarrow, narrow, "where: %q", where)
			if tt.name == "and keeps the safe leaf" {
				require.NotContains(t, where, "$p1", "only the safe leaf is bound")
				require.Contains(t, where, "`name`")
				require.NotContains(t, where, "`price`")
			}
		})
	}
}

// TestExactWhereIsFalseForHiddenNodes pins that a WHERE that leaves a leaf out is not
// exact, so the prefilter runs and the plan does not claim exactness.
func TestExactWhereIsFalseForHiddenNodes(t *testing.T) {
	pred := predicate.Eq{Path: []string{"price"}, Value: "1.5"}
	require.True(t, exactWhere(pred, numfmt.DecimalAuto))
	require.False(t, exactWhere(pred, numfmt.DecimalString))
}
