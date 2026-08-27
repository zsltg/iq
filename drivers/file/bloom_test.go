package file

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBloomNoFalseNegatives is the load-bearing guarantee: every key added must
// test positive, so the index never hides a page that holds a wanted key.
func TestBloomNoFalseNegatives(t *testing.T) {
	keys := make([]string, pageSize)
	f := newBloom(len(keys))
	for i := range keys {
		keys[i] = fmt.Sprintf("key:%d", i)
		bloomAdd(f, keys[i])
	}
	for _, k := range keys {
		require.True(t, bloomHas(f, k), "added key %q must test positive", k)
	}
}

// TestBloomBinaryKeys confirms non-UTF-8 keys (a Redis key can be arbitrary bytes)
// hash and round-trip through the filter.
func TestBloomBinaryKeys(t *testing.T) {
	f := newBloom(4)
	bin := string([]byte{0xff, 0x00, 0xfe, 0x41})
	bloomAdd(f, bin)
	require.True(t, bloomHas(f, bin))
	require.False(t, bloomHas(f, "plain"))
}

// TestBloomFalsePositiveRate keeps the filter honest: absent keys mostly test
// negative, so page-skipping actually skips. A loose bound (well above the ~1%
// target) makes the test deterministic rather than flaky.
func TestBloomFalsePositiveRate(t *testing.T) {
	f := newBloom(pageSize)
	for i := range pageSize {
		bloomAdd(f, fmt.Sprintf("present:%d", i))
	}
	const trials = 5000
	fp := 0
	for i := range trials {
		if bloomHas(f, fmt.Sprintf("absent:%d", i)) {
			fp++
		}
	}
	require.Less(t, fp, trials/10, "false-positive rate should be well under 10%%, got %d/%d", fp, trials)
}

// TestBloomEmptyFilterNeverMatches confirms a page that stored nothing (empty or
// zero filter) reports no membership, so it is skipped.
func TestBloomEmptyFilterNeverMatches(t *testing.T) {
	require.False(t, bloomHas(nil, "x"))
	require.False(t, bloomHas(newBloom(10), "x"))
}

// TestBloomDeterministic confirms the same key sets the same bits across calls —
// required so a filter written by one process reads correctly in another.
func TestBloomDeterministic(t *testing.T) {
	a, b := newBloom(8), newBloom(8)
	bloomAdd(a, "stable-key")
	bloomAdd(b, "stable-key")
	require.Equal(t, a, b)
	h1a, h2a := bloomHashes("stable-key")
	h1b, h2b := bloomHashes("stable-key")
	require.Equal(t, h1a, h1b)
	require.Equal(t, h2a, h2b)
}

// TestBloomSizing pins the filter's byte length: bloomBitsPerKey bits per key,
// rounded up to whole bytes, and never empty, so an all-absent page still gets a
// well-formed one-byte filter that bloomAdd and bloomHas can index into.
func TestBloomSizing(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{name: "zero keys floors at one byte", n: 0, want: 1},
		{name: "one key rounds ten bits up to two bytes", n: 1, want: 2},
		{name: "four keys fill five bytes exactly", n: 4, want: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Len(t, newBloom(tt.n), tt.want)
		})
	}
}
