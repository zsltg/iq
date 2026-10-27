package numfmt_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestDecodesToStringAgreesWithConvertNumber pins the single source of truth. For
// every literal and mode, DecodesToString is true exactly when ConvertNumber returns
// a string.
func TestDecodesToStringAgreesWithConvertNumber(t *testing.T) {
	literals := []string{
		"0", "-0", "7", "-7", "123456789012345678901234567890",
		"1.5", "1.50", "-0.5", "1e3", "1E3", "2e308", "-1e999", "1e-999", "0.0",
	}
	modes := map[string]numfmt.DecimalMode{
		"auto":   numfmt.DecimalAuto,
		"number": numfmt.DecimalNumber,
		"string": numfmt.DecimalString,
	}
	for name, mode := range modes {
		for _, lit := range literals {
			t.Run(name+"/"+lit, func(t *testing.T) {
				_, isString := numfmt.ConvertNumber(json.Number(lit), mode).(string)
				require.Equal(t, isString, numfmt.DecodesToString(lit, mode))
			})
		}
	}
}

func TestIsDecodedText(t *testing.T) {
	tests := []struct {
		name string
		s    string
		mode numfmt.DecimalMode
		want bool
	}{
		{"fraction, string mode", "1.5", numfmt.DecimalString, true},
		{"fraction, auto mode", "1.5", numfmt.DecimalAuto, false},
		{"out of range, auto mode", "2e308", numfmt.DecimalAuto, true},
		{"negative out of range, number mode", "-1e999", numfmt.DecimalNumber, true},
		{"integer, string mode", "7", numfmt.DecimalString, false},
		{"word with an e, string mode", "price", numfmt.DecimalString, false},
		{"empty, string mode", "", numfmt.DecimalString, false},
		{"quoted number, string mode", `"1.5"`, numfmt.DecimalString, false},
		{"leading plus, string mode", "+1.5", numfmt.DecimalString, false},
		{"not a number, string mode", "1.5.5", numfmt.DecimalString, false},
		{"leading dot, string mode", ".5", numfmt.DecimalString, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, numfmt.IsDecodedText(tt.s, tt.mode))
		})
	}
}
