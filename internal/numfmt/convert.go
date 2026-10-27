package numfmt

import (
	"encoding/json"
	"math"
	"math/big"
	"strings"
)

// ConvertNumbers walks a value decoded from JSON with json.Number tokens (a
// decoder configured with UseNumber). It converts every json.Number to its
// precision-aware Go form, leaves other values untouched, and recurses into
// objects and arrays. The adapters that decode JSON documents (Redis, CouchDB,
// Elasticsearch) share it, so a number is normalized identically everywhere.
func ConvertNumbers(v any, mode DecimalMode) any {
	switch t := v.(type) {
	case json.Number:
		return ConvertNumber(t, mode)
	case map[string]any:
		for k, e := range t {
			t[k] = ConvertNumbers(e, mode)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = ConvertNumbers(e, mode)
		}
		return t
	default:
		return t
	}
}

// ConvertNumber resolves one JSON number token. An integer (no '.', 'e', or 'E')
// is always exact: an int when it fits, else a *big.Int. gojq does exact
// arithmetic on both. So integers are never lossy regardless of the mode. The
// literal -0 is the float64 negative zero, the one integer an int cannot hold. A
// fractional number is a float64 in auto and number mode, or its exact literal
// string in string mode. A fractional literal that float64 cannot hold, such as
// 1e999, stays its exact literal text. A float64 of +Inf or -Inf has no JSON
// form, so every encoder fails on it. Go reports no error for an underflow
// literal such as 1e-999, so that literal becomes the float64 zero.
func ConvertNumber(n json.Number, mode DecimalMode) any {
	s := n.String()
	// -0 is the one integer literal that an int cannot hold. An int turns -0
	// into 0, and a dump of that int reads back without the sign. Keep it as
	// the float64 negative zero, as jq does.
	if s == "-0" {
		return math.Copysign(0, -1)
	}
	if !strings.ContainsAny(s, ".eE") {
		if i, err := n.Int64(); err == nil && int64(int(i)) == i {
			return int(i)
		}
		if bi, ok := new(big.Int).SetString(s, 10); ok {
			return bi
		}
		return s
	}
	if mode == DecimalString {
		return s
	}
	// Float64 returns an error and +Inf or -Inf for a literal out of range.
	// Keep the literal text then, as the dump reader does.
	f, err := n.Float64()
	if err != nil {
		return s
	}
	return f
}
