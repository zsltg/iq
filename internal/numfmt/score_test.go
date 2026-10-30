package numfmt_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestFiniteScore(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		want  any
	}{
		{"normal", 2.5, 2.5},
		{"zero", 0, 0.0},
		{"positive infinity", math.Inf(1), "inf"},
		{"negative infinity", math.Inf(-1), "-inf"},
		{"nan", math.NaN(), "nan"},
		{"largest float", math.MaxFloat64, math.MaxFloat64},
		{"most negative float", -math.MaxFloat64, -math.MaxFloat64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, numfmt.FiniteScore(tt.score))
		})
	}
}

func TestFiniteFloat(t *testing.T) {
	tests := []struct {
		name          string
		in            float64
		want          any
		wantConverted bool
	}{
		{"nan", math.NaN(), "NaN", true},
		{"positive infinity", math.Inf(1), "Infinity", true},
		{"negative infinity", math.Inf(-1), "-Infinity", true},
		{"zero", 0, 0.0, false},
		{"negative zero", math.Copysign(0, -1), math.Copysign(0, -1), false},
		{"normal", 2.5, 2.5, false},
		{"largest float", math.MaxFloat64, math.MaxFloat64, false},
		{"smallest float", math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, converted := numfmt.FiniteFloat(tt.in)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantConverted, converted)
		})
	}
}

func TestHitsRecordsPaths(t *testing.T) {
	h := &numfmt.Hits{}
	h.Enter("a")
	h.Enter("b")
	require.Equal(t, "Infinity", h.Float(math.Inf(1)))
	h.Leave()
	h.Enter("list")
	h.EnterIndex(2)
	require.Equal(t, "NaN", h.Float(math.NaN()))
	h.Leave()
	h.Leave()
	h.Leave()
	require.InDelta(t, 1.5, h.Float(1.5), 1e-9)
	h.EnterIndex(0)
	h.Enter("score")
	require.Equal(t, "-Infinity", h.Float(math.Inf(-1)))

	require.Equal(t, []string{"a.b", "a.list[2]", "[0].score"}, h.Fields)
}

func TestNilHitsConverts(t *testing.T) {
	var h *numfmt.Hits
	h.Enter("a")
	h.EnterIndex(1)
	require.Equal(t, "Infinity", h.Float(math.Inf(1)))
	h.Leave()
}

func TestHitsScoreKeepsRedisTexts(t *testing.T) {
	h := &numfmt.Hits{}
	h.EnterIndex(0)
	h.Enter("score")
	require.Equal(t, "inf", h.Score(math.Inf(1)))
	require.InDelta(t, 2.5, h.Score(2.5), 1e-9)
	require.Equal(t, "nan", h.Score(math.NaN()))

	require.Equal(t, []string{"[0].score", "[0].score"}, h.Fields)
}

func TestHitsLeaveOnEmptyPathKeepsRecording(t *testing.T) {
	h := &numfmt.Hits{}

	require.NotPanics(t, h.Leave)
	h.Enter("a")
	require.Equal(t, "Infinity", h.Float(math.Inf(1)))

	require.Equal(t, []string{"a"}, h.Fields)
}

func TestNilHitsScoreConverts(t *testing.T) {
	var h *numfmt.Hits

	require.NotPanics(t, func() {
		require.Equal(t, "inf", h.Score(math.Inf(1)))
		require.Equal(t, "nan", h.Score(math.NaN()))
		require.InDelta(t, 2.5, h.Score(2.5), 1e-9)
	})
}
