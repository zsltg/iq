package numfmt

import "math"

// FiniteScore returns a score that JSON can encode. A finite score stays a
// float64. JSON has no form for an infinity or NaN, so +Inf becomes "inf", -Inf
// becomes "-inf", and NaN becomes "nan". These are the texts that Redis uses.
func FiniteScore(f float64) any {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 0):
		if math.Signbit(f) {
			return "-inf"
		}
		return "inf"
	default:
		return f
	}
}
