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
	type scanCase struct {
		name   string
		mode   numfmt.DecimalMode
		filter string
	}
	var cases []scanCase
	for name, mode := range modes {
		for _, filter := range decodeScanFilters {
			cases = append(cases, scanCase{name + "/" + filter, mode, filter})
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scanFilteredKeys(t, c.mode, c.filter)
			requireKeepsWhatJQKeeps(t, got, c.mode, c.filter)
		})
	}
}

// requireKeepsWhatJQKeeps checks that got holds every document that the full jq
// filter keeps.
func requireKeepsWhatJQKeeps(t *testing.T, got map[string]any, mode numfmt.DecimalMode, filter string) {
	t.Helper()
	for k, raw := range decodeScanDocs {
		keeps, err := jqtruth.Keeps(filter, decodeJSONForTest(t, raw, mode))
		require.NoError(t, err)
		if keeps {
			require.Containsf(t, got, k, "ScanFiltered dropped %s, which jq keeps", k)
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
