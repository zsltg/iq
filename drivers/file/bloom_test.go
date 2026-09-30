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
		{name: "a hundred keys take ten bits each", n: 100, want: 125},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Len(t, newBloom(tt.n), tt.want)
		})
	}
}

// TestBloomHashesGolden pins bloomHashes to exact values. The filter is written to
// disk by one process and read by another, so the hash is part of the cache file's
// format: a changed FNV constant, a skipped input byte, or a moved 64-to-32 split
// silently relocates every bit and turns a warm index into a false-negative
// machine. Golden values are the only assertion that pins all three at once.
func TestBloomHashesGolden(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		h1, h2 uint32
	}{
		{name: "empty key is the bare offset basis", key: "", h1: 2216829733, h2: 3421674724},
		{name: "single byte mixes the first byte in", key: "a", h1: 2248273036, h2: 2942557260},
		{name: "two bytes mix both", key: "k1", h1: 3043108033, h2: 146673415},
		{name: "five bytes", key: "hello", h1: 2158673163, h2: 2754664518},
		{name: "non-utf8 bytes hash by byte", key: "\x00\xff", h1: 3035245408, h2: 137480455},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h1, h2 := bloomHashes(tt.key)
			require.Equal(t, tt.h1, h1)
			require.Equal(t, tt.h2, h2)
		})
	}
}

// TestBloomHashesForcesNonZeroSecondHash covers the one key class the golden table
// cannot reach by accident: a key whose top 32 hash bits are all zero. h2 is the
// stride between the k bit positions, so leaving it at zero would collapse every
// position onto h1%m and cost the filter its false-positive rate. "vzy0n2aa" is
// such a key (its raw h2 is 0), and bloomHashes must report 1 instead.
func TestBloomHashesForcesNonZeroSecondHash(t *testing.T) {
	const zeroH2Key = "vzy0n2aa"
	h1, h2 := bloomHashes(zeroH2Key)
	require.Equal(t, uint32(2092665342), h1)
	require.Equal(t, uint32(1), h2, "a zero stride must be forced to 1")

	// With the stride forced, the k positions spread; a collapsed stride would set a
	// single bit and make this filter accept far more than it should.
	f := newBloom(16)
	bloomAdd(f, zeroH2Key)
	set := 0
	for _, b := range f {
		for bit := range 8 {
			if b&(1<<bit) != 0 {
				set++
			}
		}
	}
	// The literal 7 pins bloomHashCount. The count is part of the cache file format,
	// so a filter written with another count gives false negatives on read.
	require.Equal(t, 7, set, "k distinct bits, so the stride did not collapse")
}
