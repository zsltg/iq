package file

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hdt3213/rdb/encoder"
	"github.com/hdt3213/rdb/model"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// writeDump writes content to a temp file and returns its file:// URL, optionally
// with a ?format= override appended.
func writeDump(t *testing.T, name string, content []byte, query string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	u := "file://" + path
	if query != "" {
		u += "?" + query
	}
	return u
}

// collect scans a store fully and returns its records keyed by Key.
func collect(t *testing.T, st *Store) map[string]query.Record {
	t.Helper()
	out := map[string]query.Record{}
	require.NoError(t, st.TypedScan(context.Background(), func(batch []query.Record) error {
		for _, r := range batch {
			out[r.Key] = r
		}
		return nil
	}))
	return out
}

func buildRDB(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, 5, 0))
	require.NoError(t, enc.WriteStringObject("s", []byte("hello")))
	require.NoError(t, enc.WriteListObject("l", [][]byte{[]byte("a"), []byte("b")}))
	require.NoError(t, enc.WriteSetObject("st", [][]byte{[]byte("y"), []byte("x")}))
	require.NoError(t, enc.WriteHashMapObject("h", map[string][]byte{"f1": []byte("v1"), "f2": []byte("v2")}))
	require.NoError(t, enc.WriteZSetObject("z", []*model.ZSetEntry{{Member: "b", Score: 2}, {Member: "a", Score: 1}}))
	require.NoError(t, enc.WriteEnd())
	return buf.Bytes()
}

func TestRDBSourceParity(t *testing.T) {
	u := writeDump(t, "dump.rdb", buildRDB(t), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatRDB, st.format)

	recs := collect(t, st)
	require.Equal(t, query.Record{Key: "s", Type: "string", Value: "hello"}, recs["s"])
	require.Equal(t, query.Record{Key: "l", Type: "list", Value: []any{"a", "b"}}, recs["l"])
	// A set is sorted lexically, matching the live adapter's canonical encoding.
	require.Equal(t, query.Record{Key: "st", Type: "set", Value: []any{"x", "y"}}, recs["st"])
	require.Equal(t, query.Record{Key: "h", Type: "hash", Value: map[string]any{"f1": "v1", "f2": "v2"}}, recs["h"])
	// A sorted set is score-ascending, each element {member, score}.
	require.Equal(t, query.Record{Key: "z", Type: "zset", Value: []any{
		map[string]any{"member": "a", "score": float64(1)},
		map[string]any{"member": "b", "score": float64(2)},
	}}, recs["z"])
}

func TestStreamValue(t *testing.T) {
	obj := &model.StreamObject{
		Entries: []*model.StreamEntry{{
			Msgs: []*model.StreamMessage{
				{Id: &model.StreamId{Ms: 2, Sequence: 0}, Fields: map[string]string{"b": "2"}},
				{Id: &model.StreamId{Ms: 1, Sequence: 0}, Fields: map[string]string{"a": "1"}},
				{Id: &model.StreamId{Ms: 3, Sequence: 0}, Deleted: true},
			},
		}},
	}
	got := streamValue(obj)
	require.Equal(t, []any{
		map[string]any{"id": "1-0", "fields": map[string]any{"a": "1"}},
		map[string]any{"id": "2-0", "fields": map[string]any{"b": "2"}},
	}, got)
}

func TestBSONSource(t *testing.T) {
	d1, err := bson.Marshal(bson.M{"_id": "k1", "n": int32(5), "t": "x"})
	require.NoError(t, err)
	d2, err := bson.Marshal(bson.M{"_id": "k2", "arr": bson.A{int32(1), int32(2)}})
	require.NoError(t, err)
	u := writeDump(t, "dump.bson", append(d1, d2...), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatBSON, st.format)

	recs := collect(t, st)
	require.Equal(t, "document", recs["k1"].Type)
	require.Equal(t, map[string]any{"_id": "k1", "n": 5, "t": "x"}, recs["k1"].Value)
	require.Equal(t, map[string]any{"_id": "k2", "arr": []any{1, 2}}, recs["k2"].Value)
}

func TestExtJSONSource(t *testing.T) {
	lines := `{"_id":"k1","name":"a","n":5}
{"_id":{"$oid":"507f1f77bcf86cd799439011"},"name":"b"}
`
	u := writeDump(t, "export.json", []byte(lines), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatMongoexport, st.format)

	recs := collect(t, st)
	require.Equal(t, map[string]any{"_id": "k1", "name": "a", "n": 5}, recs["k1"].Value)
	require.Contains(t, recs, "507f1f77bcf86cd799439011")
}

func TestJSONLSourceTyped(t *testing.T) {
	lines := `{"key":"a","type":"string","value":"x"}
{"key":"b","type":"hash","value":{"f":"v"}}
`
	u := writeDump(t, "iq.jsonl", []byte(lines), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatJSONL, st.format)

	recs := collect(t, st)
	require.Equal(t, query.Record{Key: "a", Type: "string", Value: "x"}, recs["a"])
	require.Equal(t, "hash", recs["b"].Type)
}

func TestGetOmitsMissing(t *testing.T) {
	u := writeDump(t, "dump.rdb", buildRDB(t), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	got, err := st.Get(context.Background(), []string{"s", "absent"})
	require.NoError(t, err)
	require.Equal(t, "hello", got["s"])
	require.NotContains(t, got, "absent") // a missing key is absent, not a nil entry.
}

func TestFormatOverride(t *testing.T) {
	// A typed-jsonl body forced to be read as mongoexport still parses (each line is
	// a valid object); the override wins over detection.
	u := writeDump(t, "ambiguous", []byte(`{"_id":"x","v":1}`+"\n"), "format=mongoexport")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatMongoexport, st.format)
}

func TestParseFileURLErrors(t *testing.T) {
	_, _, _, err := parseFileURL("redis://h/0")
	require.ErrorContains(t, err, "not a file url")
	_, f, _, err := parseFileURL("file:///a/b.rdb?format=rdb")
	require.NoError(t, err)
	require.Equal(t, FormatRDB, f)
	_, _, _, err = parseFileURL("file:///a?format=bogus")
	require.ErrorContains(t, err, "unknown dump format")
}

func TestQueryUnsupported(t *testing.T) {
	st := &Store{path: "x", format: FormatJSONL}
	_, err := st.Query(context.Background(), []string{"INFO"})
	require.ErrorIs(t, err, ErrRawUnsupported)
}

func mustBSON(t *testing.T, m bson.M) []byte {
	t.Helper()
	b, err := bson.Marshal(m)
	require.NoError(t, err)
	return b
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		want    Format
	}{
		{"rdb magic", buildRDB(t), FormatRDB},
		{"typed jsonl envelope", []byte(`{"key":"a","type":"string","value":1}`), FormatJSONL},
		{"mongoexport object", []byte(`{"_id":"a","x":1}`), FormatMongoexport},
		{"mongoexport array", []byte(`  [{"_id":"a"}]`), FormatMongoexport},
		{"bson binary", mustBSON(t, bson.M{"_id": "a"}), FormatBSON},
		{"bson empty doc (min length)", mustBSON(t, bson.M{}), FormatBSON},
		// A header whose length field is exactly the 16 MiB cap is still BSON-shaped;
		// detection only peeks the header, so no full-size body is needed. This pins
		// the upper `size <= maxDoc` boundary.
		{"bson at max-length boundary", []byte{0x00, 0x00, 0x00, 0x01, 0x00}, FormatBSON},
		// iq's typed YAML is block-style text with a {key,…,value} envelope, so it is
		// content-sniffable without a .yaml extension; the document marker is optional.
		{"typed yaml envelope", []byte("key: a\ntype: string\nvalue: hi\n"), FormatYAML},
		{"typed yaml with document marker", []byte("---\nkey: a\ntype: string\nvalue: hi\n"), FormatYAML},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			require.NoError(t, os.WriteFile(p, c.content, 0o600))
			got, err := detectFormat(p)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
	t.Run("empty is an error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "empty")
		require.NoError(t, os.WriteFile(p, nil, 0o600))
		_, err := detectFormat(p)
		require.ErrorContains(t, err, "empty")
	})
	t.Run("undetectable is an error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "junk")
		require.NoError(t, os.WriteFile(p, []byte{0xff, 0xff, 0xff, 0xff, 0xff}, 0o600))
		_, err := detectFormat(p)
		require.ErrorContains(t, err, "cannot detect")
	})
	t.Run("whitespace only is undetectable", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "ws")
		require.NoError(t, os.WriteFile(p, []byte("   \n\t  "), 0o600))
		_, err := detectFormat(p)
		require.ErrorContains(t, err, "cannot detect")
	})
	t.Run("yaml mapping without the key+value envelope is undetectable", func(t *testing.T) {
		// A plain YAML mapping is not an iq dump; only the {key,…,value} envelope is.
		p := filepath.Join(t.TempDir(), "cfg")
		require.NoError(t, os.WriteFile(p, []byte("foo: bar\nbaz: qux\n"), 0o600))
		_, err := detectFormat(p)
		require.ErrorContains(t, err, "cannot detect")
	})
}

func TestFormatString(t *testing.T) {
	cases := []struct {
		f    Format
		want string
	}{
		{FormatJSONL, "jsonl"},
		{FormatYAML, "yaml"},
		{FormatMongoexport, "mongoexport"},
		{FormatBSON, "bson"},
		{FormatRDB, "rdb"},
		{FormatUnknown, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			require.Equal(t, c.want, c.f.String())
			if c.f == FormatUnknown {
				return
			}
			// A named format round-trips: its String is a synonym ParseFormat accepts.
			got, err := ParseFormat(c.want)
			require.NoError(t, err)
			require.Equal(t, c.f, got)
		})
	}
}

func TestDetectFormatURL(t *testing.T) {
	t.Run("sniffs content", func(t *testing.T) {
		f, err := DetectFormat(writeDump(t, "snap.rdb", buildRDB(t), ""))
		require.NoError(t, err)
		require.Equal(t, FormatRDB, f)
	})
	t.Run("forced format wins without sniffing", func(t *testing.T) {
		// A typed-jsonl body forced to yaml is returned as yaml, unread.
		u := writeDump(t, "amb", []byte(`{"key":"a","value":1}`+"\n"), "format=yaml")
		f, err := DetectFormat(u)
		require.NoError(t, err)
		require.Equal(t, FormatYAML, f)
	})
	t.Run("missing file errors", func(t *testing.T) {
		_, err := DetectFormat("file:///no/such/dump.rdb")
		require.Error(t, err)
	})
	t.Run("non-file url errors", func(t *testing.T) {
		_, err := DetectFormat("redis://h/0")
		require.ErrorContains(t, err, "not a file url")
	})
}

func TestFormatRaw(t *testing.T) {
	st := &Store{}
	require.Equal(t, `{"a":1}`, st.FormatRaw(map[string]any{"a": 1}, false))
	// An unmarshalable value falls back to %v rather than panicking.
	require.NotEmpty(t, st.FormatRaw(make(chan int), false))
}

func TestParseFileURLVariants(t *testing.T) {
	p, _, _, err := parseFileURL("file:///abs/x.rdb")
	require.NoError(t, err)
	require.Equal(t, "/abs/x.rdb", p)

	p, _, _, err = parseFileURL("file://localhost/abs/y")
	require.NoError(t, err)
	require.Equal(t, "/abs/y", p) // a localhost host is ignored, not folded into the path.

	p, _, _, err = parseFileURL("file://seg/rest")
	require.NoError(t, err)
	require.Equal(t, "seg/rest", p) // a non-localhost host folds back into the path.

	_, _, _, err = parseFileURL("file://")
	require.ErrorContains(t, err, "no path")
}

func TestExplainPlan(t *testing.T) {
	scan := ExplainPlan(selector.KeySet{Scan: true, Streamable: true}, nil, false)
	require.Equal(t, []string{"decode dump file", "scan client-side"}, scan.Ops)

	bounded := ExplainPlan(selector.KeySet{Keys: []string{"a", "b"}}, nil, false)
	require.Equal(t, "decode dump file", bounded.Ops[0])
	require.Contains(t, bounded.Ops[1], "2 key")

	// No keys and not a scan → the scan plan, not a "filter to 0 keys" plan.
	require.Equal(t, scan.Ops, ExplainPlan(selector.KeySet{}, nil, false).Ops)
}

func TestGetMultipleAndEarlyStop(t *testing.T) {
	u := writeDump(t, "dump.rdb", buildRDB(t), "")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	// All requested keys present: the scan stops as soon as both are found.
	got, err := st.Get(context.Background(), []string{"s", "h"})
	require.NoError(t, err)
	require.Equal(t, "hello", got["s"])
	require.Equal(t, map[string]any{"f1": "v1", "f2": "v2"}, got["h"])

	// An empty request is a no-op.
	empty, err := st.Get(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// bigCount exceeds the scan page size so the flush-at-page-boundary path runs
// with a trailing partial page — and the exact page sizes pin the >= boundary.
const bigCount = pageSize + 100

// pageSizes records the length of each batch a scan emits, so a test can assert
// the store flushes a full page at the size boundary and a trailing partial page.
func pageSizes(t *testing.T, st *Store) []int {
	t.Helper()
	var sizes []int
	require.NoError(t, st.TypedScan(context.Background(), func(b []query.Record) error {
		sizes = append(sizes, len(b))
		return nil
	}))
	return sizes
}

func TestPagingRDB(t *testing.T) {
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, bigCount, 0))
	for i := range bigCount {
		require.NoError(t, enc.WriteStringObject(fmt.Sprintf("k%d", i), []byte("v")))
	}
	require.NoError(t, enc.WriteEnd())
	st, err := Open(writeDump(t, "big.rdb", buf.Bytes(), ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, st))
}

func TestPagingBSON(t *testing.T) {
	var data []byte
	for i := range bigCount {
		data = append(data, mustBSON(t, bson.M{"_id": fmt.Sprintf("k%d", i)})...)
	}
	st, err := Open(writeDump(t, "big.bson", data, ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, st))
}

func TestPagingExtJSON(t *testing.T) {
	var b strings.Builder
	for i := range bigCount {
		fmt.Fprintf(&b, "{\"_id\":\"k%d\",\"i\":%d}\n", i, i)
	}
	st, err := Open(writeDump(t, "big.json", []byte(b.String()), "format=mongoexport"), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize, bigCount - pageSize}, pageSizes(t, st))
}

// exactPaging fills exactly one page so the final-flush guard must not emit a
// trailing empty batch (pins the len(page) > 0 boundary).
func TestPagingExactMultipleRDB(t *testing.T) {
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, pageSize, 0))
	for i := range pageSize {
		require.NoError(t, enc.WriteStringObject(fmt.Sprintf("k%d", i), []byte("v")))
	}
	require.NoError(t, enc.WriteEnd())
	st, err := Open(writeDump(t, "exact.rdb", buf.Bytes(), ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize}, pageSizes(t, st))
}

func TestPagingExactMultipleBSON(t *testing.T) {
	var data []byte
	for i := range pageSize {
		data = append(data, mustBSON(t, bson.M{"_id": fmt.Sprintf("k%d", i)})...)
	}
	st, err := Open(writeDump(t, "exact.bson", data, ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []int{pageSize}, pageSizes(t, st))
}

func TestBSONEmptyDocValid(t *testing.T) {
	// A minimal 5-byte empty document is valid (length == 5, the boundary), so it
	// must parse to one record rather than be rejected as too short.
	u := writeDump(t, "empty.bson", mustBSON(t, bson.M{}), "format=bson")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Len(t, collect(t, st), 1)
}

func TestExtJSONArray(t *testing.T) {
	// Leading whitespace exercises the whitespace-skip before the '['.
	u := writeDump(t, "arr.json", []byte("  \n[{\"_id\":\"a\",\"v\":1},{\"_id\":\"b\",\"v\":2}]"), "format=mongoexport")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	recs := collect(t, st)
	require.Len(t, recs, 2)
	require.Equal(t, map[string]any{"_id": "a", "v": 1}, recs["a"].Value)
}

func TestBSONInvalidLength(t *testing.T) {
	// A framed length of 4 is one below the empty-document minimum (5) — the
	// boundary case — and is a hard error, not a silent skip.
	u := writeDump(t, "bad.bson", []byte{0x04, 0x00, 0x00, 0x00}, "format=bson")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	err = st.TypedScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "invalid bson document length")
}

func TestZSetTiebreakByMember(t *testing.T) {
	var buf bytes.Buffer
	enc := encoder.NewEncoder(&buf)
	require.NoError(t, enc.WriteHeader())
	require.NoError(t, enc.WriteDBHeader(0, 1, 0))
	// Equal scores: order must fall back to the member, so "a" precedes "b".
	require.NoError(t, enc.WriteZSetObject("z", []*model.ZSetEntry{{Member: "b", Score: 1}, {Member: "a", Score: 1}}))
	require.NoError(t, enc.WriteEnd())
	st, err := Open(writeDump(t, "z.rdb", buf.Bytes(), ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, []any{
		map[string]any{"member": "a", "score": float64(1)},
		map[string]any{"member": "b", "score": float64(1)},
	}, collect(t, st)["z"].Value)
}

func TestStreamTiebreakBySequence(t *testing.T) {
	obj := &model.StreamObject{
		Entries: []*model.StreamEntry{{
			Msgs: []*model.StreamMessage{
				{Id: &model.StreamId{Ms: 5, Sequence: 2}, Fields: map[string]string{"b": "2"}},
				{Id: &model.StreamId{Ms: 5, Sequence: 1}, Fields: map[string]string{"a": "1"}},
			},
		}},
	}
	// Equal ms: order falls back to the sequence, so 5-1 precedes 5-2.
	got := streamValue(obj)
	require.Equal(t, "5-1", got[0].(map[string]any)["id"])
	require.Equal(t, "5-2", got[1].(map[string]any)["id"])
}

func TestDetectTypedArrayVsMongoArray(t *testing.T) {
	// An array of {key,type,value} records is iq's typed dump, not mongoexport docs.
	typed := writeDump(t, "typed.json", []byte(`[{"key":"a","type":"string","value":"x"}]`), "")
	st, err := Open(typed, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatJSONL, st.format)

	// An array of documents (no key/value envelope) is mongoexport.
	docs := writeDump(t, "docs.json", []byte(`[{"_id":"a","n":1}]`), "")
	st2, err := Open(docs, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatMongoexport, st2.format)
}

func TestYAMLFileByExtensionAndRoundTrip(t *testing.T) {
	body := "key: a\ntype: string\nvalue: hi\n---\nkey: b\ntype: set\nvalue:\n    - x\n    - y\n"
	// A .yaml extension forces YAML, covering a dump whose first record overflows the
	// content-sniff window; content detection is exercised separately below.
	st, err := Open(writeDump(t, "d.yaml", []byte(body), ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatYAML, st.format)
	recs := collect(t, st)
	require.Equal(t, query.Record{Key: "a", Type: "string", Value: "hi"}, recs["a"])
	require.Equal(t, "set", recs["b"].Type)
}

func TestYAMLContentDetectedWithoutExtension(t *testing.T) {
	// iq is YAML's only producer, so a typed dump is recognized by its {key,type,value}
	// envelope even without a .yaml/.yml name — and still decodes end to end.
	body := "key: a\ntype: string\nvalue: hi\n---\nkey: b\ntype: string\nvalue: yo\n"
	st, err := Open(writeDump(t, "dump.dat", []byte(body), ""), numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)
	require.Equal(t, FormatYAML, st.format)
	recs := collect(t, st)
	require.Equal(t, "hi", recs["a"].Value)
	require.Equal(t, "yo", recs["b"].Value)
}

func TestSupportedFormatsCatalogue(t *testing.T) {
	seen := map[Format]bool{}
	for _, fi := range SupportedFormats() {
		require.False(t, seen[fi.Format], "duplicate catalogue entry %q", fi.Name())
		seen[fi.Format] = true
		require.NotEqual(t, "unknown", fi.Name())
		require.NotEmpty(t, fi.Source)
		got, err := ParseFormat(fi.Name())
		require.NoError(t, err)
		require.Equal(t, fi.Format, got, "catalogue name %q must round-trip through ParseFormat", fi.Name())
	}
	// Every decodable format is catalogued, so a newly added reader cannot be silently
	// omitted from `iq driver ls -v`. The loop walks the contiguous Format constants
	// until String() falls through to "unknown".
	for f := FormatJSONL; f.String() != "unknown"; f++ {
		require.True(t, seen[f], "format %q missing from SupportedFormats()", f)
	}
}

func TestOpenReaderBufferedStdin(t *testing.T) {
	// A buffered (stdin-like) typed JSONL dump serves the store port and re-scans.
	data := []byte("{\"key\":\"a\",\"type\":\"string\",\"value\":\"hi\"}\n{\"key\":\"b\",\"type\":\"string\",\"value\":\"yo\"}\n")
	st, err := OpenReader(data, FormatUnknown, numfmt.DecimalAuto)
	require.NoError(t, err)
	require.Equal(t, FormatJSONL, st.format)
	got, err := st.Get(context.Background(), []string{"b"})
	require.NoError(t, err)
	require.Equal(t, "yo", got["b"])
	require.Len(t, collect(t, st), 2) // a second scan re-reads the buffer
}
