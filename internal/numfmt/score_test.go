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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, numfmt.FiniteScore(tt.score))
		})
	}
}
