package couchbase

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestDecodeValue(t *testing.T) {
	t.Run("object with an exact large integer", func(t *testing.T) {
		v := decodeValue([]byte(`{"n": 9223372036854775807, "s": "x"}`), numfmt.DecimalAuto)
		m, ok := v.(map[string]any)
		require.True(t, ok)
		require.Equal(t, int(9223372036854775807), m["n"])
		require.Equal(t, "x", m["s"])
	})

	t.Run("integer beyond int64 stays exact as big.Int", func(t *testing.T) {
		v := decodeValue([]byte(`{"n": 92233720368547758070}`), numfmt.DecimalAuto)
		m := v.(map[string]any)
		want, _ := new(big.Int).SetString("92233720368547758070", 10)
		require.Equal(t, want, m["n"])
	})

	t.Run("non-json body falls back to a string", func(t *testing.T) {
		v := decodeValue([]byte("\x00\x01binary"), numfmt.DecimalAuto)
		require.Equal(t, "\x00\x01binary", v)
	})

	t.Run("bare scalar decodes", func(t *testing.T) {
		require.Equal(t, "hello", decodeValue([]byte(`"hello"`), numfmt.DecimalAuto))
	})
}

func TestDecodeValueTrailingData(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want any
	}{
		{name: "trailing garbage", raw: `{"a":1}garbage`, want: `{"a":1}garbage`},
		{name: "two concatenated objects", raw: `{"a":1}{"b":2}`, want: `{"a":1}{"b":2}`},
		{name: "a stray closing brace", raw: `{"a":1}}`, want: `{"a":1}}`},
		{name: "a trailing comma", raw: `{"a":1},`, want: `{"a":1},`},
		{name: "two scalars", raw: `1 2`, want: `1 2`},
		{name: "trailing whitespace only", raw: "{\"a\":1} \n\t", want: map[string]any{"a": 1}},
		{name: "valid object", raw: `{"a":1}`, want: map[string]any{"a": 1}},
		{name: "invalid body", raw: "\x00\x01binary", want: "\x00\x01binary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, decodeValue([]byte(tt.raw), numfmt.DecimalAuto))
		})
	}
}

func TestRawTranscoder(t *testing.T) {
	t.Run("decode copies bytes into a byte slice", func(t *testing.T) {
		var out []byte
		require.NoError(t, rawTranscoder{}.Decode([]byte(`{"a":1}`), 0, &out))
		require.Equal(t, []byte(`{"a":1}`), out)
	})
	t.Run("decode refuses a non-byte target", func(t *testing.T) {
		var out string
		require.ErrorIs(t, rawTranscoder{}.Decode([]byte(`x`), 0, &out), errNonBytesTarget)
	})
	t.Run("encode is unsupported", func(t *testing.T) {
		out, flags, err := rawTranscoder{}.Encode("x")
		require.ErrorIs(t, err, errEncodeUnsupported)
		// A refusal encodes nothing, so it reports no bytes and no datatype flag.
		require.Nil(t, out)
		require.Zero(t, flags)
	})
}
