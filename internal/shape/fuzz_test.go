package shape_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/shape"
)

// maxFuzzInput bounds one fuzz input, and maxFuzzItems bounds how many items one
// input infers over. Inference walks every value, so an unbounded item set would
// spend the -fuzztime budget on one input.
const (
	maxFuzzInput = 64 << 10
	maxFuzzItems = 200
)

// FuzzInfer drives schema inference with an arbitrary batch of items, which is what
// a scan of a foreign keyspace hands it. Inference reads the values and never the
// keys, and a Go map hands them over in a random order, so the oracle is that the
// comparable projection does not depend on either: the same values under different
// names, visited in a different order, must give an identical projection.
func FuzzInfer(f *testing.F) {
	// Seeds cover the shape branches (scalars, objects, arrays, mixed types, an
	// absent field) plus hostile input: empty, deep nesting, invalid UTF-8 and a
	// number beyond float64.
	seeds := []string{
		`{"a":{"n":1,"s":"x"}}`,
		`{"a":{"n":1},"b":{"n":2,"opt":true}}`,
		`{"a":[1,2,3],"b":[]}`,
		`{"a":{"n":1},"b":{"n":"x"}}`,
		`{"a":null,"b":1,"c":"s","d":true}`,
		`{"a":{"t":"2020-01-02T03:04:05Z"},"b":{"t":"2021-01-02T03:04:05Z"}}`,
		`{"a":{"nested":{"deep":{"x":1}}}}`,
		`{}`,
		`[]`,
		`null`,
		`{"a":[[[[[[[[[[1]]]]]]]]]]}`,
		"{\"a\":\"\xff\xfe\"}",
		`{"a":100000000000000000001}`,
		`not json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			t.Skip("input over the fuzz size bound")
		}
		first, ok := fuzzItems(data)
		if !ok {
			return // not a JSON object of items.
		}
		second, ok := fuzzItems(data)
		require.True(t, ok, "the same bytes must decode twice")

		// The second batch carries the same values under generated names, and a map
		// hands them over in a random order, so this compares two independent walks.
		renamed := make(map[string]any, len(second))
		i := 0
		for _, v := range second {
			renamed["item"+strconv.Itoa(i)] = v
			i++
		}
		require.Equal(t, shape.Infer(first).Comparable(), shape.Infer(renamed).Comparable(),
			"inference depends on the item keys or on the order they arrive in")
	})
}

// fuzzItems decodes data as a batch of items, reporting false when it is not a JSON
// object or it holds more items than the fuzz bound allows.
func fuzzItems(data []byte) (map[string]any, bool) {
	var items map[string]any
	if err := json.Unmarshal(data, &items); err != nil || items == nil {
		return nil, false
	}
	if len(items) > maxFuzzItems {
		return nil, false
	}
	return items, true
}
