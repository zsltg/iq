package dynamodb

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

func TestCompilePushable(t *testing.T) {
	tests := []struct {
		name        string
		pred        predicate.Node
		wantDisplay string
		wantNames   int
		wantValues  int
	}{
		{
			name:        "eq string",
			pred:        predicate.Eq{Path: []string{"author"}, Value: "kleppmann"},
			wantDisplay: "author = ?",
			wantNames:   1,
			wantValues:  1,
		},
		{
			name:        "eq number",
			pred:        predicate.Eq{Path: []string{"year"}, Value: 2017.0},
			wantDisplay: "year = ?",
			wantNames:   1,
			wantValues:  1,
		},
		{
			name:        "eq bool",
			pred:        predicate.Eq{Path: []string{"inprint"}, Value: true},
			wantDisplay: "inprint = ?",
			wantNames:   1,
			wantValues:  1,
		},
		{
			name:        "exists",
			pred:        predicate.Exists{Path: []string{"tags"}},
			wantDisplay: "attribute_exists(tags)",
			wantNames:   1,
			wantValues:  0,
		},
		{
			name:        "not exists",
			pred:        predicate.NotExists{Path: []string{"tags"}},
			wantDisplay: "attribute_not_exists(tags)",
			wantNames:   1,
			wantValues:  0,
		},
		{
			name: "and of two eq",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"b"}, Value: "y"},
			},
			wantDisplay: "(a = ? AND b = ?)",
			wantNames:   2,
			wantValues:  2,
		},
		{
			name: "and drops the unpushable conjunct without orphan placeholders",
			pred: predicate.And{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
			},
			wantDisplay: "a = ?",
			wantNames:   1,
			wantValues:  1,
		},
		{
			name: "or of two eq",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"a"}, Value: "x"},
				predicate.Eq{Path: []string{"b"}, Value: "y"},
			},
			wantDisplay: "(a = ? OR b = ?)",
			wantNames:   2,
			wantValues:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := compile(tt.pred)
			require.True(t, ok)
			require.Equal(t, tt.wantDisplay, f.display)
			require.Len(t, f.names, tt.wantNames)
			require.Len(t, f.values, tt.wantValues)
			// Every placeholder used in the expression is defined, and none is orphaned
			// (DynamoDB rejects unused ExpressionAttributeNames/Values).
			for ph := range f.names {
				require.Contains(t, f.expr, ph)
			}
			for ph := range f.values {
				require.Contains(t, f.expr, ph)
			}
		})
	}
}

func TestCompileNotPushable(t *testing.T) {
	tests := []struct {
		name string
		pred predicate.Node
	}{
		{"range comparison never pushes", predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0}},
		{"nested path", predicate.Eq{Path: []string{"author", "name"}, Value: "x"}},
		{"eq nil under-returns", predicate.Eq{Path: []string{"x"}, Value: nil}},
		{"not-equal never pushes", predicate.Ne{Path: []string{"x"}, Value: "y"}},
		{"regex never pushes", predicate.Regex{Path: []string{"x"}, Pattern: "^a"}},
		{"size never pushes", predicate.Size{Path: []string{"x"}, N: 3}},
		{"empty or", predicate.Or{}},
		{"or with one unpushable branch drops whole", predicate.Or{
			predicate.Eq{Path: []string{"a"}, Value: "x"},
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
		}},
		{"and all unpushable", predicate.And{
			predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: 2000.0},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := compile(tt.pred)
			require.False(t, ok)
		})
	}
}

func TestCompileExprShape(t *testing.T) {
	// The execution expression uses aliased placeholders for the value and, for a
	// single equality, references the same index for name and value.
	f, ok := compile(predicate.Eq{Path: []string{"author"}, Value: "x"})
	require.True(t, ok)
	require.Equal(t, "#n1 = :v1", f.expr)
	require.Equal(t, map[string]string{"#n1": "author"}, f.names)
	require.Contains(t, f.values, ":v1")
}
