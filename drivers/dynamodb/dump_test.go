package dynamodb

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestParseKeySchema(t *testing.T) {
	tests := []struct {
		name    string
		hint    string
		want    []KeyAttr
		wantErr string
	}{
		{
			name: "partition only defaults to string",
			hint: "pk",
			want: []KeyAttr{{name: "pk", typ: types.ScalarAttributeTypeS}},
		},
		{
			name: "composite with explicit types",
			hint: "pk:S,sk:N",
			want: []KeyAttr{{name: "pk", typ: types.ScalarAttributeTypeS}, {name: "sk", typ: types.ScalarAttributeTypeN}},
		},
		{
			name: "binary key and whitespace tolerated",
			hint: " pk : B ",
			want: []KeyAttr{{name: "pk", typ: types.ScalarAttributeTypeB}},
		},
		{
			name:    "unknown type",
			hint:    "pk:Q",
			wantErr: "unknown key type",
		},
		{
			name:    "empty attribute",
			hint:    "pk,",
			wantErr: `empty key attribute in ?keys="pk,"`,
		},
		{
			name:    "empty attribute name before a type suffix",
			hint:    ":S",
			wantErr: `empty key attribute name in ?keys=":S"`,
		},
		{
			name:    "too many attributes",
			hint:    "a,b,c",
			wantErr: "want 1 (partition) or 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseKeySchema(tc.hint)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseItemAllTypes(t *testing.T) {
	raw := []byte(`{
		"s":{"S":"hi"},
		"n":{"N":"42"},
		"b":{"B":"aGk="},
		"yes":{"BOOL":true},
		"nil":{"NULL":true},
		"m":{"M":{"inner":{"S":"deep"}}},
		"l":{"L":[{"N":"1"},{"S":"x"}]},
		"ss":{"SS":["a","b"]},
		"ns":{"NS":["1","2"]},
		"bs":{"BS":["aGk="]}
	}`)
	item, err := ParseItem(raw)
	require.NoError(t, err)

	// Normalizing the parsed item must yield exactly the live-scan shape.
	got := make(map[string]any, len(item))
	for k, v := range item {
		got[k] = Normalize(v, numfmt.DecimalAuto)
	}
	require.Equal(t, map[string]any{
		"s":   "hi",
		"n":   42,
		"b":   "aGk=",
		"yes": true,
		"nil": nil,
		"m":   map[string]any{"inner": "deep"},
		"l":   []any{1, "x"},
		"ss":  []any{"a", "b"},
		"ns":  []any{1, 2},
		"bs":  []any{"aGk="},
	}, got)
}

func TestParseItemErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		msg  string
	}{
		{"two tags", `{"a":{"S":"x","N":"1"}}`, "exactly one type tag"},
		{"unknown tag", `{"a":{"X":"x"}}`, "unknown attribute type tag"},
		{"bad base64", `{"a":{"B":"!!!"}}`, "decode B"},
		{"not an object", `{"a":"plain"}`, "decode attribute value"},
		{"item is not an object", `[1,2]`, "dynamodb: decode item"},
		{"bool payload is not a bool", `{"a":{"BOOL":"yes"}}`, "decode BOOL"},
		{"null payload is not a bool", `{"a":{"NULL":"yes"}}`, "decode NULL"},
		{"list payload is not an array", `{"a":{"L":"x"}}`, "decode L"},
		{"string set payload is not an array", `{"a":{"SS":"x"}}`, "decode SS"},
		{"number set payload is not an array", `{"a":{"NS":"x"}}`, "decode NS"},
		{"binary set payload is not an array", `{"a":{"BS":"x"}}`, "decode BS"},
		{"binary set element is not base64", `{"a":{"BS":["!!!"]}}`, "decode BS element"},
		{"string payload is not a string", `{"a":{"S":1}}`, "decode string value"},
		{"binary payload is not a string", `{"a":{"B":1}}`, "decode string value"},
		{"map payload is not an object", `{"a":{"M":"x"}}`, "decode item"},
		{"nested list element is malformed", `{"a":{"L":["plain"]}}`, "decode attribute value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseItem([]byte(tc.raw))
			require.ErrorContains(t, err, tc.msg)
		})
	}
}

func TestKeyOfParity(t *testing.T) {
	item := map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "a"},
		"sk": &types.AttributeValueMemberN{Value: "3"},
	}
	single, err := ParseKeySchema("pk")
	require.NoError(t, err)
	require.Equal(t, "a", KeyOf(single, item))

	composite, err := ParseKeySchema("pk:S,sk:N")
	require.NoError(t, err)
	require.Equal(t, `["a","3"]`, KeyOf(composite, item))
}

// TestParseItemTypedValues pins the typed attribute values ParseItem builds, not just
// their normalized form: a NULL's payload, for one, normalizes to nil whatever the
// decoded boolean was, so only the attribute value itself proves the payload was read.
func TestParseItemTypedValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want types.AttributeValue
	}{
		{"null true", `{"NULL":true}`, &types.AttributeValueMemberNULL{Value: true}},
		{"null false", `{"NULL":false}`, &types.AttributeValueMemberNULL{Value: false}},
		{"bool true", `{"BOOL":true}`, &types.AttributeValueMemberBOOL{Value: true}},
		{"bool false", `{"BOOL":false}`, &types.AttributeValueMemberBOOL{Value: false}},
		{"string", `{"S":"hi"}`, &types.AttributeValueMemberS{Value: "hi"}},
		{"number", `{"N":"42"}`, &types.AttributeValueMemberN{Value: "42"}},
		{"binary", `{"B":"aGk="}`, &types.AttributeValueMemberB{Value: []byte("hi")}},
		{"string set", `{"SS":["a","b"]}`, &types.AttributeValueMemberSS{Value: []string{"a", "b"}}},
		{"number set", `{"NS":["1"]}`, &types.AttributeValueMemberNS{Value: []string{"1"}}},
		{"binary set", `{"BS":["aGk="]}`, &types.AttributeValueMemberBS{Value: [][]byte{[]byte("hi")}}},
		{
			name: "map",
			raw:  `{"M":{"inner":{"S":"deep"}}}`,
			want: &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"inner": &types.AttributeValueMemberS{Value: "deep"},
			}},
		},
		{
			name: "list",
			raw:  `{"L":[{"N":"1"},{"BOOL":false}]}`,
			want: &types.AttributeValueMemberL{Value: []types.AttributeValue{
				&types.AttributeValueMemberN{Value: "1"},
				&types.AttributeValueMemberBOOL{Value: false},
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item, err := ParseItem([]byte(`{"a":` + tc.raw + `}`))
			require.NoError(t, err)
			require.Equal(t, tc.want, item["a"])
		})
	}
}

// TestParseItemErrorsUnwrap pins that a decode failure keeps the standard-library
// cause in the chain, so a caller can inspect it with errors.As rather than only
// reading a rendered string.
func TestParseItemErrorsUnwrap(t *testing.T) {
	t.Run("attribute value is not a tagged object", func(t *testing.T) {
		_, err := ParseItem([]byte(`{"a":"plain"}`))
		var typeErr *json.UnmarshalTypeError
		require.ErrorAs(t, err, &typeErr)
	})

	t.Run("string payload is not a string", func(t *testing.T) {
		_, err := ParseItem([]byte(`{"a":{"S":1}}`))
		var typeErr *json.UnmarshalTypeError
		require.ErrorAs(t, err, &typeErr)
	})

	t.Run("binary payload is not base64", func(t *testing.T) {
		_, err := ParseItem([]byte(`{"a":{"B":"!!!"}}`))
		var corrupt base64.CorruptInputError
		require.ErrorAs(t, err, &corrupt)
	})

	t.Run("item is not an object", func(t *testing.T) {
		_, err := ParseItem([]byte(`[1,2]`))
		var typeErr *json.UnmarshalTypeError
		require.ErrorAs(t, err, &typeErr)
	})
}
