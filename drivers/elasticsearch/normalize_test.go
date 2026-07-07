package elasticsearch

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

func TestDecodeSource(t *testing.T) {
	t.Run("injects the id and preserves fields", func(t *testing.T) {
		doc, err := decodeSource(json.RawMessage(`{"title":"Dune","year":1965}`), "1", numfmt.DecimalAuto)
		require.NoError(t, err)
		require.Equal(t, "Dune", doc["title"])
		require.Equal(t, 1965, doc["year"])
		require.Equal(t, "1", doc["_id"])
	})

	t.Run("absent source still yields the id", func(t *testing.T) {
		doc, err := decodeSource(nil, "42", numfmt.DecimalAuto)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"_id": "42"}, doc)
	})

	t.Run("big integer keeps exact precision", func(t *testing.T) {
		doc, err := decodeSource(json.RawMessage(`{"n":123456789012345678901234567890}`), "1", numfmt.DecimalAuto)
		require.NoError(t, err)
		want, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
		require.Equal(t, want, doc["n"])
	})

	t.Run("nested numbers are converted recursively", func(t *testing.T) {
		doc, err := decodeSource(json.RawMessage(`{"o":{"a":7},"l":[1,2]}`), "1", numfmt.DecimalAuto)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"a": 7}, doc["o"])
		require.Equal(t, []any{1, 2}, doc["l"])
	})
}

func TestFormatRaw(t *testing.T) {
	// A successful render returns indented JSON (quoted keys), not the Go %v form the
	// error fallback would produce.
	out := (&Store{}).FormatRaw(map[string]any{"a": 1}, false)
	require.Contains(t, out, `"a"`)
	require.Contains(t, out, "1")
}
