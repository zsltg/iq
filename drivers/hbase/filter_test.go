package hbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

func TestToFilterPushable(t *testing.T) {
	types := typeMap{cellKey("cf", "age"): ctLong}
	tests := []struct {
		name string
		pred predicate.Node
		want bool
	}{
		{
			name: "text column equality",
			pred: predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
			want: true,
		},
		{
			name: "declared long equality",
			pred: predicate.Eq{Path: []string{"cf", "age"}, Value: float64(30)},
			want: true,
		},
		{
			name: "and of equalities",
			pred: predicate.And{
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"cf", "age"}, Value: float64(30)},
			},
			want: true,
		},
		{
			name: "and with one pushable conjunct",
			pred: predicate.And{
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
				predicate.Cmp{Path: []string{"cf", "age"}, Op: predicate.Gt, Value: float64(10)},
			},
			want: true,
		},
		{
			name: "numeric literal on undeclared column not pushable",
			pred: predicate.Eq{Path: []string{"cf", "author"}, Value: float64(1)},
			want: false,
		},
		{
			name: "single-segment path not pushable",
			pred: predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
			want: false,
		},
		{
			name: "range not pushable",
			pred: predicate.Cmp{Path: []string{"cf", "age"}, Op: predicate.Gt, Value: float64(10)},
			want: false,
		},
		{
			name: "exists not pushable",
			pred: predicate.Exists{Path: []string{"cf", "author"}},
			want: false,
		},
		{
			name: "or not pushable",
			pred: predicate.Or{
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Simmons"},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := toFilter(types, tt.pred)
			require.Equal(t, tt.want, ok)
			if tt.want {
				require.NotNil(t, f)
			}
		})
	}
}

func TestPushedColumns(t *testing.T) {
	types := typeMap{cellKey("cf", "age"): ctInt}
	tests := []struct {
		name string
		pred predicate.Node
		want []string
	}{
		{
			name: "single equality",
			pred: predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
			want: []string{cellKey("cf", "author")},
		},
		{
			name: "and of two",
			pred: predicate.And{
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"cf", "year"}, Value: "1965"},
			},
			want: []string{cellKey("cf", "author"), cellKey("cf", "year")},
		},
		{
			name: "and with one pushable conjunct pushes that one column",
			pred: predicate.And{
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
				predicate.Cmp{Path: []string{"cf", "age"}, Op: predicate.Gt, Value: float64(10)},
			},
			want: []string{cellKey("cf", "author")},
		},
		{
			name: "an equality that does not encode is not listed",
			pred: predicate.And{
				predicate.Eq{Path: []string{"cf", "author"}, Value: "Herbert"},
				predicate.Eq{Path: []string{"cf", "age"}, Value: "old"},
			},
			want: []string{cellKey("cf", "author")},
		},
		{
			name: "a path that is not family:qualifier is not listed",
			pred: predicate.Eq{Path: []string{"author"}, Value: "Herbert"},
		},
		{
			name: "nothing pushable",
			pred: predicate.Cmp{Path: []string{"cf", "age"}, Op: predicate.Lt, Value: float64(5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, pushedColumns(types, tt.pred))
		})
	}
}
