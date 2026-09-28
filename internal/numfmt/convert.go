package numfmt

import (
	"encoding/json"
	"math"
	"math/big"
	"strings"
)

// ConvertNumbers walks a value decoded from JSON with json.Number tokens (a
// decoder configured with UseNumber), converting every json.Number to its
// precision-aware Go form and leaving other values untouched, recursing into
// objects and arrays. The adapters that decode JSON documents (Redis, CouchDB,
// Elasticsearch) share it so a number is normalized identically everywhere.
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
// is always exact: an int when it fits, else a *big.Int — gojq does exact
// arithmetic on both, so integers are never lossy regardless of the mode. The
// literal -0 is the float64 negative zero, the one integer an int cannot hold. A
// fractional number is a float64 in auto and number mode, or its exact literal
// string in string mode.
func ConvertNumber(n json.Number, mode DecimalMode) any {
	s := n.String()
	// -0 is the one integer literal that an int cannot hold: it would become 0,
	// and a dump of that value would then read back without its sign. Keep it as
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
	f, _ := n.Float64()
	return f
}
