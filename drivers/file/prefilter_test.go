package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/rawpred"
)

// scanBatchesPages collects the sequence of {key: value} pages a plain scan emits.
func scanBatchesPages(t *testing.T, st *Store) []map[string]any {
	t.Helper()
	var pages []map[string]any
	require.NoError(t, st.ScanBatches(context.Background(), func(b map[string]any) error {
		pages = append(pages, b)
		return nil
	}))
	return pages
}

// scanFilteredPages collects the sequence of pages ScanFiltered emits for pred.
func scanFilteredPages(t *testing.T, st *Store, pred predicate.Node) []map[string]any {
	t.Helper()
	var pages []map[string]any
	require.NoError(t, st.ScanFiltered(context.Background(), pred, func(b map[string]any) error {
		pages = append(pages, b)
		return nil
	}))
	return pages
}

// matchEverything is a predicate rawpred can never prove CannotMatch: an empty And
// evaluates to a definite yes, so Match always returns MayMatch and no record is ever
// dropped. It drives the differential parity test — with nothing dropped, ScanFiltered's
// prefilter path must reproduce ScanBatches page-for-page.
var matchEverything = predicate.And{}

// TestPrefilterParity is the load-bearing contract: for every corpus dump, the
// prefilter path (ScanFiltered with a match-everything predicate) must produce byte-
// identical pages, and byte-identical errors, to the plain ScanBatches path. It proves
// the driver-owned raw reader and its decode duplicate the core JSONSource exactly.
func TestPrefilterParity(t *testing.T) {
	// bigInt is beyond int64, so convertNumbers must keep it a *big.Int, not a float.
	bigInt, _ := new(big.Int).SetString("123456789012345678901234567890", 10)

	typedLines := strings.Join([]string{
		`{"key":"a","type":"string","value":"x"}`,
		`{"key":"unicode","type":"string","value":"héllo \"q\" ☃ \n"}`,
		`{"key":"nested","type":"document","value":{"n":5,"arr":[1,2,3],"o":{"k":"v"}}}`,
		`{"key":"bignum","type":"document","value":{"big":123456789012345678901234567890}}`,
		`{"key":"exact","type":"document","value":{"pow":9007199254740992}}`, // 2^53.
		`{"key":"frac","type":"document","value":{"f":3.14,"e":1.5e3}}`,
		`{"key":"nullval","type":"document","value":null}`,
		`{"key":"scalar","type":"document","value":42}`,
		`{"key":"novalue","type":"document"}`, // value absent -> nil, not an error.
	}, "\n") + "\n"

	negZeroLine := `{"key":"nz","type":"document","value":{"x":1,"n":-0,"f":-0.0}}` + "\n"

	arrayForm := `[
		{"key":"a","type":"string","value":"x"},
		{"key":"b","type":"document","value":{"n":1}}
	]`

	blankLines := "\n\n" + `{"key":"a","type":"string","value":"x"}` + "\n\n\n" +
		`{"key":"b","type":"string","value":"y"}` + "\n\n"

	// ~20 KB value: well past bufio.Reader's 4 KiB default, so the raw reader must
	// refill its buffer several times mid-record — the same span-the-buffer code path a
	// larger value would take, without the memory churn of a huge literal under mutation.
	bigRecord := `{"key":"big","type":"string","value":"` + strings.Repeat("z", 20000) + `"}` + "\n"

	// More records than a page, so both scans flush a full page at the boundary and a
	// trailing partial page — the page partitioning must match exactly.
	var multi strings.Builder
	for i := range pageSize + 50 {
		fmt.Fprintf(&multi, `{"key":"m%d","type":"document","value":{"n":%d}}`+"\n", i, i)
	}

	tests := []struct {
		name  string
		build func(t *testing.T) *Store
	}{
		{"typed lines", func(t *testing.T) *Store { return jsonlStore(t, typedLines, CacheConfig{}) }},
		{"array form", func(t *testing.T) *Store { return jsonlStore(t, arrayForm, CacheConfig{}) }},
		{"blank lines", func(t *testing.T) *Store { return jsonlStore(t, blankLines, CacheConfig{}) }},
		{"negative zero", func(t *testing.T) *Store { return jsonlStore(t, negZeroLine, CacheConfig{}) }},
		{"large record", func(t *testing.T) *Store { return jsonlStore(t, bigRecord, CacheConfig{}) }},
		{"multi page", func(t *testing.T) *Store { return jsonlStore(t, multi.String(), CacheConfig{}) }},
		{"empty dump", func(t *testing.T) *Store { return jsonlStore(t, "", CacheConfig{}) }},
		{"stdin buffer", func(t *testing.T) *Store {
			st, err := OpenReader([]byte(typedLines), FormatJSONL, numfmt.DecimalAuto)
			require.NoError(t, err)
			return st
		}},
		{"gzip file", func(t *testing.T) *Store {
			u := writeDump(t, "dump.jsonl.gz", gzipBytes(t, []byte(typedLines)), "format=jsonl")
			st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			return st
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.build(t)
			require.True(t, st.prefilterable(), "corpus must exercise the prefilter path")
			want := scanBatchesPages(t, st)
			got := scanFilteredPages(t, st, matchEverything)
			require.Equal(t, want, got)
			// A spot check the exact-precision values survived intact through the raw path.
			all := flatten(got)
			if v, ok := all["bignum"]; ok {
				require.Equal(t, map[string]any{"big": bigInt}, v)
			}
		})
	}
}

// TestScanFilteredKeepsNegativeZero pins the sign of a negative zero on the prefiltered
// path. An equality check cannot tell -0 from 0 for a float, so the test reads the sign
// bit of each decoded value.
func TestScanFilteredKeepsNegativeZero(t *testing.T) {
	st := jsonlStore(t, `{"key":"nz","type":"document","value":{"x":1,"n":-0,"f":-0.0}}`+"\n", CacheConfig{})
	got := flatten(scanFilteredPages(t, st, matchEverything))
	val, ok := got["nz"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"n", "f"} {
		f, ok := val[field].(float64)
		require.True(t, ok, "%s must decode to a float64", field)
		require.True(t, math.Signbit(f), "%s must keep the sign of a negative zero", field)
	}
}

// TestPrefilterParityErrors pins that the prefilter path reports the same error, with
// the same message, as the plain scan on a malformed dump — the raw reader must never
// mask or reword a decode failure the plain scan would raise.
func TestPrefilterParityErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unterminated json", `{"key":"a","type":"","value":`},
		{"missing key", `{"type":"x","value":1}` + "\n"},
		{"malformed value object", `{"key":"a","type":"","value":{bad}}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := jsonlStore(t, tt.body, CacheConfig{})
			batchErr := st.ScanBatches(context.Background(), func(map[string]any) error { return nil })
			filterErr := st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error { return nil })
			require.Error(t, batchErr)
			require.Error(t, filterErr)
			require.Equal(t, batchErr.Error(), filterErr.Error())
		})
	}
}

// TestPrefilterEnvelopeFieldErrors pins the error of a record whose envelope field has
// the wrong JSON type. Both scan paths decode through the core decoder, so both give the
// same full text and the same cause fields.
func TestPrefilterEnvelopeFieldErrors(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantField string
		wantValue string
	}{
		{
			"numeric key", `{"key":7,"type":"document","value":{}}` + "\n",
			"expected a {key,type,value} record (use --key-field for foreign JSON): " +
				"json: cannot unmarshal number into Go struct field dumpRecord.key of type string",
			"key", "number",
		},
		{
			"numeric type", `{"key":"a","type":7,"value":{}}` + "\n",
			"expected a {key,type,value} record (use --key-field for foreign JSON): " +
				"json: cannot unmarshal number into Go struct field dumpRecord.type of type string",
			"type", "number",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := jsonlStore(t, tt.body, CacheConfig{})
			batchErr := st.ScanBatches(context.Background(), func(map[string]any) error { return nil })
			filterErr := st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error { return nil })
			for _, err := range []error{batchErr, filterErr} {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantText)
				var ute *json.UnmarshalTypeError
				require.ErrorAs(t, err, &ute)
				require.Equal(t, "dumpRecord", ute.Struct)
				require.Equal(t, tt.wantField, ute.Field)
				require.Equal(t, tt.wantValue, ute.Value)
			}
		})
	}
}

// TestPrefilterClasses pins the counters and the survivor set per predicate class: a
// class rawpred can decide drops the exact provable non-matches (checked equals the
// record count, skipped equals the dropped count, and the survivors are exactly the
// records the predicate keeps); an undecidable class keeps everything (skipped zero).
func TestPrefilterClasses(t *testing.T) {
	// Ten records k0..k9, each value {"n":i,"s":<even->"apple", odd->"banana">}.
	var lines []string
	for i := range 10 {
		s := "banana"
		if i%2 == 0 {
			s = "apple"
		}
		lines = append(lines, fmt.Sprintf(`{"key":"k%d","type":"document","value":{"n":%d,"s":%q}}`, i, i, s))
	}
	body := strings.Join(lines, "\n") + "\n"

	tests := []struct {
		name        string
		pred        predicate.Node
		wantChecked int
		wantSkipped int
		wantKeys    []string
	}{
		{
			name:        "eq drops all but the one match",
			pred:        predicate.Eq{Path: []string{"n"}, Value: 5.0},
			wantChecked: 10, wantSkipped: 9, wantKeys: []string{"k5"},
		},
		{
			name:        "cmp gt keeps the strictly greater",
			pred:        predicate.Cmp{Path: []string{"n"}, Op: predicate.Gt, Value: 5.0},
			wantChecked: 10, wantSkipped: 6, wantKeys: []string{"k6", "k7", "k8", "k9"},
		},
		{
			name:        "regex keeps present strings that match",
			pred:        predicate.Regex{Path: []string{"s"}, Pattern: "^apple$"},
			wantChecked: 10, wantSkipped: 5, wantKeys: []string{"k0", "k2", "k4", "k6", "k8"},
		},
		{
			name:        "exists keeps records with the field, drops the rest",
			pred:        predicate.Exists{Path: []string{"missing"}},
			wantChecked: 10, wantSkipped: 10, wantKeys: nil,
		},
		{
			// jq's length of a number is its absolute value, so Size decides on the
			// integral n field: only |2| == 2 survives.
			name:        "size decides on numbers",
			pred:        predicate.Size{Path: []string{"n"}, N: 2},
			wantChecked: 10, wantSkipped: 9,
			wantKeys: []string{"k2"},
		},
		{
			// An invalid pattern never compiles, so the matcher treats the node as
			// undecidable and keeps every record for the client-side jq.
			name:        "undecidable regex keeps everything",
			pred:        predicate.Regex{Path: []string{"s"}, Pattern: "("},
			wantChecked: 10, wantSkipped: 0,
			wantKeys: []string{"k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := jsonlStore(t, body, CacheConfig{})
			checkedBefore, skippedBefore := st.prefilterChecked, st.prefilterSkipped
			got := flatten(scanFilteredPages(t, st, tt.pred))
			require.Equal(t, tt.wantChecked, st.prefilterChecked-checkedBefore, "checked count")
			require.Equal(t, tt.wantSkipped, st.prefilterSkipped-skippedBefore, "skipped count")
			require.ElementsMatch(t, tt.wantKeys, keysOf(got), "survivor keys")
		})
	}
}

// TestPrefilterClassesMultiPage exercises the survivor-paging path under drops that
// span more than one page: with over pageSize records and roughly half provably
// dropped, the prefilter must still deliver exactly the surviving keys and count every
// record checked and every provable non-match skipped.
func TestPrefilterClassesMultiPage(t *testing.T) {
	const n = pageSize*2 + 5
	var b strings.Builder
	var wantKeys []string
	for i := range n {
		fmt.Fprintf(&b, `{"key":"k%d","type":"document","value":{"n":%d}}`+"\n", i, i)
		if i >= 100 { // Cmp keeps n >= 100 (Ge), drops the first 100.
			wantKeys = append(wantKeys, fmt.Sprintf("k%d", i))
		}
	}
	st := jsonlStore(t, b.String(), CacheConfig{})
	got := flatten(scanFilteredPages(t, st, predicate.Cmp{Path: []string{"n"}, Op: predicate.Ge, Value: 100.0}))
	require.Equal(t, n, st.prefilterChecked)
	require.Equal(t, 100, st.prefilterSkipped)
	require.ElementsMatch(t, wantKeys, keysOf(got))
}

// TestPrefilterFreshCacheFallback proves a fresh decode cache bypasses the prefilter:
// the first scan populates the cache, and the second ScanFiltered — now cache-fresh —
// takes the plain ScanBatches path (checked stays zero) yet still returns every record
// for the engine to re-filter.
func TestPrefilterFreshCacheFallback(t *testing.T) {
	dir := t.TempDir()
	body := `{"key":"a","type":"document","value":{"n":1}}` + "\n" +
		`{"key":"b","type":"document","value":{"n":2}}` + "\n"
	st := jsonlStore(t, body, cacheCfg(dir))
	require.True(t, st.prefilterable(), "no cache yet: prefilter applies")

	// Populate the cache with a full scan.
	require.NoError(t, st.ScanBatches(context.Background(), func(map[string]any) error { return nil }))
	require.NotEmpty(t, cacheFiles(t, dir), "the scan populated the cache")
	require.False(t, st.prefilterable(), "fresh cache: prefilter is bypassed")

	// A drop-everything predicate would empty a prefiltered scan, but the fallback must
	// still return both records (the engine re-filters), and never run the prefilter.
	checkedBefore := st.prefilterChecked
	got := flatten(scanFilteredPages(t, st, predicate.Eq{Path: []string{"n"}, Value: 999.0}))
	require.Equal(t, 0, st.prefilterChecked-checkedBefore, "the prefilter never runs on a cached scan")
	require.ElementsMatch(t, []string{"a", "b"}, keysOf(got))
}

// TestPrefilterNonJSONLFallback proves a non-JSONL format bypasses the prefilter: a
// YAML typed dump takes the plain scan path (checked stays zero) and returns every
// record for the engine to re-filter.
func TestPrefilterNonJSONLFallback(t *testing.T) {
	yamlBody := "key: a\ntype: document\nvalue:\n  n: 1\n---\nkey: b\ntype: document\nvalue:\n  n: 2\n"
	u := writeDump(t, "dump.yaml", []byte(yamlBody), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatYAML, st.format)
	require.False(t, st.prefilterable(), "YAML is not a prefilter target")

	checkedBefore := st.prefilterChecked
	got := flatten(scanFilteredPages(t, st, predicate.Eq{Path: []string{"n"}, Value: 999.0}))
	require.Equal(t, 0, st.prefilterChecked-checkedBefore, "the prefilter never runs on a non-JSONL format")
	require.ElementsMatch(t, []string{"a", "b"}, keysOf(got))
}

// TestPrefilterable pins the route decision across format, cache freshness, and source
// kind — the three inputs that decide whether the raw-byte prefilter engages.
func TestPrefilterable(t *testing.T) {
	dir := t.TempDir()

	t.Run("jsonl uncached engages", func(t *testing.T) {
		require.True(t, jsonlStore(t, oneRecord(), CacheConfig{}).prefilterable())
	})
	t.Run("jsonl stdin engages", func(t *testing.T) {
		st, err := OpenReader([]byte(oneRecord()), FormatJSONL, numfmt.DecimalAuto)
		require.NoError(t, err)
		require.True(t, st.prefilterable(), "stdin is uncacheable, so the prefilter engages")
	})
	t.Run("jsonl below cache floor engages", func(t *testing.T) {
		// A dump under the (huge) MinSize is not cacheable, so no cache can ever be fresh.
		st := jsonlStore(t, oneRecord(), CacheConfig{Dir: dir, Enabled: true, MinSize: 1 << 40})
		require.True(t, st.prefilterable())
	})
	t.Run("jsonl fresh cache bypasses", func(t *testing.T) {
		st := jsonlStore(t, oneRecord(), cacheCfg(dir))
		require.NoError(t, st.ScanBatches(context.Background(), func(map[string]any) error { return nil }))
		require.False(t, st.prefilterable())
	})
	for _, f := range []Format{FormatYAML, FormatBSON, FormatRDB, FormatMongoexport, FormatDynamoDBJSON, FormatCassandraCSV, FormatNeo4jJSON} {
		t.Run("non-jsonl bypasses "+f.String(), func(t *testing.T) {
			st := &Store{format: f, dec: numfmt.DecimalAuto}
			require.False(t, st.prefilterable())
		})
	}
}

// jsonlStore writes a typed-JSONL body to a temp file and opens it as a forced-JSONL
// Store with the given cache policy, so a test controls both the bytes and caching.
func jsonlStore(t *testing.T, body string, cache CacheConfig) *Store {
	t.Helper()
	u := writeDump(t, "dump.jsonl", []byte(body), "format=jsonl")
	st, err := Open(u, numfmt.DecimalAuto, cache)
	require.NoError(t, err)
	require.Equal(t, FormatJSONL, st.format)
	return st
}

// oneRecord is a minimal valid typed-JSONL body for route-decision tests.
func oneRecord() string { return `{"key":"a","type":"string","value":"x"}` + "\n" }

// flatten folds a sequence of pages into one map, so a test asserts over the whole
// scanned set regardless of page boundaries.
func flatten(pages []map[string]any) map[string]any {
	out := map[string]any{}
	for _, p := range pages {
		maps.Copy(out, p)
	}
	return out
}

// keysOf returns the keys of a value map, for order-independent survivor assertions.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// errReader is an io.Reader whose every Read fails with a fixed non-EOF error, so a
// test can drive scanFilteredJSON's prologue read failure directly — the one path the
// corpus-backed tests cannot reach, since a bytes reader only ever ends in io.EOF.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// TestScanFilteredReadError pins the prologue read-failure path: a reader that fails
// with a non-EOF error must surface as a "read json"-wrapped error before any record is
// evaluated. It kills the mutants that swallow the startsJSONArray error (the EOF-branch
// removal at the prologue) or drop it to nil (the startsJSONArray error return), both of
// which would re-route the failure into the decode loop and mislabel it "decode json
// record" instead.
func TestScanFilteredReadError(t *testing.T) {
	boom := errors.New("disk boom")
	var checked, skipped int
	err := scanFilteredJSON(
		context.Background(), errReader{err: boom}, rawpred.NewMatcher(matchEverything),
		&checked, &skipped, func(map[string]any) error { return nil },
	)
	require.ErrorContains(t, err, "read json")
	require.ErrorIs(t, err, boom)
	require.Equal(t, 0, checked, "no record is evaluated when the prologue read fails")
}

// TestScanFilteredEmptyInput pins that a truly empty stream returns nil with no page and
// nothing checked — the EOF branch of the prologue. It is the counterpart to the read-
// error test: together they pin both directions of the prologue's error handling.
func TestScanFilteredEmptyInput(t *testing.T) {
	var checked, skipped int
	var pages int
	err := scanFilteredJSON(
		context.Background(), strings.NewReader(""), rawpred.NewMatcher(matchEverything),
		&checked, &skipped, func(map[string]any) error { pages++; return nil },
	)
	require.NoError(t, err)
	require.Equal(t, 0, pages)
	require.Equal(t, 0, checked)
}

// TestScanFilteredLeadingWhitespaceArray drives the whitespace-skip loop of
// startsJSONArray with a single leading space before a top-level array. Detecting the
// array demands the loop discard exactly one byte and keep looping to the '['; a mutant
// that bails after the first space (negated Discard guard) or over-discards the '[' too
// (incremented Discard count) mis-reads the array as line mode and mangles the scan.
func TestScanFilteredLeadingWhitespaceArray(t *testing.T) {
	body := " [\n" +
		`{"key":"a","type":"string","value":"x"},` + "\n" +
		`{"key":"b","type":"document","value":{"n":1}}` + "\n]"
	st := jsonlStore(t, body, CacheConfig{})
	got := flatten(scanFilteredPages(t, st, matchEverything))
	require.ElementsMatch(t, []string{"a", "b"}, keysOf(got))
}

// TestScanFilteredSingleByteInput pins the Peek width in startsJSONArray: a one-byte,
// malformed stream must reach the decode loop and fail there ("decode json record"),
// exactly as the plain scan does. A mutant that peeks two bytes instead of one turns the
// short read into an io.EOF and silently returns nil — masking the malformed input.
func TestScanFilteredSingleByteInput(t *testing.T) {
	var checked, skipped int
	err := scanFilteredJSON(
		context.Background(), strings.NewReader("{"), rawpred.NewMatcher(matchEverything),
		&checked, &skipped, func(map[string]any) error { return nil },
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "decode json record")
}

// TestScanFilteredTrailingGarbage pins that a line-mode scan keeps decoding past every
// record and reports a trailing malformed byte as a decode error — it never treats the
// stream as an array and breaks early on the "no more elements" check. A mutant that
// drops the array guard on that check breaks out at the stray '}' and hides the error.
func TestScanFilteredTrailingGarbage(t *testing.T) {
	st := jsonlStore(t, `{"key":"a","type":"","value":1}}`+"\n", CacheConfig{})
	err := st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error { return nil })
	require.Error(t, err)
	require.ErrorContains(t, err, "decode json record")
}

// TestScanFilteredDecodeErrorWraps pins that a mid-stream decode failure is wrapped with
// %w, so the caller can still unwrap the underlying *json.SyntaxError. A mutant that
// downgrades the wrap to %v flattens the chain and defeats errors.As.
func TestScanFilteredDecodeErrorWraps(t *testing.T) {
	st := jsonlStore(t, `{"key":"a","type":"","value":1}`+"\n"+`@nope`+"\n", CacheConfig{})
	err := st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error { return nil })
	require.Error(t, err)
	var se *json.SyntaxError
	require.ErrorAs(t, err, &se, "the decode error must stay unwrappable to *json.SyntaxError")
}

// TestScanFilteredPageEmitError pins that a page-flush error inside the loop halts the
// scan at once: with more than pageSize survivors and a sink that fails on its first
// page, exactly one page must be emitted and its error returned. A mutant that ignores
// the intermediate emit's error keeps scanning and flushes a second page before failing.
func TestScanFilteredPageEmitError(t *testing.T) {
	var b strings.Builder
	for i := range pageSize + 10 {
		fmt.Fprintf(&b, `{"key":"k%d","type":"document","value":{"n":%d}}`+"\n", i, i)
	}
	st := jsonlStore(t, b.String(), CacheConfig{})
	boom := errors.New("sink boom")
	calls := 0
	err := st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error {
		calls++
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, calls, "the scan must stop at the first failing page flush")
}

// TestScanFilteredDropAllEmitsNoPage pins that a scan whose every record is provably
// dropped emits no page at all — the final flush of an empty page is a no-op, not an
// empty batch handed to the sink. A mutant that compares the page length against -1
// instead of 0 loses that guard and emits a spurious empty page.
func TestScanFilteredDropAllEmitsNoPage(t *testing.T) {
	var lines []string
	for i := range 5 {
		lines = append(lines, fmt.Sprintf(`{"key":"k%d","type":"document","value":{"n":%d}}`, i, i))
	}
	st := jsonlStore(t, strings.Join(lines, "\n")+"\n", CacheConfig{})
	calls := 0
	err := st.ScanFiltered(context.Background(), predicate.Eq{Path: []string{"n"}, Value: 999.0}, func(map[string]any) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, calls, "a drop-everything scan emits no page")
	require.Equal(t, 5, st.prefilterChecked)
	require.Equal(t, 5, st.prefilterSkipped)
}

// TestScanFilteredRepeatedValueEnvelope pins a typed record whose envelope repeats
// the value field. jsonparser reads the first value and encoding/json keeps the
// last, so the prefilter must not judge such a record: it goes to the full decode.
func TestScanFilteredRepeatedValueEnvelope(t *testing.T) {
	body := `{"key":"k","type":"document","value":{"a":1},"value":{"a":0}}` + "\n"
	st := jsonlStore(t, body, CacheConfig{})
	got := flatten(scanFilteredPages(t, st, predicate.Eq{Path: []string{"a"}, Value: 0.0}))
	require.Equal(t, []string{"k"}, keysOf(got))
}

// TestTypedValue pins which envelopes hand their value to the prefilter: exactly one
// value field in an object that parses to the end. A value followed by a broken
// entry, a repeated value, a missing value and a non-object all go to the full decode.
func TestTypedValue(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{"one value", `{"key":"k","value":{"a":1}}`, `{"a":1}`, true},
		{"repeated value", `{"value":1,"value":2}`, "", false},
		{"repeated value in another case", `{"value":{"a":1},"Value":{"a":0}}`, "", false},
		{"one value in another case", `{"key":"k","VALUE":2}`, "2", true},
		{"no value", `{"key":"k"}`, "", false},
		{"value then a broken entry", `{"value":1,"x":}`, "", false},
		{"not an object", `[{"value":1}]`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := typedValue([]byte(tt.raw))
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.Equal(t, tt.want, string(got))
			}
		})
	}
}

// TestScanFilteredStopsOnACancelledContext cancels the scan before it starts. The
// loop returns the context error and hands over no page.
func TestScanFilteredStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var checked, skipped int
	called := false
	err := scanFilteredJSON(
		ctx, strings.NewReader(oneRecord()), rawpred.NewMatcher(matchEverything),
		&checked, &skipped, func(map[string]any) error { called = true; return nil },
	)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
	require.Equal(t, 0, checked)
}

// dataThenErrReader gives its data in one Read, then fails every later Read with
// err.
type dataThenErrReader struct {
	data string
	err  error
}

func (r *dataThenErrReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// TestScanFilteredReportsAnEOFReadErrorInsideAnArray gives the prefilter a reader
// that fails inside a top-level array with an error that wraps io.EOF. An array
// must end with its closing bracket, so the scan reports the error. It must not
// end in silence and hand over a truncated array as the whole dump.
func TestScanFilteredReportsAnEOFReadErrorInsideAnArray(t *testing.T) {
	gone := fmt.Errorf("disk gone: %w", io.EOF)
	tests := []struct {
		name  string
		input string
	}{
		{name: "after a comma", input: `[{"key":"a","type":"string","value":"x"},`},
		{name: "after a record", input: `[{"key":"a","type":"string","value":"x"}`},
		{name: "after the opening bracket", input: `[`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var checked, skipped int
			err := scanFilteredJSON(
				context.Background(), &dataThenErrReader{data: tt.input, err: gone}, rawpred.NewMatcher(matchEverything),
				&checked, &skipped, func(map[string]any) error { return nil },
			)
			require.ErrorContains(t, err, "decode json record")
			require.ErrorIs(t, err, gone)
		})
	}
}

// TestScanFilteredReportsAMissingDump removes the dump after Open. The prefiltered
// scan reports that it cannot open the dump, and it hands over no page.
func TestScanFilteredReportsAMissingDump(t *testing.T) {
	st := jsonlStore(t, oneRecord(), CacheConfig{})
	require.NoError(t, os.Remove(st.path))
	called := false
	err := st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error { called = true; return nil })
	require.ErrorContains(t, err, "open dump")
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.False(t, called)
}

// TestScanFilteredEndConditionsUnderACanceledContext runs the prefiltered scan over a
// canceled context. An input with no value ends before the first context check, so it
// gives no error. An input with a value, or with only the brackets of an array,
// reaches the check and gives context.Canceled. A read error in the prologue wins
// over the context.
func TestScanFilteredEndConditionsUnderACanceledContext(t *testing.T) {
	boom := errors.New("disk gone")
	tests := []struct {
		name    string
		r       io.Reader
		wantErr error
	}{
		{name: "empty input", r: strings.NewReader("")},
		{name: "white space only", r: strings.NewReader(" \n\t\r")},
		{name: "an empty array", r: strings.NewReader("[]"), wantErr: context.Canceled},
		{name: "an empty array after white space", r: strings.NewReader(" \n[ ]"), wantErr: context.Canceled},
		{name: "an open bracket", r: strings.NewReader("["), wantErr: context.Canceled},
		{name: "one record", r: strings.NewReader(oneRecord()), wantErr: context.Canceled},
		{name: "a prologue read error", r: errReader{err: boom}, wantErr: boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var checked, skipped int
			called := false
			err := scanFilteredJSON(
				ctx, tt.r, rawpred.NewMatcher(matchEverything),
				&checked, &skipped, func(map[string]any) error { called = true; return nil },
			)
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
				if !errors.Is(tt.wantErr, context.Canceled) {
					require.NotErrorIs(t, err, context.Canceled)
					require.ErrorContains(t, err, "read json: ")
				}
			}
			require.False(t, called)
			require.Zero(t, checked)
		})
	}
}

// TestScanFilteredEndsAnArrayOnTheClosingBracket pins the two ways a stream ends. An
// array ends at its closing bracket, so a value after the bracket is never read. A
// stream of concatenated values ends at end of input.
func TestScanFilteredEndsAnArrayOnTheClosingBracket(t *testing.T) {
	var checked, skipped int
	var seen int
	fn := func(b map[string]any) error { seen += len(b); return nil }
	err := scanFilteredJSON(
		context.Background(), strings.NewReader("["+strings.TrimSpace(oneRecord())+"]\n{not json"),
		rawpred.NewMatcher(matchEverything), &checked, &skipped, fn,
	)
	require.NoError(t, err)
	require.Equal(t, 1, seen)

	err = scanFilteredJSON(
		context.Background(), strings.NewReader(oneRecord()+"{not json"),
		rawpred.NewMatcher(matchEverything), &checked, &skipped, fn,
	)
	require.ErrorContains(t, err, "decode json record: ")
}

// TestScanFilteredCountsARepeatedKeyTowardTheBatchSize reads a dump of one key repeated
// past a page. A page holds pageSize records, and a key that repeats inside a page
// folds into one map entry, so every batch holds a single entry.
func TestScanFilteredCountsARepeatedKeyTowardTheBatchSize(t *testing.T) {
	var b strings.Builder
	for range pageSize + 1 {
		b.WriteString(oneRecord())
	}
	st := jsonlStore(t, b.String(), CacheConfig{})
	pages := scanFilteredPages(t, st, matchEverything)
	require.Len(t, pages, 2)
	require.Len(t, pages[0], 1)
	require.Len(t, pages[1], 1)
	plain := scanBatchesPages(t, st)
	require.Equal(t, pages, plain)
}
