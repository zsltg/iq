package file

// The decode cache stores one Bloom filter per record page, so a bounded Get
// consults the filters and decodes only the pages that might hold a wanted key,
// instead of streaming the whole cache. A Bloom filter never yields a false
// negative — a page that holds a key always tests positive — so this never drops
// a record; a false positive only costs an extra page decode. The filter is a
// plain []byte bitset, built and queried with a deterministic hash so a filter
// written by one process reads correctly in another (hash/maphash's per-process
// seed would break that).

const (
	// bloomBitsPerKey sizes each page filter; ~10 bits/key gives roughly a 1%
	// false-positive rate at bloomHashCount hashes.
	bloomBitsPerKey = 10
	// bloomHashCount is the number of bit positions set per key (k), tuned for
	// bloomBitsPerKey.
	bloomHashCount = 7
)

// newBloom allocates a filter sized for n keys. It is never empty, so an
// all-absent page still has a well-formed (all-zero) filter.
func newBloom(n int) []byte {
	bits := n * bloomBitsPerKey
	if bits < 8 {
		bits = 8
	}
	return make([]byte, (bits+7)/8)
}

// bloomAdd records key in filter. filter must be non-empty; its bit length is
// len(filter)*8, so add and test agree by reading that length back off the bytes.
func bloomAdd(filter []byte, key string) {
	m := uint32(len(filter) * 8) //nolint:gosec // G115: a page's bloom filter is a few KB, so len*8 fits uint32.
	h1, h2 := bloomHashes(key)
	for i := uint32(0); i < bloomHashCount; i++ {
		pos := (h1 + i*h2) % m
		filter[pos/8] |= 1 << (pos % 8)
	}
}

// bloomHas reports whether key may be in filter: true if every bit is set (a
// possible member, subject to false positives), false if any is clear (a
// definite non-member). An empty filter (a page that stored nothing) never
// matches.
func bloomHas(filter []byte, key string) bool {
	if len(filter) == 0 {
		return false
	}
	m := uint32(len(filter) * 8) //nolint:gosec // G115: a page's bloom filter is a few KB, so len*8 fits uint32.
	h1, h2 := bloomHashes(key)
	for i := uint32(0); i < bloomHashCount; i++ {
		pos := (h1 + i*h2) % m
		if filter[pos/8]&(1<<(pos%8)) == 0 {
			return false
		}
	}
	return true
}

// bloomHashes derives two 32-bit hashes from one allocation-free FNV-1a pass over
// key, then combines them (Kirsch-Mitzenmacher) into the k positions. h2 is
// forced non-zero so the k positions do not collapse onto one bit.
func bloomHashes(key string) (uint32, uint32) {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= prime64
	}
	h1 := uint32(h) //nolint:gosec // G115: deliberate low-32-bit split of a 64-bit hash (Kirsch-Mitzenmacher).
	h2 := uint32(h >> 32)
	if h2 == 0 {
		h2 = 1
	}
	return h1, h2
}
