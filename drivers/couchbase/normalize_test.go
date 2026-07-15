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
		_, _, err := rawTranscoder{}.Encode("x")
		require.ErrorIs(t, err, errEncodeUnsupported)
	})
}
