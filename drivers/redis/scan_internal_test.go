package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestScanBatchesBoundsPageSize checks the memory-bounding heart of ScanBatches:
// keys accumulate into a page and flush at exactly pageSize. It runs in the redis
// package (not redis_test) so it can lower the unexported pageSize, and flushes
// its own database first so the batch sizes are deterministic regardless of what
// other tests left behind.
func TestScanBatchesBoundsPageSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	// TestMain pins IQ_REDIS_URL to a reserved database; the default matches it so
	// a stray run never flushes DB 0, which developers seed for manual exploration.
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/15"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, url, nil, numfmt.DecimalAuto)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.client.FlushDB(ctx).Err())
	const n = 5
	for i := range n {
		require.NoError(t, store.client.Set(ctx, fmt.Sprintf("iq:test:page:%d", i), "v", 0).Err())
	}
	store.pageSize = 2

	var sizes []int
	err = store.ScanBatches(ctx, func(batch map[string]any) error {
		sizes = append(sizes, len(batch))
		return nil
	})
	require.NoError(t, err)

	total, maxSize := 0, 0
	for _, s := range sizes {
		total += s
		if s > maxSize {
			maxSize = s
		}
	}
	require.Equal(t, n, total, "every key delivered once on a stable keyspace")
	require.LessOrEqual(t, maxSize, store.pageSize, "no page exceeds pageSize")
	require.Contains(t, sizes, store.pageSize, "a page flushes at exactly pageSize")
}

// bigInt builds a *big.Int from a decimal literal for the expected values below.
func bigInt(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	require.True(t, ok)
	return v
}

// TestDecodeJSONNumbers is the precision heart of the RedisJSON path: integers are
// always exact (int or *big.Int, so gojq does exact arithmetic), while a
// fractional number follows the decimal mode. It is a pure unit test — no server —
// because jsonReader only wraps a JSON.GET reply that decodeJSON then parses.
func TestDecodeJSONNumbers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		json string
		mode numfmt.DecimalMode
		want any
	}{
		{name: "fractional auto is a float", json: `1.5`, mode: numfmt.DecimalAuto, want: 1.5},
		{name: "fractional number is a float", json: `1.5`, mode: numfmt.DecimalNumber, want: 1.5},
		{name: "fractional string is the exact literal", json: `1.5`, mode: numfmt.DecimalString, want: "1.5"},
		{name: "negative fractional number is a float", json: `-2.5`, mode: numfmt.DecimalNumber, want: -2.5},
		{name: "negative fractional string is exact", json: `-2.5`, mode: numfmt.DecimalString, want: "-2.5"},
		{name: "high-precision string is exact", json: `0.12345678901234567890123`, mode: numfmt.DecimalString, want: "0.12345678901234567890123"},
		{name: "exponent number is a float", json: `1e3`, mode: numfmt.DecimalNumber, want: 1000.0},
		{name: "exponent string is the exact literal", json: `1e3`, mode: numfmt.DecimalString, want: "1e3"},
		{name: "small integer is an int", json: `42`, mode: numfmt.DecimalAuto, want: 42},
		{name: "integer above 2^53 stays exact in auto", json: `9007199254740993`, mode: numfmt.DecimalAuto, want: 9007199254740993},
		{name: "integer above 2^53 stays exact in string mode", json: `9007199254740993`, mode: numfmt.DecimalString, want: 9007199254740993},
		{name: "integer beyond int64 is a big int", json: `123456789012345678901234567890`, mode: numfmt.DecimalNumber, want: bigInt(t, "123456789012345678901234567890")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeJSON(tt.json, tt.mode)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestDecodeJSONRejectsMalformedDocument pins the decode guard: a reply that is not
// JSON is an error naming the stage and carrying the parser's own cause, never a
// silent nil value that would read as a stored JSON null.
func TestDecodeJSONRejectsMalformedDocument(t *testing.T) {
	t.Parallel()

	v, err := decodeJSON(`{"a":x}`, numfmt.DecimalAuto)

	require.Error(t, err)
	require.ErrorContains(t, err, "decode redis json")
	var syntaxErr *json.SyntaxError
	require.ErrorAs(t, err, &syntaxErr, "the parser's cause survives the wrap")
	require.Nil(t, v)
}

// TestConvertNumbersRecurses checks that number conversion reaches into nested
// objects and arrays, not just the top-level value.
func TestConvertNumbersRecurses(t *testing.T) {
	t.Parallel()
	got, err := decodeJSON(`{"n": 9007199254740993, "xs": [1.5, {"m": 2.5}]}`, numfmt.DecimalString)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"n":  9007199254740993,
		"xs": []any{"1.5", map[string]any{"m": "2.5"}},
	}, got)
}
