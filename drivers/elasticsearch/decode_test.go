package elasticsearch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/jqtruth"
	"github.com/zsltg/iq/internal/numfmt"
)

// TestPrefilterKeepsWhatJQKeeps runs each filter through the prefilter of a store and
// through the full jq filter, over documents with fractional and out-of-range numbers.
// The prefilter never drops a hit that jq keeps.
func TestPrefilterKeepsWhatJQKeeps(t *testing.T) {
	sources := map[string]string{
		"frac": `{"price":1.5}`, "trail": `{"price":1.50}`, "half": `{"price":0.5}`,
		"int": `{"price":7}`, "big": `{"price":2e308}`, "neg": `{"price":-1e999}`,
		"text": `{"price":"1.5"}`,
	}
	filters := []string{
		`.[] | select(.price == "1.5")`,
		`.[] | select(.price == "2e308")`,
		`.[] | select(.price > "1")`,
		`.[] | select(.price < "2")`,
		`.[] | select(.price > 1)`,
		`.[] | select(.price == 1.5)`,
	}
	modes := map[string]numfmt.DecimalMode{
		"auto": numfmt.DecimalAuto, "number": numfmt.DecimalNumber, "string": numfmt.DecimalString,
	}
	for name, mode := range modes {
		st := &Store{index: "books", decimal: mode}
		for _, filter := range filters {
			t.Run(name+"/"+filter, func(t *testing.T) {
				pred, err := jqtruth.Predicate(filter)
				require.NoError(t, err)
				matcher := st.prefilterMatcher(pred)
				require.NotNil(t, matcher)
				for key, src := range sources {
					doc, err := decodeSource(json.RawMessage(src), key, mode)
					require.NoError(t, err)
					keeps, err := jqtruth.Keeps(filter, doc)
					require.NoError(t, err)
					kept := st.keep(searchHit{ID: key, Source: json.RawMessage(src)}, matcher)
					require.Truef(t, kept || !keeps, "the prefilter dropped %s, which jq keeps", key)
				}
			})
		}
	}
}
