package elasticsearch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// testStore builds a Store with a fixed exact-field map, standing in for the mapping
// read at open, so the pushdown translation is testable without a server.
func testStore() *Store {
	return &Store{
		exact: map[string]exactField{
			"author": {path: "author.keyword", class: stringClass}, // text field's keyword sub-field
			"year":   {path: "year", class: numberClass},
			"active": {path: "active", class: boolClass},
			// "title" is analyzed text with no keyword sub-field: never in the map.
		},
	}
}

func TestToQuery(t *testing.T) {
	tests := []struct {
		name      string
		pred      predicate.Node
		wantQuery map[string]any
		wantPush  bool
	}{
		{
			name:      "equality on a keyword sub-field pushes a term",
			pred:      predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
			wantQuery: map[string]any{"term": map[string]any{"author.keyword": "Herbert"}},
			wantPush:  true,
		},
		{
			name:      "equality on a numeric field pushes a term",
			pred:      predicate.Eq{Path: []string{"year"}, Value: 1965.0},
			wantQuery: map[string]any{"term": map[string]any{"year": 1965.0}},
			wantPush:  true,
		},
		{
			name:      "equality on a boolean field pushes a term",
			pred:      predicate.Eq{Path: []string{"active"}, Value: true},
			wantQuery: map[string]any{"term": map[string]any{"active": true}},
			wantPush:  true,
		},
		{
			name:     "equality with a null literal does not push",
			pred:     predicate.Eq{Path: []string{"author"}, Value: nil},
			wantPush: false,
		},
		{
			name:     "equality on an unmapped analyzed field does not push",
			pred:     predicate.Eq{Path: []string{"title"}, Value: "Dune"},
			wantPush: false,
		},
		{
			name:     "equality with a mismatched literal type does not push",
			pred:     predicate.Eq{Path: []string{"year"}, Value: "1965"},
			wantPush: false,
		},
		{
			name:     "equality on a nested path does not push",
			pred:     predicate.Eq{Path: []string{"meta", "author"}, Value: "x"},
			wantPush: false,
		},
		{
			name:      "existence pushes an exists query",
			pred:      predicate.Exists{Path: []string{"author"}},
			wantQuery: map[string]any{"exists": map[string]any{"field": "author"}},
			wantPush:  true,
		},
		{
			name:      "non-existence pushes must_not exists",
			pred:      predicate.NotExists{Path: []string{"author"}},
			wantQuery: map[string]any{"bool": map[string]any{"must_not": map[string]any{"exists": map[string]any{"field": "author"}}}},
			wantPush:  true,
		},
		{
			name:     "range does not push",
			pred:     predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			wantPush: false,
		},
		{
			name:     "inequality does not push",
			pred:     predicate.Ne{Path: []string{"author"}, Value: "Herbert"},
			wantPush: false,
		},
		{
			name: "and keeps the pushable conjunct and drops the rest",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			wantQuery: map[string]any{"term": map[string]any{"author.keyword": "Herbert"}},
			wantPush:  true,
		},
		{
			name: "and of two pushables is a bool must",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Exists{Path: []string{"year"}},
			},
			wantQuery: map[string]any{"bool": map[string]any{"must": []any{
				map[string]any{"term": map[string]any{"author.keyword": "Herbert"}},
				map[string]any{"exists": map[string]any{"field": "year"}},
			}}},
			wantPush: true,
		},
		{
			name: "and of no pushables does not push",
			pred: predicate.And{
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
				predicate.Ne{Path: []string{"author"}, Value: "x"},
			},
			wantPush: false,
		},
		{
			name: "or of pushables is a bool should",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"year"}, Value: 1965.0},
			},
			wantQuery: map[string]any{"bool": map[string]any{
				"should": []any{
					map[string]any{"term": map[string]any{"author.keyword": "Herbert"}},
					map[string]any{"term": map[string]any{"year": 1965.0}},
				},
				"minimum_should_match": 1,
			}},
			wantPush: true,
		},
		{
			name: "or with an unpushable branch does not push",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			wantPush: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, push := testStore().toQuery(tt.pred)
			require.Equal(t, tt.wantPush, push)
			if tt.wantPush {
				require.Equal(t, tt.wantQuery, q)
			}
		})
	}
}

func TestExactFieldsFrom(t *testing.T) {
	// Two index mappings, as an alias fanning out to both would report. "author"
	// agrees (keyword in both) and is kept; "code" is exact in both but as different
	// classes (keyword vs long) and is dropped; "body" is analyzed text in one and so
	// is dropped.
	raw := map[string]indexMapping{
		"idx_a": {Mappings: indexProperties{Properties: map[string]mappingProperty{
			"author": {Type: "keyword"},
			"code":   {Type: "keyword"},
			"body":   {Type: "keyword"},
		}}},
		"idx_b": {Mappings: indexProperties{Properties: map[string]mappingProperty{
			"author": {Type: "keyword"},
			"code":   {Type: "long"},
			"body":   {Type: "text"},
		}}},
	}
	got := exactFieldsFrom(raw)

	require.Equal(t, exactField{path: "author", class: stringClass}, got["author"])
	_, hasCode := got["code"]
	require.False(t, hasCode, "a field exact as different classes across mappings is dropped")
	_, hasBody := got["body"]
	require.False(t, hasBody, "a field analyzed as text in any mapping is dropped")
}

func TestMappingPropertyExact(t *testing.T) {
	tests := []struct {
		name string
		prop mappingProperty
		want exactField
		ok   bool
	}{
		{name: "keyword", prop: mappingProperty{Type: "keyword"}, want: exactField{path: "f", class: stringClass}, ok: true},
		{name: "long", prop: mappingProperty{Type: "long"}, want: exactField{path: "f", class: numberClass}, ok: true},
		{name: "double", prop: mappingProperty{Type: "double"}, want: exactField{path: "f", class: numberClass}, ok: true},
		{name: "boolean", prop: mappingProperty{Type: "boolean"}, want: exactField{path: "f", class: boolClass}, ok: true},
		{name: "ip", prop: mappingProperty{Type: "ip"}, want: exactField{path: "f", class: stringClass}, ok: true},
		{
			name: "text with keyword sub-field",
			prop: mappingProperty{Type: "text", Fields: map[string]mappingProperty{"keyword": {Type: "keyword"}}},
			want: exactField{path: "f.keyword", class: stringClass},
			ok:   true,
		},
		{name: "text without keyword sub-field", prop: mappingProperty{Type: "text"}, ok: false},
		{name: "date is not exact", prop: mappingProperty{Type: "date"}, ok: false},
		{name: "object is not a scalar", prop: mappingProperty{Properties: map[string]json.RawMessage{"a": nil}}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.prop.exact("f")
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.Equal(t, tt.want, got)
			}
		})
	}
}
