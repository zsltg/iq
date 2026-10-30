package cassandra

import (
	"context"
	"math"
	"testing"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

func TestNormalizeConvertsNonFinite(t *testing.T) {
	tests := []struct {
		name       string
		in         any
		want       any
		wantFields []string
	}{
		{"nan double", math.NaN(), "NaN", []string{"c"}},
		{"positive infinity double", math.Inf(1), "Infinity", []string{"c"}},
		{"negative infinity float", float32(math.Inf(-1)), "-Infinity", []string{"c"}},
		{"finite double", 1.5, 1.5, nil},
		{"list", []float64{1, math.NaN()}, []any{1.0, "NaN"}, []string{"c[1]"}},
		{
			"map",
			map[string]float64{"up": math.Inf(1), "down": math.Inf(-1)},
			map[string]any{"up": "Infinity", "down": "-Infinity"},
			[]string{"c.up", "c.down"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &numfmt.Hits{}
			h.Enter("c")

			got := Normalize(tt.in, numfmt.DecimalAuto, h)

			require.Equal(t, tt.want, got)
			require.ElementsMatch(t, tt.wantFields, h.Fields)
		})
	}
}

func TestKeyedRowReportsNonFinite(t *testing.T) {
	meta := tableMetaFor(
		[]*gocql.ColumnMetadata{column("id", gocql.TypeText)}, nil,
		[]*gocql.ColumnMetadata{column("price", gocql.TypeDouble)},
	)
	s := &Store{meta: meta, decimal: numfmt.DecimalAuto}
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})

	key, val := s.keyedRow(ctx, map[string]any{"id": "r1", "price": math.Inf(1)})

	require.Equal(t, KeyOf(meta, map[string]any{"id": "r1"}), key)
	require.Equal(t, map[string]any{"id": "r1", "price": "Infinity"}, val)
	require.Equal(t, []call{{key, "price"}}, got)
}

func TestAppendRowReportsRowNumbers(t *testing.T) {
	s := &Store{decimal: numfmt.DecimalAuto}
	type call struct{ key, field string }
	var got []call
	ctx := query.WithNonFiniteNotice(context.Background(), func(key, field string) {
		got = append(got, call{key, field})
	})

	rows := s.appendRow(ctx, nil, map[string]any{"a": math.Inf(1), "b": math.NaN()})
	rows = s.appendRow(ctx, rows, map[string]any{"a": 1.5, "b": math.Inf(-1)})

	require.Equal(t, []any{
		map[string]any{"a": "Infinity", "b": "NaN"},
		map[string]any{"a": 1.5, "b": "-Infinity"},
	}, rows)
	require.ElementsMatch(t, []call{{"row 1", "a"}, {"row 1", "b"}, {"row 2", "b"}}, got)
}
