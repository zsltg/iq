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
	if n, ok := signedInt(v); ok {
		return int(n)
	}
	if text, ok := textForm(v); ok {
		return text
	}
	switch t := v.(type) {
	case nil, bool, float64:
		return v
	case float32:
		return float64(t)
	case *big.Int:
		return bigValue(t)
	case *inf.Dec:
		return decimalValue(t, dec)
	default:
		return normalizeReflect(v, dec)
	}
}

// signedInt returns v as an int64 when v is an int, int8, int16, int32 or int64.
func signedInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int8:
		return int64(t), true
	case int16:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	default:
		return 0, false
	}
}

// textForm returns the canonical text of a string, uuid, time, blob or inet value.
// A time is UTC RFC 3339 with nanoseconds. A blob is standard base64. Normalize and
// keyString share it, so a value and its key read the same.
func textForm(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case gocql.UUID:
		return t.String(), true
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano), true
	case []byte:
		return base64.StdEncoding.EncodeToString(t), true
	case net.IP:
		return t.String(), true
	default:
		return "", false
	}
}

// bigValue renders a varint as an int when it fits int64, else as its decimal
// string, so precision is never silently lost.
func bigValue(z *big.Int) any {
	if z.IsInt64() {
		return int(z.Int64())
	}
	return z.String()
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
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return KeyOfColumns(names, row)
}

// KeyOfColumns renders a row's primary key from the ordered names of its key columns
// (partition keys first, then clustering columns), the schema-free form of KeyOf. It is
// exported so an offline dump reader, which learns the key columns from a ?keys= hint
// rather than table metadata, produces the exact same key a live scan does. A
// single-column key is that column's bare canonical string; a composite key is a
// compact JSON array in schema order, reversible by decodeKey.
func KeyOfColumns(keyCols []string, row map[string]any) string {
	if len(keyCols) == 1 {
		return keyString(row[keyCols[0]])
	}
	parts := make([]string, len(keyCols))
	for i, name := range keyCols {
		parts[i] = keyString(row[name])
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
	if n, ok := signedInt(v); ok {
		return strconv.FormatInt(n, 10)
	}
	if text, ok := textForm(v); ok {
		return text
	}
	if text, ok := numberKey(v); ok {
		return text
	}
	switch t := v.(type) {
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// numberKey returns the key text of a float, a varint or a decimal value.
func numberKey(v any) (string, bool) {
	switch t := v.(type) {
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	case *big.Int:
		return t.String(), true
	case *inf.Dec:
		return t.String(), true
	default:
		return "", false
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
		v, err := bindKeyValue(cols[0].Type.Type(), key)
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
		v, err := bindKeyValue(c.Type.Type(), parts[i])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// keyParsers maps a CQL type id to the function that parses a primary-key string for
// it. A type without an entry binds as the raw string. The text types need no entry.
// The map is read only: nothing writes to it after init.
var keyParsers = map[gocql.Type]func(s string) (any, error){
	gocql.TypeInt:       parseKeyInt,
	gocql.TypeSmallInt:  parseKeyInt,
	gocql.TypeTinyInt:   parseKeyInt,
	gocql.TypeBigInt:    parseKeyBigint,
	gocql.TypeCounter:   parseKeyBigint,
	gocql.TypeVarint:    parseKeyVarint,
	gocql.TypeFloat:     parseKeyFloat,
	gocql.TypeDouble:    parseKeyDouble,
	gocql.TypeDecimal:   parseKeyDecimal,
	gocql.TypeBoolean:   parseKeyBool,
	gocql.TypeUUID:      parseKeyUUID,
	gocql.TypeTimeUUID:  parseKeyUUID,
	gocql.TypeTimestamp: parseKeyTimestamp,
	gocql.TypeBlob:      parseKeyBlob,
	gocql.TypeInet:      parseKeyInet,
}

// bindKeyValue parses a primary-key string component into the Go value gocql binds
// for the column's CQL type. It is the typed reverse of keyString; an unparseable
// component is an error so a bad key fails fast rather than binding a wrong value. It
// takes the CQL type id (not a full TypeInfo) so an offline dump reader, which has only
// a type name from a ?types= hint, can drive it — see BindString.
func bindKeyValue(t gocql.Type, s string) (any, error) {
	if parse, ok := keyParsers[t]; ok {
		return parse(s)
	}
	// A type without a dedicated parse binds as the raw string; gocql rejects a
	// genuine mismatch at execution, so nothing wrong is bound silently.
	return s, nil
}

func parseKeyInt(s string) (any, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not an integer: %w", s, err)
	}
	return n, nil
}

func parseKeyBigint(s string) (any, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not a bigint: %w", s, err)
	}
	return n, nil
}

func parseKeyVarint(s string) (any, error) {
	z, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("cassandra: key %q is not a varint", s)
	}
	return z, nil
}

func parseKeyFloat(s string) (any, error) {
	f, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not a float: %w", s, err)
	}
	return float32(f), nil
}

func parseKeyDouble(s string) (any, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not a double: %w", s, err)
	}
	return f, nil
}

func parseKeyDecimal(s string) (any, error) {
	d, ok := new(inf.Dec).SetString(s)
	if !ok {
		return nil, fmt.Errorf("cassandra: key %q is not a decimal", s)
	}
	return d, nil
}

func parseKeyBool(s string) (any, error) {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not a boolean: %w", s, err)
	}
	return b, nil
}

func parseKeyUUID(s string) (any, error) {
	u, err := gocql.ParseUUID(s)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not a uuid: %w", s, err)
	}
	return u, nil
}

func parseKeyTimestamp(s string) (any, error) {
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not a timestamp: %w", s, err)
	}
	return ts, nil
}

func parseKeyBlob(s string) (any, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("cassandra: key %q is not base64 blob: %w", s, err)
	}
	return b, nil
}

func parseKeyInet(s string) (any, error) {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("cassandra: key %q is not an ip address", s)
	}
	return ip, nil
}

// binder coerces a normalized JSON value into the Go value gocql binds for one CQL type.
type binder func(t gocql.TypeInfo, v any) (any, error)

// scalarBinders maps a scalar CQL type id to the binder for it. The map is read only:
// nothing writes to it after init. It holds no collection type, so it never refers to
// bindValue.
var scalarBinders = map[gocql.Type]binder{
	gocql.TypeText:      bindText,
	gocql.TypeVarchar:   bindText,
	gocql.TypeAscii:     bindText,
	gocql.TypeInt:       bindInt,
	gocql.TypeSmallInt:  bindInt,
	gocql.TypeTinyInt:   bindInt,
	gocql.TypeBigInt:    bindInt,
	gocql.TypeCounter:   bindInt,
	gocql.TypeVarint:    bindVarint,
	gocql.TypeFloat:     bindFloat32,
	gocql.TypeDouble:    bindDouble,
	gocql.TypeDecimal:   bindDecimal,
	gocql.TypeBoolean:   bindBool,
	gocql.TypeUUID:      parsedBinder(parseUUID),
	gocql.TypeTimeUUID:  parsedBinder(parseUUID),
	gocql.TypeTimestamp: parsedBinder(parseTimestamp),
	gocql.TypeBlob:      parsedBinder(parseBlob),
	gocql.TypeInet:      parsedBinder(parseInet),
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
	if bind, ok := scalarBinders[t.Type()]; ok {
		return bind(t, v)
	}
	switch t.Type() {
	case gocql.TypeList, gocql.TypeSet:
		return bindList(t, v)
	case gocql.TypeMap:
		return bindMap(t, v)
	default:
		return nil, fmt.Errorf("cassandra: unsupported column type %s for write", t)
	}
}

func bindText(t gocql.TypeInfo, v any) (any, error) { return asString(t, v) }

func bindInt(_ gocql.TypeInfo, v any) (any, error) { return toInt(v) }

func bindVarint(_ gocql.TypeInfo, v any) (any, error) { return toBigInt(v) }

func bindFloat32(_ gocql.TypeInfo, v any) (any, error) {
	f, err := toFloat(v)
	return float32(f), err
}

func bindDouble(_ gocql.TypeInfo, v any) (any, error) { return toFloat(v) }

func bindDecimal(_ gocql.TypeInfo, v any) (any, error) { return toDecimal(v) }

func bindBool(t gocql.TypeInfo, v any) (any, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, typeErr(t, v)
	}
	return b, nil
}

// parsedBinder returns a binder for a string-encoded type: it requires a string with
// asString, then calls parse.
func parsedBinder(parse func(s string) (any, error)) binder {
	return func(t gocql.TypeInfo, v any) (any, error) {
		s, err := asString(t, v)
		if err != nil {
			return nil, err
		}
		return parse(s)
	}
}

func parseUUID(s string) (any, error) { return gocql.ParseUUID(s) }

func parseTimestamp(s string) (any, error) { return time.Parse(time.RFC3339Nano, s) }

func parseBlob(s string) (any, error) { return base64.StdEncoding.DecodeString(s) }

func parseInet(s string) (any, error) {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("cassandra: %q is not an ip address", s)
	}
	return ip, nil
}

// bindList coerces a normalized array into a typed slice of the collection's
// element type, so gocql marshals a list or set from JSON-ready input.
func bindList(t gocql.TypeInfo, v any) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, typeErr(t, v)
	}
	coll, err := collectionOf(t)
	if err != nil {
		return nil, err
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
	coll, err := collectionOf(t)
	if err != nil {
		return nil, err
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

// collectionOf returns t as a collection type, or an error when it is not one.
func collectionOf(t gocql.TypeInfo) (gocql.CollectionType, error) {
	coll, ok := t.(gocql.CollectionType)
	if !ok {
		return gocql.CollectionType{}, fmt.Errorf("cassandra: %s is not a collection type", t)
	}
	return coll, nil
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
