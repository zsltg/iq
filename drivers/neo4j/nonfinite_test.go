package neo4j

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

type noticeCall struct{ key, field string }

// noticeContext returns the marked context with a sink that appends to calls.
func noticeContext(calls *[]noticeCall) context.Context {
	return query.WithNonFiniteNotice(markedContext(), func(key, field string) {
		*calls = append(*calls, noticeCall{key, field})
	})
}

func TestNormalizeValueConvertsNonFinite(t *testing.T) {
	tests := []struct {
		name       string
		mode       numfmt.DecimalMode
		in         any
		want       any
		wantFields []string
	}{
		{"nan", numfmt.DecimalAuto, math.NaN(), "NaN", []string{"p"}},
		{"positive infinity", numfmt.DecimalAuto, math.Inf(1), "Infinity", []string{"p"}},
		{"negative infinity", numfmt.DecimalAuto, math.Inf(-1), "-Infinity", []string{"p"}},
		{"infinity in string mode", numfmt.DecimalString, math.Inf(1), "Infinity", []string{"p"}},
		{"finite in string mode", numfmt.DecimalString, 1.5, "1.5", nil},
		{"finite", numfmt.DecimalAuto, 1.5, 1.5, nil},
		{"list", numfmt.DecimalAuto, []any{1.5, math.NaN()}, []any{1.5, "NaN"}, []string{"p[1]"}},
		{
			"map",
			numfmt.DecimalAuto,
			map[string]any{"in": map[string]any{"up": math.Inf(1)}},
			map[string]any{"in": map[string]any{"up": "Infinity"}},
			[]string{"p.in.up"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{decimal: tt.mode}
			h := &numfmt.Hits{}
			h.Enter("p")

			got := s.normalizeValue(tt.in, h)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantFields, h.Fields)
		})
	}
}

func TestGetReportsNonFinite(t *testing.T) {
	s, _, _ := newFakeStore(target{name: "P"}, rows(
		getRow("a", map[string]any{"score": math.Inf(1), "ok": 1.5}),
	))
	var calls []noticeCall

	got, err := s.Get(noticeContext(&calls), []string{"a"})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"score": "Infinity", "ok": 1.5}}, got)
	require.Equal(t, []noticeCall{{"a", "score"}}, calls)
}

func TestScanReportsNonFinite(t *testing.T) {
	s, _, _ := newFakeStore(target{name: "P"}, rows(
		scanRow("a", "e1", map[string]any{"v": 1.5}),
		scanRow("b", "e2", map[string]any{"v": math.NaN()}),
	))
	var calls []noticeCall
	var seen map[string]any

	err := s.ScanBatches(noticeContext(&calls), func(b map[string]any) error {
		seen = b
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": map[string]any{"v": 1.5}, "b": map[string]any{"v": "NaN"}}, seen)
	require.Equal(t, []noticeCall{{"b", "v"}}, calls)
}
