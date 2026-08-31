package couchdb

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestDecodeDoc(t *testing.T) {
	bigLiteral := "123456789012345678901234567890"
	bigInt, _ := new(big.Int).SetString(bigLiteral, 10)

	tests := []struct {
		name string
		raw  string
		mode numfmt.DecimalMode
		want map[string]any
	}{
		{
			name: "scalars and identity fields are kept",
			raw:  `{"_id":"2","_rev":"1-abc","title":"Go","ok":true,"missing":null}`,
			mode: numfmt.DecimalAuto,
			want: map[string]any{"_id": "2", "_rev": "1-abc", "title": "Go", "ok": true, "missing": nil},
		},
		{
			name: "small integer stays an exact int",
			raw:  `{"year":2017}`,
			mode: numfmt.DecimalAuto,
			want: map[string]any{"year": 2017},
		},
		{
			name: "large integer keeps precision as big.Int",
			raw:  `{"n":` + bigLiteral + `}`,
			mode: numfmt.DecimalAuto,
			want: map[string]any{"n": bigInt},
		},
		{
			name: "fractional is a float in auto mode",
			raw:  `{"price":19.99}`,
			mode: numfmt.DecimalAuto,
			want: map[string]any{"price": 19.99},
		},
		{
			name: "fractional is the exact literal in string mode",
			raw:  `{"price":19.99}`,
			mode: numfmt.DecimalString,
			want: map[string]any{"price": "19.99"},
		},
		{
			name: "nested objects and arrays recurse",
			raw:  `{"meta":{"count":3},"tags":["a",2]}`,
			mode: numfmt.DecimalAuto,
			want: map[string]any{"meta": map[string]any{"count": 3}, "tags": []any{"a", 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeDoc(json.RawMessage(tt.raw), tt.mode)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeDocInvalid(t *testing.T) {
	got, err := decodeDoc(json.RawMessage(`{not json`), numfmt.DecimalAuto)
	require.Nil(t, got)
	require.ErrorContains(t, err, "decode couchdb document")

	// The decoder's own error is wrapped, not flattened to a string, so a caller
	// can still inspect the cause.
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
}

func TestDecodeDocRejectsANonObjectBody(t *testing.T) {
	// A document body is always a JSON object; anything else is a decode failure
	// rather than a silently empty document.
	got, err := decodeDoc(json.RawMessage(`[1,2]`), numfmt.DecimalAuto)
	require.Nil(t, got)
	require.ErrorContains(t, err, "decode couchdb document")
}
