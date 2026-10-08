package couchdb

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// toSelectorCase is one row of the toSelector table.
type toSelectorCase struct {
	name          string
	pred          predicate.Node
	wantSelector  map[string]any
	wantNarrowing bool
}

// equalitySelectorCases lists the rows for equality.
func equalitySelectorCases() []toSelectorCase {
	return []toSelectorCase{
		{
			name:          "equality on a scalar",
			pred:          predicate.Eq{Path: []string{"author"}, Value: "Kleppmann"},
			wantSelector:  map[string]any{"author": "Kleppmann"},
			wantNarrowing: true,
		},
		{
			name:          "equality on a nested path",
			pred:          predicate.Eq{Path: []string{"meta", "genre"}, Value: "go"},
			wantSelector:  map[string]any{"meta.genre": "go"},
			wantNarrowing: true,
		},
		{
			name: "equality to null also matches an absent field",
			pred: predicate.Eq{Path: []string{"a"}, Value: nil},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"a": nil},
				map[string]any{"a": map[string]any{"$exists": false}},
			}},
			wantNarrowing: true,
		},
	}
}

// rangeSelectorCases lists the rows for range comparisons.
func rangeSelectorCases() []toSelectorCase {
	return []toSelectorCase{
		{
			name: "greater-than adds higher-ranked types",
			pred: predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2015.0},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"year": map[string]any{"$gt": 2015.0}},
				map[string]any{"year": map[string]any{"$type": "string"}},
				map[string]any{"year": map[string]any{"$type": "array"}},
				map[string]any{"year": map[string]any{"$type": "object"}},
			}},
			wantNarrowing: true,
		},
		{
			name: "less-than adds lower-ranked types and a missing field",
			pred: predicate.Cmp{Path: []string{"year"}, Op: predicate.Lt, Value: 2015.0},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"year": map[string]any{"$lt": 2015.0}},
				map[string]any{"year": map[string]any{"$type": "null"}},
				map[string]any{"year": map[string]any{"$type": "boolean"}},
				map[string]any{"year": map[string]any{"$exists": false}},
			}},
			wantNarrowing: true,
		},
		{
			name: "greater-or-equal adds the same higher-ranked types under $gte",
			pred: predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2015.0},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"year": map[string]any{"$gte": 2015.0}},
				map[string]any{"year": map[string]any{"$type": "string"}},
				map[string]any{"year": map[string]any{"$type": "array"}},
				map[string]any{"year": map[string]any{"$type": "object"}},
			}},
			wantNarrowing: true,
		},
		{
			name: "less-or-equal adds the lower-ranked types and a missing field",
			pred: predicate.Cmp{Path: []string{"year"}, Op: predicate.Le, Value: 2015.0},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"year": map[string]any{"$lte": 2015.0}},
				map[string]any{"year": map[string]any{"$type": "null"}},
				map[string]any{"year": map[string]any{"$type": "boolean"}},
				map[string]any{"year": map[string]any{"$exists": false}},
			}},
			wantNarrowing: true,
		},
	}
}

// existenceSelectorCases lists the rows for existence, regex and size.
func existenceSelectorCases() []toSelectorCase {
	return []toSelectorCase{
		{
			name:          "exists",
			pred:          predicate.Exists{Path: []string{"tags"}},
			wantSelector:  map[string]any{"tags": map[string]any{"$exists": true}},
			wantNarrowing: true,
		},
		{
			name:          "not-exists",
			pred:          predicate.NotExists{Path: []string{"tags"}},
			wantSelector:  map[string]any{"tags": map[string]any{"$exists": false}},
			wantNarrowing: true,
		},
		{
			name:          "byte-safe regex narrows",
			pred:          predicate.Regex{Path: []string{"a"}, Pattern: "^x"},
			wantSelector:  map[string]any{"a": map[string]any{"$regex": "^x"}},
			wantNarrowing: true,
		},
		{
			name: "size narrows as a size-plus-type superset",
			pred: predicate.Size{Path: []string{"a"}, N: 2},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"a": map[string]any{"$size": 2}},
				map[string]any{"a": map[string]any{"$type": "string"}},
				map[string]any{"a": map[string]any{"$type": "object"}},
				map[string]any{"a": map[string]any{"$type": "number"}},
			}},
			wantNarrowing: true,
		},
		{
			name: "size zero also matches null and a missing field",
			pred: predicate.Size{Path: []string{"a"}, N: 0},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"a": map[string]any{"$size": 0}},
				map[string]any{"a": map[string]any{"$type": "string"}},
				map[string]any{"a": map[string]any{"$type": "object"}},
				map[string]any{"a": map[string]any{"$type": "number"}},
				map[string]any{"a": map[string]any{"$type": "null"}},
				map[string]any{"a": map[string]any{"$exists": false}},
			}},
			wantNarrowing: true,
		},
	}
}

// compositeSelectorCases lists the rows for And and Or of leaf nodes.
func compositeSelectorCases() []toSelectorCase {
	return []toSelectorCase{
		{
			name: "and of two pushable clauses",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Exists{Path: []string{"b"}},
			},
			wantSelector: map[string]any{"$and": []any{
				map[string]any{"a": "x"},
				map[string]any{"b": map[string]any{"$exists": true}},
			}},
			wantNarrowing: true,
		},
		{
			name: "and drops a non-pushable conjunct and keeps the rest",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Ne{Path: []string{"b"}, Value: "y"},
			},
			wantSelector:  map[string]any{"a": "x"},
			wantNarrowing: true,
		},
		{
			name: "or of two pushable clauses",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"a"}, Value: "y"},
			},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"a": "x"},
				map[string]any{"a": "y"},
			}},
			wantNarrowing: true,
		},
		{
			name: "or of a single pushable branch narrows to a one-arm $or",
			pred: predicate.Or{predicate.Eq{Path: []string{"a"}, Value: "x"}},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"a": "x"},
			}},
			wantNarrowing: true,
		},
	}
}

// nestedSelectorCases lists the rows for And and Or inside each other.
func nestedSelectorCases() []toSelectorCase {
	return []toSelectorCase{
		{
			name: "an and inside an or keeps its shape",
			pred: predicate.Or{
				predicate.And{
					predicate.Eq{Path: []string{"a"}, Value: "x"},
					predicate.Eq{Path: []string{"b"}, Value: "y"},
				},
				predicate.Eq{Path: []string{"c"}, Value: "z"},
			},
			wantSelector: map[string]any{"$or": []any{
				map[string]any{"$and": []any{
					map[string]any{"a": "x"},
					map[string]any{"b": "y"},
				}},
				map[string]any{"c": "z"},
			}},
			wantNarrowing: true,
		},
		{
			name: "an or inside an and keeps its shape",
			pred: predicate.And{
				predicate.Or{
					predicate.Eq{Path: []string{"a"}, Value: "x"},
					predicate.Eq{Path: []string{"b"}, Value: "y"},
				},
				predicate.Eq{Path: []string{"c"}, Value: "z"},
			},
			wantSelector: map[string]any{"$and": []any{
				map[string]any{"$or": []any{
					map[string]any{"a": "x"},
					map[string]any{"b": "y"},
				}},
				map[string]any{"c": "z"},
			}},
			wantNarrowing: true,
		},
	}
}

// unpushableSelectorCases lists the rows for nodes that do not narrow.
func unpushableSelectorCases() []toSelectorCase {
	return []toSelectorCase{
		{
			name: "and of only non-pushable clauses does not narrow",
			pred: predicate.And{
				predicate.Ne{Path: []string{"a"}, Value: "x"},
				predicate.Regex{Path: []string{"b"}, Pattern: "y.z"}, // unescaped dot: not byte-safe
			},
			wantNarrowing: false,
		},
		{
			name: "or with a non-pushable branch does not narrow",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Ne{Path: []string{"b"}, Value: "y"},
			},
			wantNarrowing: false,
		},
		{
			name:          "an empty or has nothing to push and does not narrow",
			pred:          predicate.Or{},
			wantNarrowing: false,
		},
		{
			name:          "an empty and has nothing to push and does not narrow",
			pred:          predicate.And{},
			wantNarrowing: false,
		},
		{
			name:          "not-equal does not narrow",
			pred:          predicate.Ne{Path: []string{"a"}, Value: "x"},
			wantNarrowing: false,
		},
		{
			name:          "case-insensitive regex does not narrow",
			pred:          predicate.Regex{Path: []string{"a"}, Pattern: "^x", Flags: "i"},
			wantNarrowing: false,
		},
		{
			name:          "regex with an unescaped dot does not narrow",
			pred:          predicate.Regex{Path: []string{"a"}, Pattern: "a.c"},
			wantNarrowing: false,
		},
		{
			name:          "elem-match does not narrow",
			pred:          predicate.ElemMatch{Path: []string{"a"}, Cond: predicate.Eq{Path: []string{"b"}, Value: 1.0}},
			wantNarrowing: false,
		},
	}
}

func TestToSelector(t *testing.T) {
	tests := slices.Concat(
		equalitySelectorCases(),
		rangeSelectorCases(),
		existenceSelectorCases(),
		compositeSelectorCases(),
		nestedSelectorCases(),
		unpushableSelectorCases(),
	)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, narrowing := toSelector(tt.pred)
			require.Equal(t, tt.wantNarrowing, narrowing)
			if tt.wantNarrowing {
				require.Equal(t, tt.wantSelector, sel)
			} else {
				require.Nil(t, sel)
			}
		})
	}
}

func TestByteSafeRegex(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"empty", "", true},
		{"ascii literals", "abc", true},
		{"anchors and quantifier", "^ab+c$", true},
		{"escaped dot is a literal", `a\.c`, true},
		{"escaped letter then literal", `\wa`, true},
		{"escaped letter then unescaped dot", `\wa.`, false},
		{"DEL is an ascii byte", "a\x7fb", true},
		{"ascii shorthand classes", `\d\w\s`, true},
		{"positive class", "[abc]", true},
		{"caret before a class is not negation", "^[a]", true},
		{"trailing open bracket", "a[", true},
		{"caret not preceded by a bracket is an anchor", "a^b", true},
		{"class opener at the very end", "ab[", true},
		{"pattern ends with a negated class opener", "[^", false},
		{"escaped backslash then letter", `a\\d`, true},
		{"unescaped dot", "a.c", false},
		{"negated class", "[^a]", false},
		{"negated digit class", `\D`, false},
		{"negated word class", `\W`, false},
		{"negated space class", `\S`, false},
		{"trailing backslash", `ab\`, false},
		{"non-ascii byte", "café", false},
		{"continuation byte exactly 0x80", "a\x80b", false},
		{"escaped backslash then bare dot", `\\.`, false},
		{"escaped backslash then letter D", `\\D`, true},
		{"escaped bracket then caret", `\[^a]`, true},
		{"class opener then escaped caret", `[\^a]`, true},
		{"negated shorthand inside a class", `[\D]`, false},
		{"class opener then negated class", `[[^a]`, false},
		{"non-ascii byte after a backslash", "\\é", false},
		{"even backslashes then a trailing one", `a\\\`, false},
		{"only an escaped backslash", `\\`, true},
		{"escaped backslash then negated class", `\\[^`, false},
		{"escaped dot then bare dot", `\..`, false},
		{"negated space class after a literal", `a\Sb`, false},
		{"negated word class after a literal", `a\Wb`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, byteSafeRegex(tt.pattern))
		})
	}
}
