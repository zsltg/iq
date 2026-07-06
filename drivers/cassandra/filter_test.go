package cassandra

import (
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// bookCols is a small column index used across the pushdown tests.
func bookCols() map[string]gocql.TypeInfo {
	return map[string]gocql.TypeInfo{
		"id":     nativeType(gocql.TypeInt),
		"author": nativeType(gocql.TypeText),
		"year":   nativeType(gocql.TypeInt),
	}
}

func TestToCQL(t *testing.T) {
	tests := []struct {
		name      string
		pred      predicate.Node
		wantWhere string
		wantArgs  []any
		wantOK    bool
	}{
		{
			name:      "equality on a text column",
			pred:      predicate.Eq{Path: []string{"author"}, Value: "Tolkien"},
			wantWhere: `"author" = ?`,
			wantArgs:  []any{"Tolkien"},
			wantOK:    true,
		},
		{
			name:      "equality coerces number to column type",
			pred:      predicate.Eq{Path: []string{"year"}, Value: float64(1937)},
			wantWhere: `"year" = ?`,
			wantArgs:  []any{1937},
			wantOK:    true,
		},
		{
			name:   "equality on an unknown column is not pushable",
			pred:   predicate.Eq{Path: []string{"missing"}, Value: "x"},
			wantOK: false,
		},
		{
			name:   "equality on a nested path is not pushable",
			pred:   predicate.Eq{Path: []string{"meta", "author"}, Value: "x"},
			wantOK: false,
		},
		{
			name: "same-column or collapses to IN",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"year"}, Value: float64(1937)},
				predicate.Eq{Path: []string{"year"}, Value: float64(1954)},
			},
			wantWhere: `"year" IN (?,?)`,
			wantArgs:  []any{1937, 1954},
			wantOK:    true,
		},
		{
			name: "or across columns is not pushable",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"year"}, Value: float64(1937)},
				predicate.Eq{Path: []string{"author"}, Value: "Tolkien"},
			},
			wantOK: false,
		},
		{
			name: "and pushes the compilable conjuncts and drops the rest",
			pred: predicate.And{
				predicate.Eq{Path: []string{"author"}, Value: "Tolkien"},
				predicate.Cmp{Path: []string{"year"}, Op: predicate.Gt, Value: float64(1900)},
			},
			wantWhere: `"author" = ?`,
			wantArgs:  []any{"Tolkien"},
			wantOK:    true,
		},
		{
			name:   "range alone is not pushable",
			pred:   predicate.Cmp{Path: []string{"year"}, Op: predicate.Lt, Value: float64(2000)},
			wantOK: false,
		},
		{
			name:   "regex is not pushable",
			pred:   predicate.Regex{Path: []string{"author"}, Pattern: "^T"},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args, ok := toCQL(bookCols(), tt.pred)
			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			require.Equal(t, tt.wantWhere, where)
			require.Equal(t, tt.wantArgs, args)
		})
	}
}

func TestToCQLDescribeModeSkipsValidation(t *testing.T) {
	// A nil column index (the connection-free --explain path) pushes an equality on
	// any column name and passes the literal through unchanged.
	where, args, ok := toCQL(nil, predicate.Eq{Path: []string{"whatever"}, Value: "v"})
	require.True(t, ok)
	require.Equal(t, `"whatever" = ?`, where)
	require.Equal(t, []any{"v"}, args)
}
