package dynamodb

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/zsltg/iq/internal/numfmt"
)

// Normalize converts a DynamoDB attribute value into a JSON-ready Go value the jq
// engine accepts: nil, bool, int, float64, string, []any, map[string]any. A
// DynamoDB scalar gets its natural JSON form: a binary value becomes its base64
// string, and a null becomes null. The number type N (integers and decimals as
// strings) follows dec. An integer literal is an int, or its exact decimal string
// if it does not fit int. A fractional literal keeps its exact string in auto and
// string mode, or becomes a float64 in number mode. The set types (SS, NS, BS) map
// to a []any, so a filter reads them as a list. Normalize is exported so a dump
// reader produces the same shape as a live scan. The encoding is a frozen contract.
func Normalize(av types.AttributeValue, dec numfmt.DecimalMode) any {
	switch t := av.(type) {
	case nil, *types.AttributeValueMemberNULL:
		return nil
	case *types.AttributeValueMemberS:
		return t.Value
	case *types.AttributeValueMemberBOOL:
		return t.Value
	case *types.AttributeValueMemberN:
		return numberValue(t.Value, dec)
	case *types.AttributeValueMemberB:
		return base64.StdEncoding.EncodeToString(t.Value)
	case *types.AttributeValueMemberM:
		return normalizeMap(t.Value, dec)
	case *types.AttributeValueMemberL:
		return normalizeEach(t.Value, func(v types.AttributeValue) any { return Normalize(v, dec) })
	default:
		return normalizeSet(av, dec)
	}
}

// normalizeMap normalizes every attribute value of an M value or an item, keeping
// the names.
func normalizeMap(m map[string]types.AttributeValue, dec numfmt.DecimalMode) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = Normalize(v, dec)
	}
	return out
}

// normalizeEach converts every element of in with f and keeps the order. It always
// returns a non-nil slice, so an empty set or list stays an empty JSON array.
func normalizeEach[T any](in []T, f func(T) any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// normalizeSet renders the set types SS, NS and BS as a []any. Any other attribute
// type is rendered as its Go string form rather than dropped, so nothing is lost
// silently.
func normalizeSet(av types.AttributeValue, dec numfmt.DecimalMode) any {
	switch t := av.(type) {
	case *types.AttributeValueMemberSS:
		return normalizeEach(t.Value, func(s string) any { return s })
	case *types.AttributeValueMemberNS:
		return normalizeEach(t.Value, func(s string) any { return numberValue(s, dec) })
	case *types.AttributeValueMemberBS:
		return normalizeEach(t.Value, func(b []byte) any { return base64.StdEncoding.EncodeToString(b) })
	default:
		return fmt.Sprintf("%v", av)
	}
}

// numberValue renders a DynamoDB N literal per the decimal mode. An integer literal
// becomes an int if it fits int, else its exact decimal string. The parse uses
// strconv.IntSize, so a 32-bit build keeps a value beyond int32 as a string. A
// fractional literal keeps its exact string in auto and string mode. In number
// mode it parses to a float64 (lossy beyond float64), like the Cassandra decimal.
func numberValue(s string, mode numfmt.DecimalMode) any {
	if isIntLiteral(s) {
		if i, err := strconv.ParseInt(s, 10, strconv.IntSize); err == nil {
			return int(i)
		}
		// A well-formed integer that overflows int keeps its exact decimal string.
		if _, ok := new(big.Int).SetString(s, 10); ok {
			return s
		}
	}
	if mode == numfmt.DecimalNumber {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return s
}

// isIntLiteral reports whether s is a plain base-10 integer literal (optional sign,
// then digits). The result routes a fractional or exponential N to the decimal
// path.
func isIntLiteral(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '+' || s[0] == '-' {
		i = 1
	}
	if i == len(s) {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// keyOf renders an item's full primary key to the string key the table is keyed by.
// It delegates to KeyOf, so the live scan and an offline dump reader share one
// contract.
func (s *Store) keyOf(item map[string]types.AttributeValue) string {
	return KeyOf(s.keys, item)
}

// KeyOf renders an item's full primary key to the string key the table is keyed by.
// A partition-key-only table becomes that attribute's bare canonical string (the
// DynamoDB analogue of Mongo's _id). A table with a sort key becomes a compact JSON
// array of the two key attributes in schema order, so identity is reversible by
// decodeKey. KeyOf is the inverse of decodeKey and the frozen key contract. It is
// exported so a dump reader keys an item exactly as a live scan does.
func KeyOf(keys []KeyAttr, item map[string]types.AttributeValue) string {
	if len(keys) == 1 {
		return keyString(item[keys[0].name])
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = keyString(item[k.name])
	}
	b, err := json.Marshal(parts)
	if err != nil {
		// A slice of strings always marshals. The guard keeps the contract total.
		return fmt.Sprintf("%v", parts)
	}
	return string(b)
}

// keyString renders a single primary-key attribute to its canonical string form,
// the scalar building block of keyOf. A key attribute is only ever S, N, or B.
// Anything else falls back to its Go string form, so the contract stays total.
func keyString(av types.AttributeValue) string {
	switch t := av.(type) {
	case *types.AttributeValueMemberS:
		return t.Value
	case *types.AttributeValueMemberN:
		return t.Value
	case *types.AttributeValueMemberB:
		return base64.StdEncoding.EncodeToString(t.Value)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", av)
	}
}

// decodeKey reverses keyOf: it parses a string key back into the typed DynamoDB key
// attributes. A partition-key-only key is the whole string. A table with a sort key
// takes a JSON array whose element count must match the two key attributes. Get and
// Put use it as the read/write-side inverse.
func (s *Store) decodeKey(key string) (map[string]types.AttributeValue, error) {
	if len(s.keys) == 0 {
		return nil, errNoTable
	}
	out := make(map[string]types.AttributeValue, len(s.keys))
	if len(s.keys) == 1 {
		av, err := keyAV(s.keys[0], key)
		if err != nil {
			return nil, err
		}
		out[s.keys[0].name] = av
		return out, nil
	}
	var parts []string
	if err := json.Unmarshal([]byte(key), &parts); err != nil {
		return nil, fmt.Errorf("dynamodb: decode composite key %q: %w", key, err)
	}
	if len(parts) != len(s.keys) {
		return nil, fmt.Errorf("dynamodb: composite key %q has %d part(s), want %d", key, len(parts), len(s.keys))
	}
	for i, k := range s.keys {
		av, err := keyAV(k, parts[i])
		if err != nil {
			return nil, err
		}
		out[k.name] = av
	}
	return out, nil
}

// keyAV parses a primary-key string component into the DynamoDB attribute value
// for the key attribute's scalar type. It is the typed reverse of keyString. A
// component that is not a valid number or base64 blob for its column type is an
// error. So a bad key fails fast rather than binding a wrong value.
func keyAV(k KeyAttr, s string) (types.AttributeValue, error) {
	switch k.typ {
	case types.ScalarAttributeTypeN:
		if !isNumberLiteral(s) {
			return nil, fmt.Errorf("dynamodb: key %q is not a number for attribute %q", s, k.name)
		}
		if err := checkNumber(s); err != nil {
			return nil, fmt.Errorf("dynamodb: key attribute %q: %w", k.name, err)
		}
		return &types.AttributeValueMemberN{Value: s}, nil
	case types.ScalarAttributeTypeB:
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("dynamodb: key %q is not base64 for binary attribute %q: %w", s, k.name, err)
		}
		return &types.AttributeValueMemberB{Value: b}, nil
	default:
		// String key attribute (the default), or a type without a dedicated parse.
		return &types.AttributeValueMemberS{Value: s}, nil
	}
}

// isNumberLiteral reports whether s is a well-formed DynamoDB number (an integer or
// a decimal, of any precision). It validates a numeric key component before
// binding. big.Float rejects a fraction like "1/2" that big.Rat accepts.
func isNumberLiteral(s string) bool {
	if s == "" {
		return false
	}
	_, ok := new(big.Float).SetString(s)
	return ok
}

// toItem builds the DynamoDB item to write for a record. The value object's
// attributes are marshaled, then the primary-key attributes decoded from the record
// key overlay them (the key is authoritative, already typed). A keyless record must
// carry its key attributes in the object. A missing key attribute is an error.
func (s *Store) toItem(r recordValue) (map[string]types.AttributeValue, error) {
	obj, ok := r.value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dynamodb: record value must be an item object, got %T", r.value)
	}
	item, err := toAttributeMap(obj)
	if err != nil {
		return nil, err
	}
	if r.key != "" {
		keyAVs, err := s.decodeKey(r.key)
		if err != nil {
			return nil, err
		}
		maps.Copy(item, keyAVs)
	}
	for _, k := range s.keys {
		if _, ok := item[k.name]; !ok {
			return nil, fmt.Errorf("dynamodb: record missing key attribute %q", k.name)
		}
	}
	return item, nil
}

// recordValue is the key and value toItem needs, decoupling it from the query.Record
// type so normalize.go stays free of the write port.
type recordValue struct {
	key   string
	value any
}

// toAttributeValue coerces a normalized JSON value into a DynamoDB attribute
// value. It is the write-side inverse of Normalize. A number becomes N, a string S,
// a bool BOOL, null NULL, an object M, an array L. A value with no DynamoDB
// representation is an error, never a silent drop. Caveats: the set types
// (SS/NS/BS) normalize to []any, so a round trip writes them back as L, not a set.
// Numbers presented as strings (auto and string decimal mode) write back as S.
func toAttributeValue(v any) (types.AttributeValue, error) {
	return toAttributeAt("", v)
}

// toAttributeAt is toAttributeValue for the value at path inside the item. The
// path names the attribute in a number error, like "a.b[2]". The top level has an
// empty path.
func toAttributeAt(path string, v any) (types.AttributeValue, error) {
	switch t := v.(type) {
	case map[string]any:
		m, err := toAttributeMapAt(path, t)
		if err != nil {
			return nil, err
		}
		return &types.AttributeValueMemberM{Value: m}, nil
	case []any:
		l, err := toAttributeListAt(path, t)
		if err != nil {
			return nil, err
		}
		return &types.AttributeValueMemberL{Value: l}, nil
	default:
		av, err := scalarAttributeValue(v)
		if err != nil {
			return nil, atPath(path, err)
		}
		return av, nil
	}
}

// atPath adds the attribute path to a number error. An error of another kind
// passes through unchanged.
func atPath(path string, err error) error {
	var ne *numberError
	switch {
	case !errors.As(err, &ne):
		return err
	case path == "":
		return fmt.Errorf("dynamodb: %w", err)
	}
	return fmt.Errorf("dynamodb: attribute %q: %w", path, err)
}

// toAttributeMap converts every entry of a JSON object to an attribute value.
func toAttributeMap(obj map[string]any) (map[string]types.AttributeValue, error) {
	return toAttributeMapAt("", obj)
}

// toAttributeMapAt converts the entries of the object at path.
func toAttributeMapAt(path string, obj map[string]any) (map[string]types.AttributeValue, error) {
	m := make(map[string]types.AttributeValue, len(obj))
	err := convertAll(maps.All(obj), func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}, func(k string, av types.AttributeValue) { m[k] = av })
	if err != nil {
		return nil, err
	}
	return m, nil
}

// toAttributeListAt converts the elements of the array at path.
func toAttributeListAt(path string, list []any) ([]types.AttributeValue, error) {
	l := make([]types.AttributeValue, len(list))
	err := convertAll(slices.All(list), func(i int) string {
		return path + "[" + strconv.Itoa(i) + "]"
	}, func(i int, av types.AttributeValue) { l[i] = av })
	if err != nil {
		return nil, err
	}
	return l, nil
}

// convertAll converts each value of seq to an attribute value and passes it to store
// with its key or index. at gives the path of an entry. It stops at the first
// conversion error and returns it.
func convertAll[K any](seq iter.Seq2[K, any], at func(K) string, store func(K, types.AttributeValue)) error {
	for k, e := range seq {
		av, err := toAttributeAt(at(k), e)
		if err != nil {
			return err
		}
		store(k, av)
	}
	return nil
}

// scalarAttributeValue converts a JSON scalar (nil, bool, string, or a Go or json
// number) to an attribute value. A number must fit a DynamoDB N, so checkNumber
// runs on its text. The text is never changed. Any other type is an error.
func scalarAttributeValue(v any) (types.AttributeValue, error) {
	switch t := v.(type) {
	case nil:
		return &types.AttributeValueMemberNULL{Value: true}, nil
	case bool:
		return &types.AttributeValueMemberBOOL{Value: t}, nil
	case string:
		return &types.AttributeValueMemberS{Value: t}, nil
	case int:
		return &types.AttributeValueMemberN{Value: strconv.Itoa(t)}, nil
	case int64:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(t, 10)}, nil
	case float64:
		return numberAttribute(strconv.FormatFloat(t, 'g', -1, 64))
	case json.Number:
		return numberAttribute(t.String())
	case *big.Int:
		return nil, &numberError{msg: fmt.Sprintf("cannot write value %v (%T)", v, v)}
	default:
		return nil, fmt.Errorf("dynamodb: cannot write value %v (%T)", v, v)
	}
}

// The DynamoDB N limits: at most 38 significant digits, and a magnitude from
// 1E-130 to 9.9999999999999999999999999999999999999E+125. Zero is valid.
const (
	maxNumberDigits = 38
	minNumberExp    = -130
	maxNumberExp    = 125
	// maxExpClamp stands for an exponent that does not fit an int.
	maxExpClamp = 1 << 30
)

// numberError is a number that a write refuses. atPath adds the attribute path to
// it.
type numberError struct{ msg string }

func (e *numberError) Error() string { return e.msg }

// numberAttribute makes an N attribute from text that checkNumber accepts. The
// text goes to DynamoDB unchanged.
func numberAttribute(text string) (types.AttributeValue, error) {
	if err := checkNumber(text); err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberN{Value: text}, nil
}

// checkNumber makes sure that text is a decimal number that DynamoDB stores
// exactly as an N: a finite decimal with an optional sign, fraction, and exponent,
// of at most 38 significant digits and a magnitude within the N limits. Leading
// and trailing zeros do not count as significant. It never changes text.
func checkNumber(text string) error {
	intPart, fracPart, exp, ok := splitDecimal(text)
	if !ok {
		return &numberError{msg: fmt.Sprintf("%q is not a finite decimal number", text)}
	}
	all := intPart + fracPart
	first := strings.IndexFunc(all, func(r rune) bool { return r != '0' })
	if first < 0 {
		return nil // zero
	}
	digits := strings.TrimRight(all[first:], "0")
	if len(digits) > maxNumberDigits {
		return &numberError{msg: fmt.Sprintf("number %s has %d significant digits, DynamoDB stores at most %d",
			text, len(digits), maxNumberDigits)}
	}
	// The value is d.ddd x 10^lead, with the first digit before the point.
	lead := len(intPart) - 1 - first + exp
	if lead < minNumberExp || lead > maxNumberExp {
		return &numberError{msg: fmt.Sprintf("number %s is outside the DynamoDB range 1E-130 to 9.9999999999999999999999999999999999999E+125", text)}
	}
	return nil
}

// splitDecimal splits text into the digits before the point, the digits after it,
// and the exponent. It reports false if text is not a decimal number with at least
// one digit. An exponent that does not fit an int becomes a huge value.
func splitDecimal(text string) (intPart, fracPart string, exp int, ok bool) {
	rest := text
	if strings.HasPrefix(rest, "+") || strings.HasPrefix(rest, "-") {
		rest = rest[1:]
	}
	if i := strings.IndexAny(rest, "eE"); i >= 0 {
		e, err := strconv.Atoi(rest[i+1:])
		switch {
		case errors.Is(err, strconv.ErrRange):
			// Far outside the N range. The clamp keeps the arithmetic of the caller safe.
			e = maxExpClamp
			if strings.HasPrefix(rest[i+1:], "-") {
				e = -maxExpClamp
			}
		case err != nil:
			return "", "", 0, false
		}
		exp, rest = e, rest[:i]
	}
	intPart, fracPart, _ = strings.Cut(rest, ".")
	if intPart+fracPart == "" || !allDigits(intPart) || !allDigits(fracPart) {
		return "", "", 0, false
	}
	return intPart, fracPart, exp, true
}

// allDigits reports whether s holds only the digits 0 to 9.
func allDigits(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}
