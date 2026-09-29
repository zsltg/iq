package numfmt_test

import (
	"encoding/json"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestConvertNumber(t *testing.T) {
	bigWant, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	tests := []struct {
		name string
		in   string
		mode numfmt.DecimalMode
		want any
	}{
		{name: "small int", in: "5", mode: numfmt.DecimalAuto, want: 5},
		{name: "negative int", in: "-7", mode: numfmt.DecimalAuto, want: -7},
		{name: "big int stays exact", in: "123456789012345678901234567890", mode: numfmt.DecimalAuto, want: bigWant},
		{name: "fractional auto is float", in: "19.99", mode: numfmt.DecimalAuto, want: 19.99},
		{name: "fractional number mode is float", in: "19.99", mode: numfmt.DecimalNumber, want: 19.99},
		{name: "fractional string mode is exact literal", in: "19.99", mode: numfmt.DecimalString, want: "19.99"},
		{name: "exponent auto is float", in: "1e3", mode: numfmt.DecimalAuto, want: 1000.0},
		{name: "exponent string mode keeps literal", in: "1e3", mode: numfmt.DecimalString, want: "1e3"},
		// A token that is not a valid integer literal cannot become an int or a
		// *big.Int. It is returned as its literal text, not lost.
		{name: "malformed integer literal keeps its text", in: "12x", mode: numfmt.DecimalAuto, want: "12x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, numfmt.ConvertNumber(json.Number(tt.in), tt.mode))
		})
	}
}

func TestConvertNumbers(t *testing.T) {
	t.Run("recurses into objects and arrays", func(t *testing.T) {
		in := map[string]any{
			"o": map[string]any{"a": json.Number("7")},
			"l": []any{json.Number("1"), json.Number("2.5")},
		}
		got := numfmt.ConvertNumbers(in, numfmt.DecimalAuto)
		require.Equal(t, map[string]any{
			"o": map[string]any{"a": 7},
			"l": []any{1, 2.5},
		}, got)
	})

	t.Run("leaves non-number values untouched", func(t *testing.T) {
		require.Equal(t, "x", numfmt.ConvertNumbers("x", numfmt.DecimalAuto))
		require.Equal(t, true, numfmt.ConvertNumbers(true, numfmt.DecimalAuto))
		require.Nil(t, numfmt.ConvertNumbers(nil, numfmt.DecimalAuto))
	})
}

// TestConvertNumberNegativeZero pins that the literal -0 becomes the float64
// negative zero in every mode, not the int 0 (require.Equal cannot tell them
// apart, so the sign bit is checked directly).
func TestConvertNumberNegativeZero(t *testing.T) {
	for _, mode := range []numfmt.DecimalMode{numfmt.DecimalAuto, numfmt.DecimalNumber, numfmt.DecimalString} {
		got := numfmt.ConvertNumber(json.Number("-0"), mode)
		f, ok := got.(float64)
		require.Truef(t, ok, "mode %d: -0 converted to %T, not float64", mode, got)
		require.Zero(t, f)
		require.True(t, math.Signbit(f), "mode %d: the sign of -0 is lost", mode)
	}
}
