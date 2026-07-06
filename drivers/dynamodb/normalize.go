package dynamodb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/zsltg/iq/internal/numfmt"
)

// Normalize converts a DynamoDB attribute value into a JSON-ready Go value the jq
// engine accepts: nil, bool, int, float64, string, []any, map[string]any. DynamoDB's
// scalar types map to their natural JSON form — a string stays a string, a boolean a
// boolean, a binary value to its base64 string, a null to null. The single number
// type N (which carries both integers and decimals as strings) follows dec: an integer
// literal is an int (or its exact decimal string when it overflows int64, so precision
// is never lost), a fractional literal keeps its exact string in auto and string mode
// or a bare float64 in number mode. The three set types (SS, NS, BS) map to a []any,
// so a filter reads them as a list. It is exported so a future dump reader produces the
// exact same shape a live scan does — the frozen encoding contract.
func Normalize(av types.AttributeValue, dec numfmt.DecimalMode) any {
	switch t := av.(type) {
	case nil:
		return nil
	case *types.AttributeValueMemberNULL:
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
		out := make(map[string]any, len(t.Value))
		for k, v := range t.Value {
			out[k] = Normalize(v, dec)
		}
		return out
	case *types.AttributeValueMemberL:
		out := make([]any, len(t.Value))
		for i, v := range t.Value {
			out[i] = Normalize(v, dec)
		}
		return out
	case *types.AttributeValueMemberSS:
		out := make([]any, len(t.Value))
		for i, s := range t.Value {
			out[i] = s
		}
		return out
	case *types.AttributeValueMemberNS:
		out := make([]any, len(t.Value))
		for i, s := range t.Value {
			out[i] = numberValue(s, dec)
		}
		return out
	case *types.AttributeValueMemberBS:
		out := make([]any, len(t.Value))
		for i, b := range t.Value {
			out[i] = base64.StdEncoding.EncodeToString(b)
		}
		return out
	default:
		// An attribute type without a first-class JSON form is rendered as its Go
		// string form rather than dropped, so nothing is lost silently.
		return fmt.Sprintf("%v", av)
	}
}

// numberValue renders a DynamoDB N literal per the decimal mode. An integer literal
// becomes an int when it fits int64, else its exact decimal string (so a value beyond
// int64 survives without precision loss). A fractional literal keeps its exact string
// in auto and string mode; in number mode it parses to a float64 (lossy beyond
// float64), matching the Cassandra decimal presentation.
func numberValue(s string, mode numfmt.DecimalMode) any {
	if isIntLiteral(s) {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return int(i)
		}
		// A well-formed integer that overflows int64 keeps its exact decimal string.
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
// then digits), so a fractional or exponential N is routed to the decimal path.
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

// keyOf renders an item's full primary key to the string key the table is keyed by,
// delegating to KeyOf so the live scan and an offline dump reader share one contract.
func (s *Store) keyOf(item map[string]types.AttributeValue) string {
	return KeyOf(s.keys, item)
}

// KeyOf renders an item's full primary key to the string key the table is keyed by. A
// partition-key-only table becomes that attribute's bare canonical string (the
// DynamoDB analogue of Mongo's _id); a table with a sort key becomes a compact JSON
// array of the two key attributes in schema order, so identity is reversible by
// decodeKey. It is the inverse of decodeKey and the frozen key contract, exported so a
// dump reader keys an item exactly as a live scan does.
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
		// A slice of strings always marshals; the guard keeps the contract total.
		return fmt.Sprintf("%v", parts)
	}
	return string(b)
}

// keyString renders a single primary-key attribute to its canonical string form, the
// scalar building block of keyOf. A key attribute is only ever S, N, or B; anything
// else falls back to its Go string form so the contract stays total.
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
// attributes. A partition-key-only key is the whole string; a table with a sort key
// takes a JSON array whose element count must match the two key attributes. It is the
// read/write-side inverse used by Get and Put.
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

// keyAV parses a primary-key string component into the DynamoDB attribute value for
// the key attribute's scalar type. It is the typed reverse of keyString; a component
// that is not a valid number or base64 blob for its column type is an error, so a bad
// key fails fast rather than binding a wrong value.
func keyAV(k KeyAttr, s string) (types.AttributeValue, error) {
	switch k.typ {
	case types.ScalarAttributeTypeN:
		if !isNumberLiteral(s) {
			return nil, fmt.Errorf("dynamodb: key %q is not a number for attribute %q", s, k.name)
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

// isNumberLiteral reports whether s is a well-formed DynamoDB number (an integer or a
// decimal, of arbitrary precision), used to validate a numeric key component before
// binding it. big.Float parses integers, decimals, and exponents at arbitrary
// precision while rejecting a fraction like "1/2" that big.Rat would accept.
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
	item := make(map[string]types.AttributeValue, len(obj))
	for name, v := range obj {
		av, err := toAttributeValue(v)
		if err != nil {
			return nil, err
		}
		item[name] = av
	}
	if r.key != "" {
		keyAVs, err := s.decodeKey(r.key)
		if err != nil {
			return nil, err
		}
		for name, av := range keyAVs {
			item[name] = av
		}
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

// toAttributeValue coerces a normalized JSON value (the shape the read path and a copy
// source produce) into a DynamoDB attribute value, the write-side inverse of
// Normalize. A number becomes N, a string S, a bool BOOL, null NULL, an object M, an
// array L. A value with no DynamoDB representation is an error, never a silent drop.
// Note the set types (SS/NS/BS) normalize to []any, so a round-trip writes them back
// as L (a list), not a set — the documented set-degradation caveat, and numbers
// presented as strings (auto/string decimal mode) write back as S, not N.
func toAttributeValue(v any) (types.AttributeValue, error) {
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
		return &types.AttributeValueMemberN{Value: strconv.FormatFloat(t, 'g', -1, 64)}, nil
	case json.Number:
		return &types.AttributeValueMemberN{Value: t.String()}, nil
	case map[string]any:
		m := make(map[string]types.AttributeValue, len(t))
		for k, e := range t {
			av, err := toAttributeValue(e)
			if err != nil {
				return nil, err
			}
			m[k] = av
		}
		return &types.AttributeValueMemberM{Value: m}, nil
	case []any:
		l := make([]types.AttributeValue, len(t))
		for i, e := range t {
			av, err := toAttributeValue(e)
			if err != nil {
				return nil, err
			}
			l[i] = av
		}
		return &types.AttributeValueMemberL{Value: l}, nil
	default:
		return nil, fmt.Errorf("dynamodb: cannot write value %v (%T)", v, v)
	}
}
