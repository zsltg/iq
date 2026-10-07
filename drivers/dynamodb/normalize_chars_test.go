package dynamodb

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestNormalizeUnknownAttributeType(t *testing.T) {
	av := &types.UnknownUnionMember{Tag: "X"}
	require.Equal(t, fmt.Sprintf("%v", av), Normalize(av, numfmt.DecimalAuto))
}

func TestNormalizeDecimalModeReachesNestedValues(t *testing.T) {
	n := &types.AttributeValueMemberN{Value: "1.5"}
	tests := []struct {
		name string
		av   types.AttributeValue
		mode numfmt.DecimalMode
		want any
	}{
		{"map number mode", &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"a": n}}, numfmt.DecimalNumber, map[string]any{"a": 1.5}},
		{"map auto mode", &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"a": n}}, numfmt.DecimalAuto, map[string]any{"a": "1.5"}},
		{"list number mode", &types.AttributeValueMemberL{Value: []types.AttributeValue{n}}, numfmt.DecimalNumber, []any{1.5}},
		{"list auto mode", &types.AttributeValueMemberL{Value: []types.AttributeValue{n}}, numfmt.DecimalAuto, []any{"1.5"}},
		{"number set number mode", &types.AttributeValueMemberNS{Value: []string{"1.5"}}, numfmt.DecimalNumber, []any{1.5}},
		{"number set string mode", &types.AttributeValueMemberNS{Value: []string{"1.5"}}, numfmt.DecimalString, []any{"1.5"}},
		{
			"nested three levels",
			&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"l": &types.AttributeValueMemberL{Value: []types.AttributeValue{
				&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"x": n}},
			}}}},
			numfmt.DecimalNumber,
			map[string]any{"l": []any{map[string]any{"x": 1.5}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Normalize(tt.av, tt.mode))
		})
	}
}

func TestNormalizeEmptyCollectionsAreNotNil(t *testing.T) {
	tests := []struct {
		name string
		av   types.AttributeValue
		want any
	}{
		{"map", &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}, map[string]any{}},
		{"list", &types.AttributeValueMemberL{Value: []types.AttributeValue{}}, []any{}},
		{"string set", &types.AttributeValueMemberSS{Value: []string{}}, []any{}},
		{"number set", &types.AttributeValueMemberNS{Value: []string{}}, []any{}},
		{"binary set", &types.AttributeValueMemberBS{Value: [][]byte{}}, []any{}},
		{"nil map value", &types.AttributeValueMemberM{}, map[string]any{}},
		{"nil list value", &types.AttributeValueMemberL{}, []any{}},
		{"nil string set value", &types.AttributeValueMemberSS{}, []any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.av, numfmt.DecimalAuto)
			require.NotNil(t, got)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeNullAndMissingStayDistinct(t *testing.T) {
	tests := []struct {
		name string
		av   types.AttributeValue
		want any
	}{
		{"null false is still null", &types.AttributeValueMemberNULL{Value: false}, nil},
		{
			"null attribute keeps its key",
			&types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"a": &types.AttributeValueMemberNULL{Value: true}}},
			map[string]any{"a": nil},
		},
		{
			"null and nil list elements keep their index",
			&types.AttributeValueMemberL{Value: []types.AttributeValue{
				&types.AttributeValueMemberS{Value: "x"}, &types.AttributeValueMemberNULL{Value: true}, nil,
			}},
			[]any{"x", nil, nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Normalize(tt.av, numfmt.DecimalAuto))
		})
	}
}

func TestNormalizeSetsKeepOrderAndPrecision(t *testing.T) {
	tests := []struct {
		name string
		av   types.AttributeValue
		want any
	}{
		{
			"binary set",
			&types.AttributeValueMemberBS{Value: [][]byte{[]byte("hey"), []byte("a")}},
			[]any{"aGV5", "YQ=="},
		},
		{
			"number set with an overflowing integer",
			&types.AttributeValueMemberNS{Value: []string{"7", "123456789012345678901234567890"}},
			[]any{7, "123456789012345678901234567890"},
		},
		{"string set", &types.AttributeValueMemberSS{Value: []string{"b", "a"}}, []any{"b", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Normalize(tt.av, numfmt.DecimalAuto))
		})
	}
}

func TestToAttributeValueNumbersAndRejections(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want types.AttributeValue
	}{
		{"int64", int64(-7), &types.AttributeValueMemberN{Value: "-7"}},
		{"min int64", int64(math.MinInt64), &types.AttributeValueMemberN{Value: "-9223372036854775808"}},
		{"json number keeps its text", json.Number("1.50"), &types.AttributeValueMemberN{Value: "1.50"}},
		{"json number keeps a long integer", json.Number("123456789012345678901234567890"), &types.AttributeValueMemberN{Value: "123456789012345678901234567890"}},
		{"float exponent", 1e21, &types.AttributeValueMemberN{Value: "1e+21"}},
		{"float fraction", 0.1, &types.AttributeValueMemberN{Value: "0.1"}},
		{"float negative zero", math.Copysign(0, -1), &types.AttributeValueMemberN{Value: "-0"}},
		{"empty map", map[string]any{}, &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}},
		{"empty list", []any{}, &types.AttributeValueMemberL{Value: []types.AttributeValue{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			av, err := toAttributeValue(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, av)
		})
	}

	x := 1
	rejected := []struct {
		name string
		in   any
	}{
		{"uint", uint(1)},
		{"float32", float32(1)},
		{"int32", int32(1)},
		{"string slice", []string{"a"}},
		{"byte slice", []byte("a")},
		{"struct", struct{ X int }{X: 1}},
		{"pointer", &x},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			av, err := toAttributeValue(tt.in)
			require.Nil(t, av)
			require.EqualError(t, err, fmt.Sprintf("dynamodb: cannot write value %v (%T)", tt.in, tt.in))
		})
	}
}

func TestToAttributeValueNestedFailureKeepsTheInnerError(t *testing.T) {
	bad := struct{ X int }{X: 1}
	in := map[string]any{"a": []any{map[string]any{"b": bad}}}
	_, err := toAttributeValue(in)
	require.EqualError(t, err, fmt.Sprintf("dynamodb: cannot write value %v (%T)", bad, bad))
}

func TestToItemOrderAndKeyOverlay(t *testing.T) {
	single := &Store{keys: []KeyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}}
	_, err := single.toItem(recordValue{key: "zz", value: map[string]any{"bad": struct{}{}}})
	require.ErrorContains(t, err, "cannot write value") // attributes are built before the key is decoded

	composite := &Store{keys: []KeyAttr{
		{name: "pk", typ: types.ScalarAttributeTypeS},
		{name: "sk", typ: types.ScalarAttributeTypeN},
	}}
	item, err := composite.toItem(recordValue{key: `["a","1"]`, value: map[string]any{"pk": "other", "sk": 99, "x": nil}})
	require.NoError(t, err)
	require.Equal(t, map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "a"},
		"sk": &types.AttributeValueMemberN{Value: "1"},
		"x":  &types.AttributeValueMemberNULL{Value: true},
	}, item)

	_, err = composite.toItem(recordValue{key: "", value: map[string]any{"pk": "a"}})
	require.EqualError(t, err, `dynamodb: record missing key attribute "sk"`)
}

func TestParseItemErrorTexts(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"nested map", `{"a":{"M":{"b":{"X":1}}}}`, `dynamodb: attribute "a": dynamodb: attribute "b": unknown attribute type tag "X"`},
		{"list element has no index", `{"a":{"L":[{"S":"x"},{"Q":1}]}}`, `dynamodb: attribute "a": unknown attribute type tag "Q"`},
		{"two tags", `{"a":{"S":"x","N":"1"}}`, `dynamodb: attribute "a": attribute value must have exactly one type tag, got 2`},
		{"no tag", `{"a":{}}`, `dynamodb: attribute "a": attribute value must have exactly one type tag, got 0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseItem([]byte(tt.raw))
			require.EqualError(t, err, tt.want)
		})
	}
}

func TestParseItemErrorPrefixPerTag(t *testing.T) {
	tests := []struct {
		raw    string
		prefix string
	}{
		{`{"a":{"S":1}}`, `dynamodb: attribute "a": decode string value: `},
		{`{"a":{"N":1}}`, `dynamodb: attribute "a": decode string value: `},
		{`{"a":{"B":1}}`, `dynamodb: attribute "a": decode string value: `},
		{`{"a":{"B":"!"}}`, `dynamodb: attribute "a": decode B: `},
		{`{"a":{"BOOL":1}}`, `dynamodb: attribute "a": decode BOOL: `},
		{`{"a":{"NULL":1}}`, `dynamodb: attribute "a": decode NULL: `},
		{`{"a":{"L":1}}`, `dynamodb: attribute "a": decode L: `},
		{`{"a":{"SS":1}}`, `dynamodb: attribute "a": decode SS: `},
		{`{"a":{"NS":1}}`, `dynamodb: attribute "a": decode NS: `},
		{`{"a":{"BS":1}}`, `dynamodb: attribute "a": decode BS: `},
		{`{"a":{"BS":["!"]}}`, `dynamodb: attribute "a": decode BS element: `},
		{`{"a":{"M":1}}`, `dynamodb: attribute "a": dynamodb: decode item: `},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			_, err := ParseItem([]byte(tt.raw))
			require.Error(t, err)
			require.True(t, strings.HasPrefix(err.Error(), tt.prefix), err.Error())
		})
	}
}

func TestParseItemNullAndEmptyPayloads(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want types.AttributeValue
	}{
		{"null string", `{"S":null}`, &types.AttributeValueMemberS{Value: ""}},
		{"null string set", `{"SS":null}`, &types.AttributeValueMemberSS{Value: nil}},
		{"null number set", `{"NS":null}`, &types.AttributeValueMemberNS{Value: nil}},
		{"null binary set", `{"BS":null}`, &types.AttributeValueMemberBS{Value: [][]byte{}}},
		{"null list", `{"L":null}`, &types.AttributeValueMemberL{Value: []types.AttributeValue{}}},
		{"null map", `{"M":null}`, &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{}}},
		{"empty list", `{"L":[]}`, &types.AttributeValueMemberL{Value: []types.AttributeValue{}}},
		{"empty string set", `{"SS":[]}`, &types.AttributeValueMemberSS{Value: []string{}}},
		{"empty binary set", `{"BS":[]}`, &types.AttributeValueMemberBS{Value: [][]byte{}}},
		{"empty binary", `{"B":""}`, &types.AttributeValueMemberB{Value: []byte{}}},
		{
			"binary set keeps order",
			`{"BS":["YQ==","Yg=="]}`,
			&types.AttributeValueMemberBS{Value: [][]byte{[]byte("a"), []byte("b")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, err := ParseItem([]byte(`{"a":` + tt.raw + `}`))
			require.NoError(t, err)
			require.Equal(t, tt.want, item["a"])
		})
	}
}

func TestParseItemDeepListNesting(t *testing.T) {
	const depth = 50
	raw := `{"S":"leaf"}`
	for range depth {
		raw = `{"L":[` + raw + `]}`
	}
	item, err := ParseItem([]byte(`{"a":` + raw + `}`))
	require.NoError(t, err)
	var cur types.AttributeValue = item["a"]
	for range depth {
		l, ok := cur.(*types.AttributeValueMemberL)
		require.True(t, ok)
		require.Len(t, l.Value, 1)
		cur = l.Value[0]
	}
	require.Equal(t, &types.AttributeValueMemberS{Value: "leaf"}, cur)
}
