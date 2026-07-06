package dynamodb

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		av   types.AttributeValue
		mode numfmt.DecimalMode
		want any
	}{
		{"string", &types.AttributeValueMemberS{Value: "hi"}, numfmt.DecimalAuto, "hi"},
		{"bool", &types.AttributeValueMemberBOOL{Value: true}, numfmt.DecimalAuto, true},
		{"null", &types.AttributeValueMemberNULL{Value: true}, numfmt.DecimalAuto, nil},
		{"nil interface", nil, numfmt.DecimalAuto, nil},
		{"integer number", &types.AttributeValueMemberN{Value: "42"}, numfmt.DecimalAuto, 42},
		{"negative integer", &types.AttributeValueMemberN{Value: "-7"}, numfmt.DecimalAuto, -7},
		{"decimal auto keeps string", &types.AttributeValueMemberN{Value: "3.14"}, numfmt.DecimalAuto, "3.14"},
		{"decimal string mode keeps string", &types.AttributeValueMemberN{Value: "3.14"}, numfmt.DecimalString, "3.14"},
		{"decimal number mode floats", &types.AttributeValueMemberN{Value: "3.5"}, numfmt.DecimalNumber, 3.5},
		{"huge integer keeps exact string", &types.AttributeValueMemberN{Value: "123456789012345678901234567890"}, numfmt.DecimalAuto, "123456789012345678901234567890"},
		{"binary base64", &types.AttributeValueMemberB{Value: []byte("hey")}, numfmt.DecimalAuto, "aGV5"},
		{
			name: "map recurses",
			av:   &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"a": &types.AttributeValueMemberN{Value: "1"}}},
			mode: numfmt.DecimalAuto,
			want: map[string]any{"a": 1},
		},
		{
			name: "list recurses",
			av:   &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "x"}, &types.AttributeValueMemberBOOL{Value: false}}},
			mode: numfmt.DecimalAuto,
			want: []any{"x", false},
		},
		{
			name: "string set to list",
			av:   &types.AttributeValueMemberSS{Value: []string{"a", "b"}},
			mode: numfmt.DecimalAuto,
			want: []any{"a", "b"},
		},
		{
			name: "number set to list of numbers",
			av:   &types.AttributeValueMemberNS{Value: []string{"1", "2"}},
			mode: numfmt.DecimalAuto,
			want: []any{1, 2},
		},
		{
			name: "binary set to list of base64",
			av:   &types.AttributeValueMemberBS{Value: [][]byte{[]byte("hey")}},
			mode: numfmt.DecimalAuto,
			want: []any{"aGV5"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Normalize(tt.av, tt.mode))
		})
	}
}

func TestNumberValue(t *testing.T) {
	require.Equal(t, 10, numberValue("10", numfmt.DecimalAuto))
	require.Equal(t, 10, numberValue("10", numfmt.DecimalNumber)) // integers stay int in every mode
	require.Equal(t, "1.5", numberValue("1.5", numfmt.DecimalAuto))
	require.Equal(t, "1.5", numberValue("1.5", numfmt.DecimalString))
	require.Equal(t, 1.5, numberValue("1.5", numfmt.DecimalNumber))
}

func TestIsIntLiteral(t *testing.T) {
	for _, s := range []string{"0", "42", "-7", "+3", "123456789012345678901234567890"} {
		require.True(t, isIntLiteral(s), s)
	}
	for _, s := range []string{"", "-", "+", "1.5", "1e3", "0x1", "12a"} {
		require.False(t, isIntLiteral(s), s)
	}
}

func TestKeyRoundTripSingle(t *testing.T) {
	s := &Store{keys: []keyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}}
	item := map[string]types.AttributeValue{
		"id":    &types.AttributeValueMemberN{Value: "42"},
		"title": &types.AttributeValueMemberS{Value: "Go"},
	}
	key := s.keyOf(item)
	require.Equal(t, "42", key)

	decoded, err := s.decodeKey(key)
	require.NoError(t, err)
	require.Equal(t, map[string]types.AttributeValue{"id": &types.AttributeValueMemberN{Value: "42"}}, decoded)
}

func TestKeyRoundTripComposite(t *testing.T) {
	s := &Store{keys: []keyAttr{
		{name: "pk", typ: types.ScalarAttributeTypeS},
		{name: "sk", typ: types.ScalarAttributeTypeN},
	}}
	item := map[string]types.AttributeValue{
		"pk":   &types.AttributeValueMemberS{Value: "user#1"},
		"sk":   &types.AttributeValueMemberN{Value: "7"},
		"note": &types.AttributeValueMemberS{Value: "x"},
	}
	key := s.keyOf(item)
	require.Equal(t, `["user#1","7"]`, key)

	decoded, err := s.decodeKey(key)
	require.NoError(t, err)
	require.Equal(t, map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "user#1"},
		"sk": &types.AttributeValueMemberN{Value: "7"},
	}, decoded)
}

func TestDecodeKeyErrors(t *testing.T) {
	single := &Store{keys: []keyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}}
	_, err := single.decodeKey("not-a-number")
	require.ErrorContains(t, err, "is not a number")

	composite := &Store{keys: []keyAttr{
		{name: "pk", typ: types.ScalarAttributeTypeS},
		{name: "sk", typ: types.ScalarAttributeTypeS},
	}}
	_, err = composite.decodeKey(`["only-one"]`)
	require.ErrorContains(t, err, "want 2")

	_, err = composite.decodeKey("not-json")
	require.ErrorContains(t, err, "decode composite key")

	none := &Store{}
	_, err = none.decodeKey("x")
	require.ErrorIs(t, err, errNoTable)
}

func TestKeyAV(t *testing.T) {
	sv, err := keyAV(keyAttr{name: "k", typ: types.ScalarAttributeTypeS}, "hello")
	require.NoError(t, err)
	require.Equal(t, &types.AttributeValueMemberS{Value: "hello"}, sv)

	nv, err := keyAV(keyAttr{name: "k", typ: types.ScalarAttributeTypeN}, "3.5")
	require.NoError(t, err)
	require.Equal(t, &types.AttributeValueMemberN{Value: "3.5"}, nv)

	_, err = keyAV(keyAttr{name: "k", typ: types.ScalarAttributeTypeN}, "abc")
	require.ErrorContains(t, err, "is not a number")

	bv, err := keyAV(keyAttr{name: "k", typ: types.ScalarAttributeTypeB}, "aGV5")
	require.NoError(t, err)
	require.Equal(t, &types.AttributeValueMemberB{Value: []byte("hey")}, bv)

	_, err = keyAV(keyAttr{name: "k", typ: types.ScalarAttributeTypeB}, "!!!not-base64")
	require.ErrorContains(t, err, "is not base64")
}

func TestToAttributeValue(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want types.AttributeValue
	}{
		{"nil", nil, &types.AttributeValueMemberNULL{Value: true}},
		{"bool", true, &types.AttributeValueMemberBOOL{Value: true}},
		{"string", "x", &types.AttributeValueMemberS{Value: "x"}},
		{"int", 5, &types.AttributeValueMemberN{Value: "5"}},
		{"float", 2.5, &types.AttributeValueMemberN{Value: "2.5"}},
		{"map", map[string]any{"a": 1}, &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"a": &types.AttributeValueMemberN{Value: "1"}}}},
		{"list", []any{"a"}, &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "a"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			av, err := toAttributeValue(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, av)
		})
	}

	_, err := toAttributeValue(struct{ X int }{X: 1})
	require.ErrorContains(t, err, "cannot write value")
}

func TestToItem(t *testing.T) {
	s := &Store{keys: []keyAttr{{name: "id", typ: types.ScalarAttributeTypeN}}}

	// Key from the record key overrides the value object's key attribute.
	item, err := s.toItem(recordValue{key: "42", value: map[string]any{"id": 999, "title": "Go"}})
	require.NoError(t, err)
	require.Equal(t, &types.AttributeValueMemberN{Value: "42"}, item["id"])
	require.Equal(t, &types.AttributeValueMemberS{Value: "Go"}, item["title"])

	// A keyless record must carry its key attribute in the object.
	item, err = s.toItem(recordValue{key: "", value: map[string]any{"id": 7}})
	require.NoError(t, err)
	require.Equal(t, &types.AttributeValueMemberN{Value: "7"}, item["id"])

	// Missing key attribute is an error.
	_, err = s.toItem(recordValue{key: "", value: map[string]any{"title": "Go"}})
	require.ErrorContains(t, err, "missing key attribute")

	// A non-object value is an error.
	_, err = s.toItem(recordValue{key: "1", value: "not-an-object"})
	require.ErrorContains(t, err, "must be an item object")
}
