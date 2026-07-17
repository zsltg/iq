package redis

import (
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/zsltg/iq/internal/numfmt"
)

// benchShape names a representative document size for the decode benchmark. The
// redis driver is a separate package from the query core, so this file carries its
// own small deterministic generator rather than sharing the query corpus helper.
type benchShape struct {
	name string
	json string
}

// benchAlphabet is the ASCII alphabet the string generator draws from so encoded
// sizes stay predictable.
const benchAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randStr returns a deterministic pseudo-random string of length n.
func randStr(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = benchAlphabet[rng.Intn(len(benchAlphabet))]
	}
	return string(b)
}

// buildDecodeCorpus builds three deterministic JSON documents (~300 B flat, ~3 KB
// nested, ~30 KB nested-with-arrays), the input decodeJSON parses. The seed is
// fixed so the corpus is repeatable across runs.
func buildDecodeCorpus() []benchShape {
	rng := rand.New(rand.NewSource(0xD00D)) //nolint:gosec // deterministic corpus, not security.
	flat := map[string]any{
		"id": rng.Intn(1_000_000), "status": "active", "name": randStr(rng, 12),
		"email": randStr(rng, 8) + "@example.com", "age": rng.Intn(90),
		"score": rng.Float64() * 100, "active": true,
		"created": "2026-07-01T00:00:00Z", "balance": rng.Int63n(1_000_000_000),
	}
	nested := map[string]any{}
	for k, v := range flat {
		nested[k] = v
	}
	nested["profile"] = map[string]any{
		"bio": randStr(rng, 40), "location": randStr(rng, 16),
		"website":  "https://" + randStr(rng, 10) + ".example.com",
		"settings": map[string]any{"theme": randStr(rng, 6), "notifications": true, "language": randStr(rng, 4), "timezone": randStr(rng, 8)},
	}
	nested["metrics"] = map[string]any{"visits": rng.Intn(100_000), "purchases": rng.Intn(500), "revenue": rng.Float64() * 10_000, "rating": rng.Float64() * 5}

	array := map[string]any{}
	for k, v := range nested {
		array[k] = v
	}
	events := make([]any, 80)
	for i := range events {
		events[i] = map[string]any{"ts": "2026-01-01T00:00:00Z", "kind": randStr(rng, 10), "amount": rng.Float64() * 1000, "count": rng.Intn(1000), "note": randStr(rng, 24)}
	}
	array["events"] = events

	return []benchShape{
		{name: "flat300B", json: mustJSON(flat)},
		{name: "nested3KB", json: mustJSON(nested)},
		{name: "array30KB", json: mustJSON(array)},
	}
}

// mustJSON marshals v to a JSON string or panics; benchmark setup, not production.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// BenchmarkDecodeJSON measures the redis driver's full-fidelity JSON decode path
// (decodeJSON: json.Decoder.UseNumber + numfmt.ConvertNumbers) per doc shape. It is
// the per-document normalization cost every scanned RedisJSON value pays, and a
// representative baseline for the driver decode a raw-byte pre-filter would skip
// for non-matching documents. decodeJSON is a pure function, so no container is
// needed.
func BenchmarkDecodeJSON(b *testing.B) {
	for _, shape := range buildDecodeCorpus() {
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(shape.json)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := decodeJSON(shape.json, numfmt.DecimalAuto); err != nil {
					b.Fatalf("decodeJSON: %v", err)
				}
			}
		})
	}
}
