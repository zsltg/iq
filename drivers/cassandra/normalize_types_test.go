package cassandra

import (
	"fmt"
	"math"
	"math/big"
	"net"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
	"gopkg.in/inf.v0"

	"github.com/zsltg/iq/internal/numfmt"
)

const testUUID = "11111111-1111-1111-1111-111111111111"

func TestNormalizeEveryGoType(t *testing.T) {
	tooBig := new(big.Int).Add(big.NewInt(math.MaxInt64), big.NewInt(1))
	tooSmall := new(big.Int).Sub(big.NewInt(math.MinInt64), big.NewInt(1))
	ts := time.Date(2021, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 3600))
	n := 5
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"int8", int8(-7), -7},
		{"int16", int16(300), 300},
		{"big int at max int64", big.NewInt(math.MaxInt64), math.MaxInt64},
		{"big int at min int64", big.NewInt(math.MinInt64), math.MinInt64},
		{"big int above int64", tooBig, "9223372036854775808"},
		{"big int below int64", tooSmall, "-9223372036854775809"},
		{"nil decimal", (*inf.Dec)(nil), nil},
		{"time in another zone is utc", ts, "2021-01-02T02:04:05.000000006Z"},
		{"empty blob", []byte{}, ""},
		{"ipv6", net.ParseIP("::1"), "::1"},
		{"array", [2]int{1, 2}, []any{1, 2}},
		{"pointer", &n, 5},
		{"nil pointer", (*int)(nil), nil},
		{"map with int keys", map[int]string{1: "a"}, map[string]any{"1": "a"}},
		{"map with uuid keys", map[gocql.UUID]int{mustUUID(t, testUUID): 1}, map[string]any{testUUID: 1}},
		{"named duration is rendered by reflection", time.Second, "1s"},
		{"uint8 has no first-class form", uint8(7), "7"},
		{"struct has no first-class form", struct{ A, B int }{1, 2}, "{1 2}"},
		{"nested list", [][]int{{1}, {2, 3}}, []any{[]any{1}, []any{2, 3}}},
		{"empty list is not nil", []int{}, []any{}},
		{"empty map is not nil", map[string]int{}, map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Normalize(tt.in, numfmt.DecimalAuto))
		})
	}
}

func TestKeyOfColumnsEveryGoType(t *testing.T) {
	ts := time.Date(2021, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"nil", nil, ""},
		{"string", "abc", "abc"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"int", 42, "42"},
		{"int8", int8(42), "42"},
		{"int16", int16(4200), "4200"},
		{"int32", int32(42), "42"},
		{"int64", int64(-42), "-42"},
		{"float32 keeps 32-bit precision", float32(0.1), "0.1"},
		{"float64 exponent", 1e21, "1e+21"},
		{"big int", mustBig("123456789012345678901234567890"), "123456789012345678901234567890"},
		{"decimal", mustDec("3.14"), "3.14"},
		{"uuid", mustUUID(t, testUUID), testUUID},
		{"time is utc", ts, "2021-01-02T02:04:05Z"},
		{"blob", []byte("hi"), "aGk="},
		{"inet", net.ParseIP("10.0.0.1"), "10.0.0.1"},
		{"uint8 uses the default form", uint8(7), "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, KeyOfColumns([]string{"k"}, map[string]any{"k": tt.val}))
		})
	}
	t.Run("a missing column is empty", func(t *testing.T) {
		require.Empty(t, KeyOfColumns([]string{"k"}, map[string]any{}))
	})
	t.Run("composite mixes types in the given order", func(t *testing.T) {
		row := map[string]any{"a": "x", "b": 7, "c": true}
		require.Equal(t, `["x","7","true"]`, KeyOfColumns([]string{"a", "b", "c"}, row))
	})
	t.Run("composite escapes html characters like json.Marshal", func(t *testing.T) {
		row := map[string]any{"a": "a<b", "b": "x"}
		require.Equal(t, `["a\u003cb","x"]`, KeyOfColumns([]string{"a", "b"}, row))
	})
}

// keyTypeCases lists every CQL type id that has its own key form, and the
// types that bind as the raw string.
var keyTypeCases = []struct {
	name    string
	typ     gocql.Type
	ok      string
	want    any
	bad     string
	wantErr string
}{
	{"text", gocql.TypeText, "abc", "abc", "", ""},
	{"varchar", gocql.TypeVarchar, "abc", "abc", "", ""},
	{"ascii", gocql.TypeAscii, "abc", "abc", "", ""},
	{"int", gocql.TypeInt, "-42", -42, "x", `cassandra: key "x" is not an integer: strconv.Atoi: parsing "x": invalid syntax`},
	{"smallint", gocql.TypeSmallInt, "99999", 99999, "x", `cassandra: key "x" is not an integer: strconv.Atoi: parsing "x": invalid syntax`},
	{"tinyint", gocql.TypeTinyInt, "7", 7, "x", `cassandra: key "x" is not an integer: strconv.Atoi: parsing "x": invalid syntax`},
	{"int out of range", gocql.TypeInt, "7", 7, "99999999999999999999", `cassandra: key "99999999999999999999" is not an integer: strconv.Atoi: parsing "99999999999999999999": value out of range`},
	{"bigint", gocql.TypeBigInt, "-9", int64(-9), "x", `cassandra: key "x" is not a bigint: strconv.ParseInt: parsing "x": invalid syntax`},
	{"counter", gocql.TypeCounter, "9", int64(9), "x", `cassandra: key "x" is not a bigint: strconv.ParseInt: parsing "x": invalid syntax`},
	{"varint", gocql.TypeVarint, "-12345678901234567890", mustBig("-12345678901234567890"), "x", `cassandra: key "x" is not a varint`},
	{"float", gocql.TypeFloat, "1.5", float32(1.5), "x", `cassandra: key "x" is not a float: strconv.ParseFloat: parsing "x": invalid syntax`},
	{"double", gocql.TypeDouble, "2.5", 2.5, "x", `cassandra: key "x" is not a double: strconv.ParseFloat: parsing "x": invalid syntax`},
	{"decimal", gocql.TypeDecimal, "3.14", mustDec("3.14"), "x", `cassandra: key "x" is not a decimal`},
	{"boolean true", gocql.TypeBoolean, "TRUE", true, "x", `cassandra: key "x" is not a boolean: strconv.ParseBool: parsing "x": invalid syntax`},
	{"boolean one", gocql.TypeBoolean, "1", true, "x", `cassandra: key "x" is not a boolean: strconv.ParseBool: parsing "x": invalid syntax`},
	{"uuid", gocql.TypeUUID, testUUID, wantUUID, "x", `cassandra: key "x" is not a uuid: invalid UUID "x"`},
	{"timeuuid", gocql.TypeTimeUUID, testUUID, wantUUID, "x", `cassandra: key "x" is not a uuid: invalid UUID "x"`},
	{"timestamp", gocql.TypeTimestamp, "2021-01-02T03:04:05Z", time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC), "x", `cassandra: key "x" is not a timestamp: parsing time "x" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "x" as "2006"`},
	{"blob", gocql.TypeBlob, "aGk=", []byte("hi"), "!", `cassandra: key "!" is not base64 blob: illegal base64 data at input byte 0`},
	{"inet", gocql.TypeInet, "10.0.0.1", net.ParseIP("10.0.0.1"), "x", `cassandra: key "x" is not an ip address`},
	{"inet v6", gocql.TypeInet, "::1", net.ParseIP("::1"), "x", `cassandra: key "x" is not an ip address`},
	{"date binds raw", gocql.TypeDate, "2021-01-02", "2021-01-02", "", ""},
	{"time binds raw", gocql.TypeTime, "10:00", "10:00", "", ""},
	{"duration binds raw", gocql.TypeDuration, "1h", "1h", "", ""},
	{"list binds raw", gocql.TypeList, "x", "x", "", ""},
}

// wantUUID is the parsed form of testUUID.
var wantUUID, _ = gocql.ParseUUID(testUUID)

func TestBindKeyValueEveryType(t *testing.T) {
	for _, tt := range keyTypeCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindKeyValue(tt.typ, tt.ok)
			require.NoError(t, err)
			if want, isTime := tt.want.(time.Time); isTime {
				require.True(t, want.Equal(got.(time.Time)))
			} else {
				require.Equal(t, tt.want, got)
			}
			if tt.wantErr == "" {
				return
			}
			got, err = bindKeyValue(tt.typ, tt.bad)
			require.EqualError(t, err, tt.wantErr)
			require.Nil(t, got)
		})
	}
	t.Run("a timestamp with an offset keeps the instant", func(t *testing.T) {
		got, err := bindKeyValue(gocql.TypeTimestamp, "2021-01-02T03:04:05+01:00")
		require.NoError(t, err)
		require.True(t, time.Date(2021, 1, 2, 2, 4, 5, 0, time.UTC).Equal(got.(time.Time)))
	})
	t.Run("an empty text key is the empty string", func(t *testing.T) {
		got, err := bindKeyValue(gocql.TypeText, "")
		require.NoError(t, err)
		require.Empty(t, got)
	})
}

// typeName renders a native type the way the bind errors do.
func typeName(typ gocql.Type) string {
	return fmt.Sprintf("%s", nativeType(typ))
}

// scalarBindCases lists every scalar CQL type that bindValue handles. ok is a value the
// column accepts. mismatch is a value of the wrong kind and mismatchErr is the exact
// error it gets. The error value that comes with a failure is not part of the contract.
var scalarBindCases = []struct {
	name        string
	typ         gocql.Type
	ok          any
	want        any
	mismatch    any
	mismatchErr string
}{
	{"text", gocql.TypeText, "x", "x", 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeText)},
	{"varchar", gocql.TypeVarchar, "x", "x", 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeVarchar)},
	{"ascii", gocql.TypeAscii, "x", "x", 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeAscii)},
	{"int", gocql.TypeInt, int64(5), 5, "5", "cassandra: 5 (string) is not an integer"},
	{"smallint", gocql.TypeSmallInt, float64(5), 5, "5", "cassandra: 5 (string) is not an integer"},
	{"tinyint", gocql.TypeTinyInt, mustBig("5"), 5, true, "cassandra: true (bool) is not an integer"},
	{"bigint", gocql.TypeBigInt, 5, 5, "5", "cassandra: 5 (string) is not an integer"},
	{"counter", gocql.TypeCounter, 5, 5, "5", "cassandra: 5 (string) is not an integer"},
	{"varint int", gocql.TypeVarint, 5, mustBig("5"), true, "cassandra: true (bool) is not a varint"},
	{"varint int64", gocql.TypeVarint, int64(5), mustBig("5"), "x", `cassandra: "x" is not a varint`},
	{"varint big", gocql.TypeVarint, mustBig("5"), mustBig("5"), 5.5, "cassandra: 5.5 is not a whole number"},
	{"float", gocql.TypeFloat, 0.1, float32(0.1), "x", "cassandra: x (string) is not a number"},
	{"float from int64", gocql.TypeFloat, int64(3), float32(3), "x", "cassandra: x (string) is not a number"},
	{"double", gocql.TypeDouble, 0.5, 0.5, "x", "cassandra: x (string) is not a number"},
	{"decimal string", gocql.TypeDecimal, "1.5", mustDec("1.5"), "x", `cassandra: "x" is not a decimal`},
	{"decimal float", gocql.TypeDecimal, 1.5, mustDec("1.5"), math.NaN(), "cassandra: NaN is not a decimal"},
	{"decimal infinity", gocql.TypeDecimal, 1.5, mustDec("1.5"), math.Inf(1), "cassandra: +Inf is not a decimal"},
	{"decimal int", gocql.TypeDecimal, 5, mustDec("5"), int64(5), "cassandra: 5 (int64) is not a decimal"},
	{"boolean", gocql.TypeBoolean, false, false, "x", "cassandra: value x (string) is not a " + typeName(gocql.TypeBoolean)},
	{"uuid", gocql.TypeUUID, testUUID, wantUUID, 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeUUID)},
	{"timeuuid", gocql.TypeTimeUUID, testUUID, wantUUID, 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeTimeUUID)},
	{"timestamp", gocql.TypeTimestamp, "2021-01-02T03:04:05Z", time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC), 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeTimestamp)},
	{"blob", gocql.TypeBlob, "aGk=", []byte("hi"), 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeBlob)},
	{"inet", gocql.TypeInet, "::1", net.ParseIP("::1"), 5, "cassandra: value 5 (int) is not a " + typeName(gocql.TypeInet)},
}

func TestBindValueEveryScalarType(t *testing.T) {
	for _, tt := range scalarBindCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindValue(nativeType(tt.typ), tt.ok)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			_, err = bindValue(nativeType(tt.typ), tt.mismatch)
			require.EqualError(t, err, tt.mismatchErr)
		})
	}
}

func TestBindValueParseErrors(t *testing.T) {
	// A string that does not parse keeps the parser's own error, with no prefix
	// for uuid, timestamp and blob, and a prefix for inet.
	tests := []struct {
		name    string
		typ     gocql.Type
		in      string
		wantErr string
	}{
		{"uuid", gocql.TypeUUID, "zz", `invalid UUID "zz"`},
		{"timeuuid", gocql.TypeTimeUUID, "zz", `invalid UUID "zz"`},
		{"timestamp", gocql.TypeTimestamp, "2021", `parsing time "2021" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "" as "-"`},
		{"blob", gocql.TypeBlob, "!!", "illegal base64 data at input byte 0"},
		{"inet", gocql.TypeInet, "zz", `cassandra: "zz" is not an ip address`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := bindValue(nativeType(tt.typ), tt.in)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestBindValueNumberEdges(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		wantErr string
	}{
		{"nan", math.NaN(), "cassandra: NaN is not a whole number"},
		{"float beyond int64", 1e30, "cassandra: 1e+30 is not a whole number"},
		{"big int beyond int64", mustBig("99999999999999999999"), "cassandra: 99999999999999999999 overflows int"},
		{"non-whole float", 5.5, "cassandra: 5.5 is not a whole number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := bindValue(nativeType(gocql.TypeInt), tt.in)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestBindValueUnsupportedTypes(t *testing.T) {
	tuple := gocql.NewNativeType(4, gocql.TypeCustom, "tuple<int,int>")
	types := []struct {
		name string
		typ  gocql.TypeInfo
	}{
		{"date", nativeType(gocql.TypeDate)},
		{"time", nativeType(gocql.TypeTime)},
		{"duration", nativeType(gocql.TypeDuration)},
		{"tuple", tuple},
	}
	for _, tt := range types {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindValue(tt.typ, "x")
			require.Nil(t, got)
			require.EqualError(t, err, fmt.Sprintf("cassandra: unsupported column type %s for write", tt.typ))
		})
	}
}

func TestBindValueCollections(t *testing.T) {
	list := gocql.NewNativeType(4, gocql.TypeCustom, "list<int>")
	set := gocql.NewNativeType(4, gocql.TypeCustom, "set<int>")
	nested := gocql.NewNativeType(4, gocql.TypeCustom, "list<list<int>>")
	textMap := gocql.NewNativeType(4, gocql.TypeCustom, "map<text,int>")
	intMap := gocql.NewNativeType(4, gocql.TypeCustom, "map<int,int>")
	tests := []struct {
		name    string
		typ     gocql.TypeInfo
		in      any
		want    any
		wantErr string
	}{
		{name: "list", typ: list, in: []any{1, 2.0}, want: []any{1, 2}},
		{name: "set", typ: set, in: []any{1}, want: []any{1}},
		{name: "empty list is not nil", typ: list, in: []any{}, want: []any{}},
		{name: "nested list", typ: nested, in: []any{[]any{1}}, want: []any{[]any{1}}},
		{name: "map", typ: textMap, in: map[string]any{"a": 1}, want: map[string]any{"a": 1}},
		{name: "empty map is not nil", typ: textMap, in: map[string]any{}, want: map[string]any{}},
		{name: "list given an object", typ: list, in: map[string]any{"a": 1}, wantErr: fmt.Sprintf("cassandra: value map[a:1] (map[string]interface {}) is not a %s", list)},
		{name: "list given a string", typ: list, in: "x", wantErr: fmt.Sprintf("cassandra: value x (string) is not a %s", list)},
		{name: "map given an array", typ: textMap, in: []any{1}, wantErr: fmt.Sprintf("cassandra: value [1] ([]interface {}) is not a %s", textMap)},
		{name: "map with a key type that is not text", typ: intMap, in: map[string]any{"a": 1}, wantErr: fmt.Sprintf("cassandra: map key type %s is not supported for write", intMap.(gocql.CollectionType).Key)},
		{name: "nested element of the wrong kind", typ: nested, in: []any{1}, wantErr: "cassandra: value 1 (int) is not a " + fmt.Sprint(nested.(gocql.CollectionType).Elem)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindValue(tt.typ, tt.in)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
