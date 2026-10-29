package redis

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/jqtruth"
	"github.com/zsltg/iq/internal/numfmt"
)

// decodeScanDocs hold fractional and out-of-range numbers, as RedisJSON returns them.
var decodeScanDocs = map[string]string{
	"frac": `{"price":1.5}`, "trail": `{"price":1.50}`, "half": `{"price":0.5}`,
	"int": `{"price":7}`, "big": `{"price":2e308}`, "neg": `{"price":-1e999}`,
	"text": `{"price":"1.5"}`,
}

var decodeScanFilters = []string{
	`.[] | select(.price == "1.5")`,
	`.[] | select(.price == "2e308")`,
	`.[] | select(.price > "1")`,
	`.[] | select(.price < "2")`,
	`.[] | select(.price > 1)`,
	`.[] | select(.price == 1.5)`,
}

// TestScanFilteredKeepsWhatJQKeeps runs each filter through ScanFiltered and through
// the full jq filter over a plain scan, with a fake server. The documents hold
// fractional and out-of-range numbers. Both paths return the same keys.
func TestScanFilteredKeepsWhatJQKeeps(t *testing.T) {
	modes := map[string]numfmt.DecimalMode{
		"auto": numfmt.DecimalAuto, "number": numfmt.DecimalNumber, "string": numfmt.DecimalString,
	}
	for name, mode := range modes {
		for _, filter := range decodeScanFilters {
			t.Run(name+"/"+filter, func(t *testing.T) {
				got := scanFilteredKeys(t, mode, filter)
				for k, raw := range decodeScanDocs {
					keeps, err := jqtruth.Keeps(filter, decodeJSONForTest(t, raw, mode))
					require.NoError(t, err)
					if keeps {
						require.Containsf(t, got, k, "ScanFiltered dropped %s, which jq keeps", k)
					}
				}
			})
		}
	}
}

// scanFilteredKeys runs ScanFiltered over a fake server that holds decodeScanDocs and
// returns the keys that it yields.
func scanFilteredKeys(t *testing.T, mode numfmt.DecimalMode, filter string) map[string]any {
	t.Helper()
	fake := &fakeRedis{keys: map[string]fakeKey{}}
	var names []string
	for k, doc := range decodeScanDocs {
		fake.keys[k] = fakeKey{typ: "ReJSON-RL", val: doc}
		names = append(names, k)
	}
	fake.scan = [][]string{names}
	st := newFakeStore(t, fake, 100)
	st.decimal = mode
	pred, err := jqtruth.Predicate(filter)
	require.NoError(t, err)

	got := map[string]any{}
	require.NoError(t, st.ScanFiltered(context.Background(), pred, func(b map[string]any) error {
		maps.Copy(got, b)
		return nil
	}))
	return got
}

func decodeJSONForTest(t *testing.T, s string, mode numfmt.DecimalMode) any {
	t.Helper()
	v, err := decodeJSON(s, mode)
	require.NoError(t, err)
	return v
}
