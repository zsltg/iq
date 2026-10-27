package numfmt

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
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
	if f, ok := fractionalFloat(s, mode); ok {
		return f
	}
	return s
}

// DecodesToString reports whether ConvertNumber turns the JSON number literal into
// a string. It is true for a fractional literal in string mode and for a fractional
// literal that float64 cannot hold. An integer literal never becomes a string. A
// client-side prefilter that reads the raw JSON type calls this, so it models the
// decode with the same test.
func DecodesToString(literal string, mode DecimalMode) bool {
	if !strings.ContainsAny(literal, ".eE") {
		return false
	}
	_, ok := fractionalFloat(literal, mode)
	return !ok
}

// IsDecodedText reports whether s is the exact text that ConvertNumber can return
// for a JSON number token in the mode. A server-side filter calls this for a string
// literal. Only such a string can equal a number that the client decodes to a string.
func IsDecodedText(s string, mode DecimalMode) bool {
	if s == "" || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) {
		return false
	}
	return json.Valid([]byte(s)) && DecodesToString(s, mode)
}

// fractionalFloat decodes a fractional literal to a float64. It reports false when
// the literal stays a string: always in string mode, and when float64 cannot hold the
// literal. ParseFloat returns an error and +Inf or -Inf for a literal out of range.
func fractionalFloat(literal string, mode DecimalMode) (float64, bool) {
	if mode == DecimalString {
		return 0, false
	}
	f, err := strconv.ParseFloat(literal, 64)
	return f, err == nil
}
