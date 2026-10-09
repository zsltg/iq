package redis_test

import (
	"context"
	"errors"
	"maps"
	"math"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/itchyny/gojq"
	"github.com/stretchr/testify/require"

	iqredis "github.com/zsltg/iq/drivers/redis"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
)

// seedFilterKeyspace flushes the reserved database and loads a mixed keyspace:
// several RedisJSON documents (the only type the prefilter inspects) alongside a
// plain string and a hash (which it must always pass through). It returns the
// RedisJSON and non-JSON key sets for the assertions.
func seedFilterKeyspace(t *testing.T, store *iqredis.Store) (jsonKeys, otherKeys []string) {
	t.Helper()
	flushKeyspace(t, store)

	// The s field spans the Regex prefilter's cases: doc1/doc3 are strings that
	// match ^h (case-insensitively), doc2 a string that does not, doc4 omits s
	// (missing), and doc5 carries a non-string s (jq test() would error, so it must
	// never be dropped). The items array spans the Size/ElemMatch/NoneMatch cases:
	// doc1 has an element q==1 (any matches), doc2 has none (any fails), and the rest
	// omit items entirely (a missing array — length 0, any over null errors).
	docs := map[string]string{
		"iq:test:sf:doc1": `{"author":{"name":"Rob"},"year":2015,"lang":"go","s":"hello","items":[{"q":1},{"q":2}]}`,
		"iq:test:sf:doc2": `{"author":{"name":"Ken"},"year":1978,"lang":"c","s":"World","items":[{"q":5}]}`,
		"iq:test:sf:doc3": `{"author":{"name":"Rob"},"year":1970,"lang":"b","s":"Hi there"}`,
		"iq:test:sf:doc4": `{"year":2000}`,
		"iq:test:sf:doc5": `{"author":{"name":"Rob"},"year":"recent","s":123}`,
	}
	for k, v := range docs {
		mustQuery(t, store, "JSON.SET", k, "$", v)
		jsonKeys = append(jsonKeys, k)
	}

	mustQuery(t, store, "SET", "iq:test:sf:str", "hello")
	mustQuery(t, store, "HSET", "iq:test:sf:hash", "f", "v")
	otherKeys = []string{"iq:test:sf:str", "iq:test:sf:hash"}
	return jsonKeys, otherKeys
}

// setupTimeout bounds each setup and cleanup call to the server.
const setupTimeout = 5 * time.Second

// mustQuery runs one command on store under its own deadline and fails the test
// on error.
func mustQuery(t *testing.T, store *iqredis.Store, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), setupTimeout)
	defer cancel()
	_, err := store.Query(ctx, args)
	require.NoError(t, err)
}

// flushKeyspace empties the reserved database now and again when the test ends.
// The cleanup uses a fresh context, because the context of the test is already
// cancelled by then, and it reports a failed flush.
func flushKeyspace(t *testing.T, store *iqredis.Store) {
	t.Helper()
	mustQuery(t, store, "FLUSHDB")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		defer cancel()
		if _, err := store.Query(ctx, []string{"FLUSHDB"}); err != nil {
			t.Errorf("flush the keyspace at cleanup: %v", err)
		}
	})
}

// collect walks a scan into a single {key: value} map, the whole (small) keyspace.
func collect(t *testing.T, scan func(context.Context, func(map[string]any) error) error) map[string]any {
	t.Helper()
	out := map[string]any{}
	err := scan(context.Background(), func(batch map[string]any) error {
		require.NotEmpty(t, batch, "a scan never yields an empty batch")
		maps.Copy(out, batch)
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestScanFilteredMatchesRefilteredScanBatches is the driver-level invariant: for
// several predicates, re-applying the full filter to ScanFiltered's output yields
// exactly the same set as re-applying it to a plain ScanBatches — the prefilter
// only ever drops documents the filter rejects. It also checks the prefilter never
// touches non-RedisJSON keys and never alters a surviving document's decode.
func TestScanFilteredMatchesRefilteredScanBatches(t *testing.T) {
	store := openIntegration(t)
	_, otherKeys := seedFilterKeyspace(t, store)

	baseline := collect(t, store.ScanBatches)

	tests := []struct {
		name string
		pred predicate.Node
	}{
		{"eq hit and miss on lang", predicate.Eq{Path: []string{"lang"}, Value: "go"}},
		{"cmp on year, absent and mistyped in some docs", predicate.Cmp{Path: []string{"year"}, Op: predicate.Ge, Value: 2000.0}},
		{"nested author name", predicate.Eq{Path: []string{"author", "name"}, Value: "Rob"}},
		{"exists lang, absent from some docs", predicate.Exists{Path: []string{"lang"}}},
		{"ne lang", predicate.Ne{Path: []string{"lang"}, Value: "go"}},
		// Regex over s: matching (doc1/doc3), non-matching (doc2, dropped),
		// non-string (doc5, kept), and missing (doc4, kept) all exercised at once.
		{"regex on s, mixed string/non-string/missing", predicate.Regex{Path: []string{"s"}, Pattern: "^h", Flags: "i"}},
		// Size over items: doc1 (length 2, kept), doc2 (length 1, dropped), and the
		// items-less docs (length 0, dropped) — a missing array is jq's null length 0.
		{"size on items array", predicate.Size{Path: []string{"items"}, N: 2}},
		// ElemMatch over items: doc1 has an element q==1 (kept), doc2's elements all
		// fail (dropped), and the items-less docs stay (any over a missing array errors).
		{"elemmatch q==1 over items", predicate.ElemMatch{Path: []string{"items"}, Cond: predicate.Eq{Path: []string{"q"}, Value: 1.0}}},
		// NoneMatch over items: doc1 has a matching element so it is dropped, doc2's
		// elements all fail so it stays, and the items-less docs stay.
		{"nonematch q==1 over items", predicate.NoneMatch{Path: []string{"items"}, Cond: predicate.Eq{Path: []string{"q"}, Value: 1.0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := collect(t, func(ctx context.Context, fn func(map[string]any) error) error {
				return store.ScanFiltered(ctx, tt.pred, fn)
			})

			// A prefiltered scan is a subset of the full scan, and every key it keeps
			// decodes identically to the full scan's.
			for k, v := range filtered {
				require.Contains(t, baseline, k)
				require.Equal(t, baseline[k], v, "prefilter must not change a surviving decode")
			}
			// Non-RedisJSON keys are always passed through untouched.
			for _, k := range otherKeys {
				require.Contains(t, filtered, k, "a non-JSON key is never prefiltered out")
			}
			// The load-bearing equivalence: the full filter over each scan's output
			// selects the same keys.
			require.Equal(t, matchingKeys(baseline, tt.pred), matchingKeys(filtered, tt.pred),
				"re-filtering ScanFiltered and ScanBatches must agree")
		})
	}
}

// TestScanFilteredHonorsDecimalMode pins that a surviving RedisJSON document is
// decoded under the store's decimal mode, not a default: under DecimalString a
// fractional number in a kept document must be its exact literal string.
func TestScanFilteredHonorsDecimalMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping redis integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	store, err := iqredis.Open(ctx, testURL(), nil, numfmt.DecimalString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	flushKeyspace(t, store)
	mustQuery(t, store, "JSON.SET", "iq:test:dec", "$", `{"a":1,"r":1.5}`)

	// a == 1 keeps the document, so it is decoded — under DecimalString, r is "1.5".
	pred := predicate.Eq{Path: []string{"a"}, Value: 1.0}
	got := collect(t, func(ctx context.Context, fn func(map[string]any) error) error {
		return store.ScanFiltered(ctx, pred, fn)
	})
	doc, ok := got["iq:test:dec"].(map[string]any)
	require.True(t, ok, "the surviving document is decoded to an object")
	require.Equal(t, "1.5", doc["r"], "fractional decoded under the store's decimal mode")
}

// TestScanFilteredRejectsUnsupportedType pins that a filtered scan fails fast on a
// type with no frozen JSON encoding, exactly as an unfiltered read does, rather
// than silently skipping it.
func TestScanFilteredRejectsUnsupportedType(t *testing.T) {
	store := openIntegration(t)
	ctx := context.Background()
	flushKeyspace(t, store)
	mustQuery(t, store, "TS.ADD", "iq:test:ts", "*", "1")

	err := store.ScanFiltered(ctx, predicate.Exists{Path: []string{"a"}}, func(map[string]any) error {
		return nil
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported redis type")
	// The read error wraps (%w) its cause rather than flattening it (%v), so the
	// original is recoverable from the chain.
	require.ErrorContains(t, errors.Unwrap(err), "unsupported redis type")
}

// matchingKeys returns the set of keys whose value satisfies pred, the stand-in for
// the engine's full-jq re-filter over a scan's output.
func matchingKeys(batch map[string]any, pred predicate.Node) map[string]bool {
	out := map[string]bool{}
	for k, v := range batch {
		if refMatchValue(v, pred) {
			out[k] = true
		}
	}
	return out
}

// refMatchValue evaluates pred over a decoded value with gojq.Compare as the oracle
// for jq's ordering, mirroring predicate semantics.
func refMatchValue(v any, pred predicate.Node) bool {
	switch n := pred.(type) {
	case predicate.Eq:
		return gojq.Compare(lookup(v, n.Path), n.Value) == 0
	case predicate.Ne:
		return gojq.Compare(lookup(v, n.Path), n.Value) != 0
	case predicate.Cmp:
		return refCmpHolds(gojq.Compare(lookup(v, n.Path), n.Value), n.Op)
	case predicate.Exists:
		return present(v, n.Path)
	case predicate.NotExists:
		return !present(v, n.Path)
	case predicate.Regex:
		return refRegexValue(lookup(v, n.Path), n)
	case predicate.Size:
		return refSizeValue(lookup(v, n.Path), n.N)
	case predicate.ElemMatch:
		// jq's any errors on a non-container or a non-indexable element ahead of a
		// match; such an error must not drop the document, so it counts as must-keep.
		match, errored := refAnyValue(lookup(v, n.Path), n.Cond)
		return errored || match
	case predicate.NoneMatch:
		match, errored := refAnyValue(lookup(v, n.Path), n.Cond)
		return errored || !match
	default:
		return false
	}
}

// refCmpHolds reports whether a gojq.Compare result c satisfies op.
func refCmpHolds(c int, op predicate.Op) bool {
	switch op {
	case predicate.Gt:
		return c > 0
	case predicate.Ge:
		return c >= 0
	case predicate.Lt:
		return c < 0
	default:
		return c <= 0
	}
}

// refRegexValue reports whether v is a string that matches the pattern of n.
func refRegexValue(v any, n predicate.Regex) bool {
	s, ok := v.(string)
	if !ok {
		// jq test() errors on a non-string; the oracle treats it as non-matching,
		// and rawpred keeps it (MayMatch), so it is never wrongly dropped.
		return false
	}
	pat := n.Pattern
	if strings.ContainsRune(n.Flags, 'i') {
		pat = "(?i)" + pat
	}
	if strings.ContainsRune(n.Flags, 'm') {
		pat = "(?s)" + pat
	}
	return regexp.MustCompile(pat).MatchString(s)
}

// refSizeValue mirrors jq's length compared to n over a decoded value: array
// elements, object keys, string runes, 0 for null, |value| for a number; a boolean
// (jq length errors) and any unexpected type are must-keep.
func refSizeValue(v any, n int) bool {
	switch t := v.(type) {
	case nil:
		return n == 0
	case string:
		return utf8.RuneCountInString(t) == n
	case []any:
		return len(t) == n
	case map[string]any:
		return len(t) == n
	case int, *big.Int, float64:
		return refAbsEquals(t, n)
	default:
		// A boolean (jq length errors) or an unexpected type.
		return true
	}
}

// refAbsEquals reports whether the absolute value of the number v equals n.
func refAbsEquals(v any, n int) bool {
	switch t := v.(type) {
	case int:
		if t < 0 {
			t = -t
		}
		return t == n
	case *big.Int:
		return new(big.Int).Abs(t).Cmp(big.NewInt(int64(n))) == 0
	case float64:
		return math.Abs(t) == float64(n)
	default:
		return true
	}
}

// refAnyValue mirrors jq's any(Cond), short-circuiting in element order: match=true
// at the first element satisfying Cond, errored=true on a non-container or a
// non-indexable element reached before any match (jq would error there).
func refAnyValue(v any, cond predicate.Node) (match, errored bool) {
	elems, ok := refElements(v)
	if !ok {
		return false, true
	}
	for _, e := range elems {
		if !refIndexable(e) {
			return false, true
		}
		if refMatchValue(e, cond) {
			return true, false
		}
	}
	return false, false
}

// refElements returns the elements of an array or the values of an object.
func refElements(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case map[string]any:
		var elems []any
		for _, e := range t {
			elems = append(elems, e)
		}
		return elems, true
	default:
		return nil, false
	}
}

// refIndexable reports whether jq can index e: null or an object.
func refIndexable(e any) bool {
	if e == nil {
		return true
	}
	_, ok := e.(map[string]any)
	return ok
}

// walk resolves path through nested objects and reports whether it exists.
func walk(v any, path []string) (any, bool) {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// lookup returns the value at path or nil (jq's null) when it is absent.
func lookup(v any, path []string) any {
	cur, _ := walk(v, path)
	return cur
}

// present reports whether path resolves to a value.
func present(v any, path []string) bool {
	_, ok := walk(v, path)
	return ok
}
