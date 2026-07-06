package cassandra

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"strconv"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"gopkg.in/inf.v0"

	"github.com/zsltg/iq/internal/numfmt"
)

// Normalize converts a value gocql yields for a CQL column into a JSON-ready Go
// value the jq engine accepts: nil, bool, int, float64, string, []any,
// map[string]any. Richer CQL types are rendered to their natural JSON form — a
// uuid to its canonical string, a timestamp to RFC 3339, a blob to base64, inet to
// its string — so filters read naturally and the encoding is a frozen contract,
// the Cassandra analogue of the Redis and Mongo normalizations. A decimal follows
// dec: exact string, or a bare number. A varint keeps its exact value as an int
// when it fits, else its decimal string, so precision is never silently lost. It
// is exported so a future dump reader produces the exact same shape a live scan
// does. Collections arrive as concrete typed Go containers, so they are walked by
// reflection rather than an exhaustive per-element-type switch.
func Normalize(v any, dec numfmt.DecimalMode) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case string:
		return t
	case int:
		return t
	case int8:
		return int(t)
	case int16:
		return int(t)
	case int32:
		return int(t)
	case int64:
		return int(t)
	case float32:
		return float64(t)
	case float64:
		return t
	case *big.Int:
		if t.IsInt64() {
			return int(t.Int64())
		}
		return t.String()
	case *inf.Dec:
		return decimalValue(t, dec)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case gocql.UUID:
		return t.String()
	case []byte:
		return base64.StdEncoding.EncodeToString(t)
	case net.IP:
		return t.String()
	default:
		return normalizeReflect(v, dec)
	}
}

// normalizeReflect handles the CQL collection types (list, set, map) and tuples,
// which gocql returns as concrete typed Go slices and maps. A slice or array
// becomes a []any of normalized elements; a map becomes a map[string]any with each
// key stringified (a CQL map key may be non-text). A pointer is dereferenced. A
// value with no first-class JSON form is rendered as its Go string form rather than
// dropped, so nothing is lost silently.
func normalizeReflect(v any, dec numfmt.DecimalMode) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = Normalize(rv.Index(i).Interface(), dec)
		}
		return out
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		for _, k := range rv.MapKeys() {
			out[keyString(k.Interface())] = Normalize(rv.MapIndex(k).Interface(), dec)
		}
		return out
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return Normalize(rv.Elem().Interface(), dec)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// decimalValue renders a CQL decimal per the decimal mode. In auto and string mode
// it keeps the exact decimal literal as a string; in number mode it parses to a
// float64 (lossy beyond float64). A nil decimal is null.
func decimalValue(d *inf.Dec, mode numfmt.DecimalMode) any {
	if d == nil {
		return nil
	}
	str := d.String()
	if mode != numfmt.DecimalNumber {
		return str
	}
	if f, err := strconv.ParseFloat(str, 64); err == nil {
		return f
	}
	return str
}

// KeyOf renders a row's full primary key to the string key the table is keyed by.
// A single-column primary key becomes that column's bare canonical string (the
// Cassandra analogue of Mongo's _id-as-string); a composite primary key becomes a
// compact JSON array of the key columns in schema order (partition keys first, then
// clustering columns), so identity is reversible by decodeKey. It is the inverse of
// decodeKey and the frozen key contract.
func KeyOf(meta *gocql.TableMetadata, row map[string]any) string {
	cols := primaryKeyColumns(meta)
	if len(cols) == 1 {
		return keyString(row[cols[0].Name])
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = keyString(row[c.Name])
	}
	b, err := json.Marshal(parts)
	if err != nil {
		// A slice of strings always marshals; the guard keeps the contract total.
		return fmt.Sprintf("%v", parts)
	}
	return string(b)
}

// keyString renders a single primary-key value to its canonical string form, the
// scalar building block of KeyOf. It mirrors Normalize's scalar cases but always
// yields a string so a key round-trips through decodeKey.
func keyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int8:
		return strconv.FormatInt(int64(t), 10)
	case int16:
		return strconv.FormatInt(int64(t), 10)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case *big.Int:
		return t.String()
	case *inf.Dec:
		return t.String()
	case gocql.UUID:
		return t.String()
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case []byte:
		return base64.StdEncoding.EncodeToString(t)
	case net.IP:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// primaryKeyColumns returns a table's full primary key in schema order: the
// partition-key columns followed by the clustering columns. This order is the
// frozen order KeyOf encodes and decodeKey reverses.
func primaryKeyColumns(meta *gocql.TableMetadata) []*gocql.ColumnMetadata {
	cols := append([]*gocql.ColumnMetadata{}, meta.PartitionKey...)
	return append(cols, meta.ClusteringColumns...)
}

// decodeKey reverses KeyOf: it parses a string key back into the typed primary-key
// bind values, in schema order, using the table's column types. A single-column key
// is the whole string; a composite key is a JSON array whose element count must
// match the primary key. It is the read/write-side inverse used by Get and Put.
func decodeKey(meta *gocql.TableMetadata, key string) ([]any, error) {
	cols := primaryKeyColumns(meta)
	if len(cols) == 0 {
		return nil, fmt.Errorf("cassandra: table %q has no primary key", meta.Name)
	}
	if len(cols) == 1 {
		v, err := bindKeyValue(cols[0].Type, key)
		if err != nil {
			return nil, err
		}
		return []any{v}, nil
	}
	var parts []string
	if err := json.Unmarshal([]byte(key), &parts); err != nil {
		return nil, fmt.Errorf("cassandra: decode composite key %q: %w", key, err)
	}
	if len(parts) != len(cols) {
		return nil, fmt.Errorf("cassandra: composite key %q has %d part(s), want %d", key, len(parts), len(cols))
	}
	out := make([]any, len(cols))
	for i, c := range cols {
		v, err := bindKeyValue(c.Type, parts[i])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// bindKeyValue parses a primary-key string component into the Go value gocql binds
// for the column's CQL type. It is the typed reverse of keyString; an unparseable
// component is an error so a bad key fails fast rather than binding a wrong value.
func bindKeyValue(t gocql.TypeInfo, s string) (any, error) {
	switch t.Type() {
	case gocql.TypeText, gocql.TypeVarchar, gocql.TypeAscii:
		return s, nil
	case gocql.TypeInt, gocql.TypeSmallInt, gocql.TypeTinyInt:
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not an integer: %w", s, err)
		}
		return n, nil
	case gocql.TypeBigInt, gocql.TypeCounter:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not a bigint: %w", s, err)
		}
		return n, nil
	case gocql.TypeVarint:
		z, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("cassandra: key %q is not a varint", s)
		}
		return z, nil
	case gocql.TypeFloat:
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not a float: %w", s, err)
		}
		return float32(f), nil
	case gocql.TypeDouble:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not a double: %w", s, err)
		}
		return f, nil
	case gocql.TypeDecimal:
		d, ok := new(inf.Dec).SetString(s)
		if !ok {
			return nil, fmt.Errorf("cassandra: key %q is not a decimal", s)
		}
		return d, nil
	case gocql.TypeBoolean:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not a boolean: %w", s, err)
		}
		return b, nil
	case gocql.TypeUUID, gocql.TypeTimeUUID:
		u, err := gocql.ParseUUID(s)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not a uuid: %w", s, err)
		}
		return u, nil
	case gocql.TypeTimestamp:
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not a timestamp: %w", s, err)
		}
		return ts, nil
	case gocql.TypeBlob:
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("cassandra: key %q is not base64 blob: %w", s, err)
		}
		return b, nil
	case gocql.TypeInet:
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("cassandra: key %q is not an ip address", s)
		}
		return ip, nil
	default:
		// A type without a dedicated parse binds as the raw string; gocql rejects a
		// genuine mismatch at execution, so nothing wrong is bound silently.
		return s, nil
	}
}

// bindValue coerces a normalized JSON value (the shape the read path and a copy
// source produce) into the Go value gocql binds for the column's CQL type, the
// write-side inverse of Normalize. A scalar mismatch or an unsupported column type
// is an error, never a silent drop. Collections of scalar elements (and text-keyed
// maps) are supported; a nested tuple, UDT, or non-text map key is rejected so a
// copy fails loudly rather than corrupting the row.
func bindValue(t gocql.TypeInfo, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch t.Type() {
	case gocql.TypeText, gocql.TypeVarchar, gocql.TypeAscii:
		return asString(t, v)
	case gocql.TypeInt, gocql.TypeSmallInt, gocql.TypeTinyInt, gocql.TypeBigInt, gocql.TypeCounter:
		return toInt(v)
	case gocql.TypeVarint:
		return toBigInt(v)
	case gocql.TypeFloat:
		f, err := toFloat(v)
		return float32(f), err
	case gocql.TypeDouble:
		return toFloat(v)
	case gocql.TypeDecimal:
		return toDecimal(v)
	case gocql.TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, typeErr(t, v)
		}
		return b, nil
	case gocql.TypeUUID, gocql.TypeTimeUUID:
		s, err := asString(t, v)
		if err != nil {
			return nil, err
		}
		return gocql.ParseUUID(s)
	case gocql.TypeTimestamp:
		s, err := asString(t, v)
		if err != nil {
			return nil, err
		}
		return time.Parse(time.RFC3339Nano, s)
	case gocql.TypeBlob:
		s, err := asString(t, v)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(s)
	case gocql.TypeInet:
		s, err := asString(t, v)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("cassandra: %q is not an ip address", s)
		}
		return ip, nil
	case gocql.TypeList, gocql.TypeSet:
		return bindList(t, v)
	case gocql.TypeMap:
		return bindMap(t, v)
	default:
		return nil, fmt.Errorf("cassandra: unsupported column type %s for write", t)
	}
}

// bindList coerces a normalized array into a typed slice of the collection's
// element type, so gocql marshals a list or set from JSON-ready input.
func bindList(t gocql.TypeInfo, v any) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, typeErr(t, v)
	}
	coll, ok := t.(gocql.CollectionType)
	if !ok {
		return nil, fmt.Errorf("cassandra: %s is not a collection type", t)
	}
	out := make([]any, len(arr))
	for i, e := range arr {
		bv, err := bindValue(coll.Elem, e)
		if err != nil {
			return nil, err
		}
		out[i] = bv
	}
	return out, nil
}

// bindMap coerces a normalized object into a typed map of the collection's key and
// element types. Only a text-keyed map is supported, because the normalized form
// keys by string; a non-text key type is rejected rather than guessed.
func bindMap(t gocql.TypeInfo, v any) (any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, typeErr(t, v)
	}
	coll, ok := t.(gocql.CollectionType)
	if !ok {
		return nil, fmt.Errorf("cassandra: %s is not a collection type", t)
	}
	switch coll.Key.Type() {
	case gocql.TypeText, gocql.TypeVarchar, gocql.TypeAscii:
	default:
		return nil, fmt.Errorf("cassandra: map key type %s is not supported for write", coll.Key)
	}
	out := make(map[string]any, len(obj))
	for k, e := range obj {
		bv, err := bindValue(coll.Elem, e)
		if err != nil {
			return nil, err
		}
		out[k] = bv
	}
	return out, nil
}

// asString requires v to be a string, the JSON form of every string-encoded CQL
// type (text, uuid, timestamp, blob, inet).
func asString(t gocql.TypeInfo, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", typeErr(t, v)
	}
	return s, nil
}

// toInt coerces a JSON number (int, or a whole float64) into a Go int for an
// integer column. A non-whole float or non-number is an error.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("cassandra: %v is not a whole number", v)
		}
		return int(int64(n)), nil
	case *big.Int:
		if !n.IsInt64() {
			return 0, fmt.Errorf("cassandra: %v overflows int", v)
		}
		return int(n.Int64()), nil
	default:
		return 0, fmt.Errorf("cassandra: %v (%T) is not an integer", v, v)
	}
}

// toBigInt coerces a JSON number or its exact decimal string into a *big.Int for a
// varint column, so a value beyond int64 survives a round-trip.
func toBigInt(v any) (*big.Int, error) {
	switch n := v.(type) {
	case *big.Int:
		return n, nil
	case int:
		return big.NewInt(int64(n)), nil
	case int64:
		return big.NewInt(n), nil
	case string:
		z, ok := new(big.Int).SetString(n, 10)
		if !ok {
			return nil, fmt.Errorf("cassandra: %q is not a varint", n)
		}
		return z, nil
	case float64:
		if n != float64(int64(n)) {
			return nil, fmt.Errorf("cassandra: %v is not a whole number", v)
		}
		return big.NewInt(int64(n)), nil
	default:
		return nil, fmt.Errorf("cassandra: %v (%T) is not a varint", v, v)
	}
}

// toFloat coerces a JSON number into a float64 for a float or double column.
func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("cassandra: %v (%T) is not a number", v, v)
	}
}

// toDecimal coerces a JSON number or its exact string into a *inf.Dec for a decimal
// column, so both the string (exact) and number decimal presentations round-trip.
func toDecimal(v any) (*inf.Dec, error) {
	switch n := v.(type) {
	case string:
		d, ok := new(inf.Dec).SetString(n)
		if !ok {
			return nil, fmt.Errorf("cassandra: %q is not a decimal", n)
		}
		return d, nil
	case float64:
		d, ok := new(inf.Dec).SetString(strconv.FormatFloat(n, 'f', -1, 64))
		if !ok {
			return nil, fmt.Errorf("cassandra: %v is not a decimal", v)
		}
		return d, nil
	case int:
		return inf.NewDec(int64(n), 0), nil
	default:
		return nil, fmt.Errorf("cassandra: %v (%T) is not a decimal", v, v)
	}
}

// typeErr reports a value that cannot be bound to a column's CQL type.
func typeErr(t gocql.TypeInfo, v any) error {
	return fmt.Errorf("cassandra: value %v (%T) is not a %s", v, v, t)
}
