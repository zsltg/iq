package dynamodb

import (
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
			wantErr: "empty key attribute",
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
