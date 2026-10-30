package mongo

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/jqtruth"
	"github.com/zsltg/iq/internal/numfmt"
)

var decodeFilters = []string{
	`.[] | select(.price == "9.99")`,
	`.[] | select(.price == "100")`,
	`.[] | select(.price == 9.5)`,
	`.[] | select(.price == "apple")`,
	`.[] | select(.price > 1000)`,
	`.[] | select(.price >= "9.99")`,
	`.[] | select(.price < 5)`,
	`.[] | select(.price <= 50)`,
	`.[] | select((.price | type) == "string" and (.price | test("^9")))`,
	`.[] | select(.price == "9.99" or .price == "apple")`,
}

// TestScanFilteredKeepsWhatJQKeeps stores a Decimal128 next to other types. For each
// decimal mode and each filter, the pushed scan returns every document that the full
// jq filter keeps. The 9.5 equality uses a value that a double holds exactly, because
// MongoDB does not equate a Decimal128 9.99 with the double 9.99. The server compares a Decimal128 as a number. jq compares the
// decoded string, so the filter must return the Decimal128 rows too.
func TestScanFilteredKeepsWhatJQKeeps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mongodb integration test in -short mode")
	}
	modes := map[string]numfmt.DecimalMode{
		"auto": numfmt.DecimalAuto, "number": numfmt.DecimalNumber, "string": numfmt.DecimalString,
	}
	for name, mode := range modes {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		store, err := Open(ctx, testURI(), "decode_docs", nil, mode)
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		seedDocs(t, store, []any{
			bson.M{"_id": "dec", "price": mustDecimal(t, "9.99")},
			bson.M{"_id": "dec_half", "price": mustDecimal(t, "9.5")},
			bson.M{"_id": "dec_int", "price": mustDecimal(t, "100")},
			bson.M{"_id": "dec_big", "price": mustDecimal(t, "5000.5")},
			bson.M{"_id": "double", "price": 9.99},
			bson.M{"_id": "int", "price": int32(7)},
			bson.M{"_id": "text", "price": "9.99"},
			bson.M{"_id": "word", "price": "apple"},
			bson.M{"_id": "missing"},
		})
		all := map[string]any{}
		require.NoError(t, store.ScanBatches(ctx, func(b map[string]any) error {
			maps.Copy(all, b)
			return nil
		}))
		require.Len(t, all, 9)

		for _, filter := range decodeFilters {
			t.Run(name+"/"+filter, func(t *testing.T) {
				pred, err := jqtruth.Predicate(filter)
				require.NoError(t, err)
				pushed := map[string]any{}
				require.NoError(t, store.ScanFiltered(ctx, pred, func(b map[string]any) error {
					maps.Copy(pushed, b)
					return nil
				}))
				for key, doc := range all {
					keeps, err := jqtruth.Keeps(filter, doc)
					require.NoError(t, err)
					require.Falsef(t, keeps && pushed[key] == nil,
						"the pushed scan dropped %s, which jq keeps", key)
				}
			})
		}
	}
}
