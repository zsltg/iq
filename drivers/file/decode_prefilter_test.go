package file

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/jqtruth"
)

// TestPrefilterKeepsOutOfRangeNumbers pins the prefilter against the full jq filter. The
// dump reader decodes a number that float64 cannot hold to its literal string. So a
// filter on that string must keep the record. The records hold fractional and
// out-of-range numbers.
func TestPrefilterKeepsOutOfRangeNumbers(t *testing.T) {
	values := map[string]string{
		"frac": `1.5`, "half": `0.5`, "int": `7`, "big": `2e308`, "neg": `-1e999`, "text": `"1.5"`,
	}
	var lines []string
	for k, v := range values {
		lines = append(lines, fmt.Sprintf(`{"key":%q,"type":"document","value":{"price":%s}}`, k, v))
	}
	st := jsonlStore(t, strings.Join(lines, "\n")+"\n", CacheConfig{})
	all := flatten(scanBatchesPages(t, st))
	require.Len(t, all, len(values))

	filters := []string{
		`.[] | select(.price == "2e308")`,
		`.[] | select(.price == "-1e999")`,
		`.[] | select(.price > "0")`,
		`.[] | select(.price >= "1.5")`,
		`.[] | select(.price < "2")`,
		`.[] | select(.price > 1)`,
		`.[] | select(.price < 1)`,
		`.[] | select(.price == 1.5)`,
		`.[] | select(.price == 7)`,
	}
	for _, filter := range filters {
		t.Run(filter, func(t *testing.T) {
			pred, err := jqtruth.Predicate(filter)
			require.NoError(t, err)
			got := flatten(scanFilteredPages(t, st, pred))
			for k, doc := range all {
				keeps, err := jqtruth.Keeps(filter, doc)
				require.NoError(t, err)
				if keeps {
					require.Containsf(t, got, k, "the prefilter dropped %s, which jq keeps", k)
				}
			}
		})
	}
}

func TestPrefilterStillDropsRecords(t *testing.T) {
	st := jsonlStore(t, `{"key":"a","type":"document","value":{"price":7}}`+"\n", CacheConfig{})
	pred, err := jqtruth.Predicate(`.[] | select(.price == "x")`)
	require.NoError(t, err)
	require.Empty(t, flatten(scanFilteredPages(t, st, pred)))
	require.Equal(t, 1, st.prefilterSkipped)
}
