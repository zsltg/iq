package numfmt_test

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// maxFuzzInput bounds one fuzz input. A longer literal reaches no new branch, and
// a huge digit string only slows the big.Int parse.
const maxFuzzInput = 4 << 10

// fuzzModes lists every DecimalMode, so one input is converted under all three.
var fuzzModes = []numfmt.DecimalMode{numfmt.DecimalAuto, numfmt.DecimalNumber, numfmt.DecimalString}

// FuzzConvertNumber drives the number conversion with arbitrary literals, which is
// what a backend document hands it. The package documents that an integer is never
// lossy in any mode, so the oracle holds every integer literal to its exact value
// as an int or a *big.Int, whichever mode is asked for. A fractional literal keeps
// its exact text in string mode. Nothing panics, whatever the bytes say.
func FuzzConvertNumber(f *testing.F) {
	// Seeds cover the branches of the conversion plus hostile input: empty, invalid
	// UTF-8, an integer beyond int64, an exponent beyond float64 and a bare word.
	seeds := []string{
		"0", "-3", "42", "9007199254740993", "100000000000000000001",
		"-100000000000000000001", "9223372036854775807", "-9223372036854775808",
		"1.5", "-2.5", "1e2", "1E2", "1.0", "-0", "0.1",
		"1e400", "-1e400", "1e-400", "3.141592653589793238462643383279",
		"", " ", "abc", "\xff\xfe", "--1", "1.2.3", "+1", "01",
	}
	for i, s := range seeds {
		f.Add(s, byte(i))
	}

	f.Fuzz(func(t *testing.T, s string, mode byte) {
		if len(s) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		m := fuzzModes[int(mode)%len(fuzzModes)]
		got := numfmt.ConvertNumber(json.Number(s), m)

		var probe json.Number
		if json.Unmarshal([]byte(s), &probe) != nil {
			return // not a JSON number, so no exactness claim applies.
		}
		if probe.String() != s {
			// JSON permits space around a value, and a decoder strips it. The
			// conversion takes the literal as written, so a padded string is not a
			// number token and it degrades to text.
			return
		}
		if !strings.ContainsAny(s, ".eE") {
			want, ok := new(big.Int).SetString(s, 10)
			require.Truef(t, ok, "a JSON integer literal must parse as an integer: %q", s)
			switch v := got.(type) {
			case int:
				require.Zerof(t, big.NewInt(int64(v)).Cmp(want), "integer %q converted lossily to %d", s, v)
			case *big.Int:
				require.Zerof(t, v.Cmp(want), "integer %q converted lossily to %s", s, v)
			default:
				t.Fatalf("integer %q converted to %T, which is not exact", s, got)
			}
			return
		}
		if m == numfmt.DecimalString {
			require.Equalf(t, s, got, "string mode must keep the exact literal %q", s)
			return
		}
		// Auto and number mode give the float64 strconv gives. Both modes are
		// documented as lossy beyond float64's range, so an out-of-range literal
		// saturates to an infinity here and only string mode keeps it exact.
		v, ok := got.(float64)
		require.Truef(t, ok, "decimal %q converted to %T under mode %d", s, got, m)
		want, err := strconv.ParseFloat(s, 64)
		if err != nil {
			require.ErrorIsf(t, err, strconv.ErrRange, "%q is a JSON number strconv rejects outright", s)
		}
		// Compared bit for bit, so an infinity from an out-of-range literal and a
		// signed zero must both match exactly.
		require.Equalf(t, math.Float64bits(want), math.Float64bits(v),
			"float conversion of %q disagrees with strconv", s)
	})
}
