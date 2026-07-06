package cassandra

import (
	"math/big"
	"net"
	"testing"
	"time"

	gocql "github.com/apache/cassandra-gocql-driver/v2"
	"github.com/stretchr/testify/require"
	"gopkg.in/inf.v0"

	"github.com/zsltg/iq/internal/numfmt"
)

// nativeType builds a scalar CQL TypeInfo for a test, so schema-shaped helpers need
// no live cluster.
func nativeType(t gocql.Type) gocql.TypeInfo {
	return gocql.NewNativeType(4, t, "")
}

// column builds a ColumnMetadata for a test table.
func column(name string, t gocql.Type) *gocql.ColumnMetadata {
	return &gocql.ColumnMetadata{Name: name, Type: nativeType(t)}
}

// tableMetaFor assembles a TableMetadata from its partition, clustering, and regular
// columns, indexing every column by name as the live metadata does.
func tableMetaFor(partition, clustering, regular []*gocql.ColumnMetadata) *gocql.TableMetadata {
	m := &gocql.TableMetadata{
		Name:              "t",
		PartitionKey:      partition,
		ClusteringColumns: clustering,
		Columns:           map[string]*gocql.ColumnMetadata{},
	}
	for _, set := range [][]*gocql.ColumnMetadata{partition, clustering, regular} {
		for _, c := range set {
			m.Columns[c.Name] = c
		}
	}
	return m
}

func mustUUID(t *testing.T, s string) gocql.UUID {
	t.Helper()
	u, err := gocql.ParseUUID(s)
	require.NoError(t, err)
	return u
}

func TestNormalize(t *testing.T) {
	uuidStr := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name string
		mode numfmt.DecimalMode
		in   any
		want any
	}{
		{name: "nil", in: nil, want: nil},
		{name: "bool", in: true, want: true},
		{name: "string", in: "hello", want: "hello"},
		{name: "int stays int", in: 7, want: 7},
		{name: "int32 to int", in: int32(7), want: 7},
		{name: "int64 to int", in: int64(7), want: 7},
		{name: "float64", in: 3.5, want: 3.5},
		{name: "float32 to float64", in: float32(2.5), want: 2.5},
		{name: "small varint to int", in: big.NewInt(42), want: 42},
		{name: "huge varint to string", in: mustBig("123456789012345678901234567890"), want: "123456789012345678901234567890"},
		{name: "decimal auto keeps string", mode: numfmt.DecimalAuto, in: inf.NewDec(314, 2), want: "3.14"},
		{name: "decimal number to float", mode: numfmt.DecimalNumber, in: inf.NewDec(314, 2), want: 3.14},
		{name: "timestamp to rfc3339", in: time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC), want: "2021-01-02T03:04:05Z"},
		{name: "uuid to string", in: mustUUID(t, uuidStr), want: uuidStr},
		{name: "blob to base64", in: []byte("hi"), want: "aGk="},
		{name: "inet to string", in: net.ParseIP("10.0.0.1"), want: "10.0.0.1"},
		{name: "list to slice", in: []int{1, 2, 3}, want: []any{1, 2, 3}},
		{name: "map to object", in: map[string]int{"a": 1}, want: map[string]any{"a": 1}},
		{name: "nested list of maps", in: []map[string]int{{"a": 1}}, want: []any{map[string]any{"a": 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Normalize(tt.in, tt.mode))
		})
	}
}

func TestKeyOf(t *testing.T) {
	tests := []struct {
		name string
		meta *gocql.TableMetadata
		row  map[string]any
		want string
	}{
		{
			name: "single int key is bare",
			meta: tableMetaFor([]*gocql.ColumnMetadata{column("id", gocql.TypeInt)}, nil, nil),
			row:  map[string]any{"id": 42, "title": "x"},
			want: "42",
		},
		{
			name: "single text key is bare",
			meta: tableMetaFor([]*gocql.ColumnMetadata{column("id", gocql.TypeText)}, nil, nil),
			row:  map[string]any{"id": "abc"},
			want: "abc",
		},
		{
			name: "composite key is json array in schema order",
			meta: tableMetaFor(
				[]*gocql.ColumnMetadata{column("country", gocql.TypeText)},
				[]*gocql.ColumnMetadata{column("zip", gocql.TypeInt)},
				nil,
			),
			row:  map[string]any{"country": "US", "zip": 10001},
			want: `["US","10001"]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, KeyOf(tt.meta, tt.row))
		})
	}
}

func TestDecodeKey(t *testing.T) {
	single := tableMetaFor([]*gocql.ColumnMetadata{column("id", gocql.TypeInt)}, nil, nil)
	composite := tableMetaFor(
		[]*gocql.ColumnMetadata{column("country", gocql.TypeText)},
		[]*gocql.ColumnMetadata{column("zip", gocql.TypeInt)},
		nil,
	)

	t.Run("single column", func(t *testing.T) {
		got, err := decodeKey(single, "42")
		require.NoError(t, err)
		require.Equal(t, []any{42}, got)
	})
	t.Run("composite in schema order", func(t *testing.T) {
		got, err := decodeKey(composite, `["US","10001"]`)
		require.NoError(t, err)
		require.Equal(t, []any{"US", 10001}, got)
	})
	t.Run("wrong part count", func(t *testing.T) {
		_, err := decodeKey(composite, `["US"]`)
		require.ErrorContains(t, err, "want 2")
	})
	t.Run("unparseable single", func(t *testing.T) {
		_, err := decodeKey(single, "notanint")
		require.ErrorContains(t, err, "not an integer")
	})
	t.Run("composite not json", func(t *testing.T) {
		_, err := decodeKey(composite, "US,10001")
		require.ErrorContains(t, err, "decode composite key")
	})
}

func TestKeyRoundTrip(t *testing.T) {
	meta := tableMetaFor(
		[]*gocql.ColumnMetadata{column("country", gocql.TypeText)},
		[]*gocql.ColumnMetadata{column("zip", gocql.TypeInt)},
		nil,
	)
	row := map[string]any{"country": "US", "zip": 10001}
	got, err := decodeKey(meta, KeyOf(meta, row))
	require.NoError(t, err)
	require.Equal(t, []any{"US", 10001}, got)
}

func TestBindValue(t *testing.T) {
	uuidStr := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name    string
		typ     gocql.Type
		in      any
		want    any
		wantErr string
	}{
		{name: "text", typ: gocql.TypeText, in: "x", want: "x"},
		{name: "int from whole float", typ: gocql.TypeInt, in: float64(5), want: 5},
		{name: "int from int", typ: gocql.TypeInt, in: 5, want: 5},
		{name: "int from non-whole float errors", typ: gocql.TypeInt, in: 5.5, wantErr: "whole number"},
		{name: "double", typ: gocql.TypeDouble, in: float64(1.5), want: 1.5},
		{name: "boolean", typ: gocql.TypeBoolean, in: true, want: true},
		{name: "boolean mismatch errors", typ: gocql.TypeBoolean, in: "true", wantErr: "is not a"},
		{name: "uuid from string", typ: gocql.TypeUUID, in: uuidStr, want: mustUUID(t, uuidStr)},
		{name: "timestamp from rfc3339", typ: gocql.TypeTimestamp, in: "2021-01-02T03:04:05Z", want: time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)},
		{name: "blob from base64", typ: gocql.TypeBlob, in: "aGk=", want: []byte("hi")},
		{name: "inet from string", typ: gocql.TypeInet, in: "10.0.0.1", want: net.ParseIP("10.0.0.1")},
		{name: "varint from string", typ: gocql.TypeVarint, in: "123456789012345678901234567890", want: mustBig("123456789012345678901234567890")},
		{name: "varint from whole float", typ: gocql.TypeVarint, in: float64(42), want: mustBig("42")},
		{name: "bigint from int64", typ: gocql.TypeBigInt, in: int64(9), want: 9},
		{name: "int from big.Int", typ: gocql.TypeInt, in: mustBig("5"), want: 5},
		{name: "decimal from string", typ: gocql.TypeDecimal, in: "3.14", want: mustDec("3.14")},
		{name: "decimal from float keeps all decimals", typ: gocql.TypeDecimal, in: float64(3.14159), want: mustDec("3.14159")},
		{name: "decimal from int", typ: gocql.TypeDecimal, in: 5, want: mustDec("5")},
		{name: "double from int", typ: gocql.TypeDouble, in: 5, want: float64(5)},
		{name: "nil passes through", typ: gocql.TypeText, in: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindValue(nativeType(tt.typ), tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBindKeyValue(t *testing.T) {
	uuidStr := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name    string
		typ     gocql.Type
		in      string
		want    any
		wantErr string
	}{
		{name: "text", typ: gocql.TypeText, in: "abc", want: "abc"},
		{name: "int", typ: gocql.TypeInt, in: "42", want: 42},
		{name: "int invalid", typ: gocql.TypeInt, in: "x", wantErr: "not an integer"},
		{name: "bigint", typ: gocql.TypeBigInt, in: "9000000000", want: int64(9000000000)},
		{name: "bigint invalid", typ: gocql.TypeBigInt, in: "x", wantErr: "not a bigint"},
		{name: "varint", typ: gocql.TypeVarint, in: "123456789012345678901234567890", want: mustBig("123456789012345678901234567890")},
		{name: "varint invalid", typ: gocql.TypeVarint, in: "x", wantErr: "not a varint"},
		{name: "float", typ: gocql.TypeFloat, in: "1.5", want: float32(1.5)},
		{name: "float invalid", typ: gocql.TypeFloat, in: "x", wantErr: "not a float"},
		{name: "double", typ: gocql.TypeDouble, in: "1.5", want: 1.5},
		{name: "double invalid", typ: gocql.TypeDouble, in: "x", wantErr: "not a double"},
		{name: "decimal", typ: gocql.TypeDecimal, in: "3.14", want: mustDec("3.14")},
		{name: "decimal invalid", typ: gocql.TypeDecimal, in: "x", wantErr: "not a decimal"},
		{name: "boolean", typ: gocql.TypeBoolean, in: "true", want: true},
		{name: "boolean invalid", typ: gocql.TypeBoolean, in: "x", wantErr: "not a boolean"},
		{name: "uuid", typ: gocql.TypeUUID, in: uuidStr, want: mustUUID(t, uuidStr)},
		{name: "uuid invalid", typ: gocql.TypeUUID, in: "x", wantErr: "not a uuid"},
		{name: "timestamp", typ: gocql.TypeTimestamp, in: "2021-01-02T03:04:05Z", want: time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)},
		{name: "timestamp invalid", typ: gocql.TypeTimestamp, in: "x", wantErr: "not a timestamp"},
		{name: "blob", typ: gocql.TypeBlob, in: "aGk=", want: []byte("hi")},
		{name: "blob invalid", typ: gocql.TypeBlob, in: "!!!", wantErr: "base64"},
		{name: "inet", typ: gocql.TypeInet, in: "10.0.0.1", want: net.ParseIP("10.0.0.1")},
		{name: "inet invalid", typ: gocql.TypeInet, in: "x", wantErr: "not an ip"},
		{name: "unknown type binds as string", typ: gocql.TypeDuration, in: "raw", want: "raw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bindKeyValue(nativeType(tt.typ), tt.in)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestKeyOfTypedColumns(t *testing.T) {
	uuidStr := "11111111-1111-1111-1111-111111111111"
	meta := tableMetaFor([]*gocql.ColumnMetadata{column("id", gocql.TypeUUID)}, nil, nil)
	require.Equal(t, uuidStr, KeyOf(meta, map[string]any{"id": mustUUID(t, uuidStr)}))

	tsMeta := tableMetaFor([]*gocql.ColumnMetadata{column("at", gocql.TypeTimestamp)}, nil, nil)
	ts := time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)
	require.Equal(t, "2021-01-02T03:04:05Z", KeyOf(tsMeta, map[string]any{"at": ts}))
}

func TestDecimalValueNil(t *testing.T) {
	require.Nil(t, decimalValue(nil, numfmt.DecimalAuto))
}

func mustBig(s string) *big.Int {
	z, _ := new(big.Int).SetString(s, 10)
	return z
}

func mustDec(s string) *inf.Dec {
	d, _ := new(inf.Dec).SetString(s)
	return d
}
