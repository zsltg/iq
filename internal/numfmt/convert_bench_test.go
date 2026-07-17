package numfmt_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"github.com/zsltg/iq/internal/numfmt"
)

// benchShape names a local document size for the conversion benchmark. numfmt is a
// separate package from query, so it cannot share the query corpus helper and
// keeps its own small generator here.
type benchShape struct {
	name string
	json []byte
}

// buildNumfmtCorpus builds three deterministic JSON blobs (flat, nested, array)
// dense in numbers, the input ConvertNumbers walks after a UseNumber decode.
func buildNumfmtCorpus() []benchShape {
	rng := rand.New(rand.NewSource(0xBEEF)) //nolint:gosec // deterministic corpus, not security.
	flat := map[string]any{}
	for i := 0; i < 12; i++ {
		flat[fmt.Sprintf("f%d", i)] = rng.Intn(1_000_000)
	}
	flat["ratio"] = rng.Float64()

	nested := map[string]any{"top": rng.Int63()}
	for i := 0; i < 6; i++ {
		sub := map[string]any{}
		for j := 0; j < 8; j++ {
			sub[fmt.Sprintf("n%d", j)] = rng.Float64() * 1000
		}
		nested[fmt.Sprintf("group%d", i)] = sub
	}

	nums := make([]any, 200)
	for i := range nums {
		if i%2 == 0 {
			nums[i] = rng.Intn(1_000_000)
		} else {
			nums[i] = rng.Float64() * 1000
		}
	}
	array := map[string]any{"series": nums, "count": len(nums)}

	return []benchShape{
		{name: "flat", json: mustMarshal(flat)},
		{name: "nested", json: mustMarshal(nested)},
		{name: "array", json: mustMarshal(array)},
	}
}

// mustMarshal marshals v or panics; benchmark setup, not production code.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// decodeUseNumber decodes raw with json.Number tokens preserved, the exact input
// ConvertNumbers is designed to normalize.
func decodeUseNumber(raw []byte) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		panic(err)
	}
	return v
}

// BenchmarkConvertNumbers measures numfmt.ConvertNumbers in isolation per shape.
// ConvertNumbers mutates its argument in place (json.Number becomes int/float), so
// a value cannot be converted twice. Rather than reset per iteration (which forces
// an expensive untimed decode into every step), it pre-decodes a fresh
// json.Number-laden value per iteration before the timer starts, leaving the timed
// loop to run only the conversion over never-before-converted inputs.
func BenchmarkConvertNumbers(b *testing.B) {
	for _, shape := range buildNumfmtCorpus() {
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(shape.json)))
			inputs := make([]any, b.N)
			for i := range inputs {
				inputs[i] = decodeUseNumber(shape.json)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				numfmt.ConvertNumbers(inputs[i], numfmt.DecimalAuto)
			}
		})
	}
}
