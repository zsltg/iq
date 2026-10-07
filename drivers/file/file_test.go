package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zsltg/rdb/encoder"
	"github.com/zsltg/rdb/model"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// writeDump writes content to a temp file and returns its file:// URL, optionally
// with a ?format= override appended.
func writeDump(t *testing.T, name string, content []byte, query string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	u := URL(path)
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

// TestURLRoundTrip pins URL as the inverse of DumpPath: a Unix path takes the
// triple-slash form, a drive path the RFC 8089 form, and DumpPath folds each
// back to a native path (the drive form only on Windows, where it is native).
func TestURLRoundTrip(t *testing.T) {
	tests := []struct {
		name, path, url, back string
	}{
		{name: "absolute unix path", path: "/abs/x.rdb", url: "file:///abs/x.rdb", back: "/abs/x.rdb"},
		{name: "space is percent-escaped", path: "/a b/c.json", url: "file:///a%20b/c.json", back: "/a b/c.json"},
		{name: "drive path", path: "C:/dir/f.json", url: "file:///C:/dir/f.json", back: "/C:/dir/f.json"},
		{name: "lowercase drive path", path: "d:/f", url: "file:///d:/f", back: "/d:/f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := URL(tt.path)
			require.Equal(t, tt.url, u)
			back, err := DumpPath(u)
			require.NoError(t, err)
			want := tt.back
			if drive := strings.TrimPrefix(tt.back, "/"); runtime.GOOS == "windows" && isDrivePath(drive) {
				want = filepath.FromSlash(drive) // the drive form is native only on Windows.
			}
			require.Equal(t, want, back)
		})
	}
}

func TestIsDrivePath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"C:/x", true},
		{"c:", true},
		{"Z:\\x", true},
		{"a:", true},
		{"z:", true},
		{"A:", true},
		{"", false},
		{"C", false},
		{":", false},
		{"1:/x", false},
		{"@:", false},
		{"[:", false},
		{"`:", false},
		{"{:", false},
		{"Cx/", false},
		{"/C:/x", false},
		{"C/:", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, isDrivePath(tt.in))
		})
	}
}

func TestNativePath(t *testing.T) {
	tests := []struct {
		name, goos, in, want string
	}{
		{name: "windows drive form", goos: "windows", in: "/C:/dir/f.json", want: filepath.FromSlash("C:/dir/f.json")},
		{name: "windows lowercase drive", goos: "windows", in: "/d:/f", want: filepath.FromSlash("d:/f")},
		{name: "windows unix path untouched", goos: "windows", in: "/abs/x", want: "/abs/x"},
		{name: "windows bare drive without slash", goos: "windows", in: "C:/x", want: "C:/x"},
		{name: "windows lone slash", goos: "windows", in: "/", want: "/"},
		{name: "windows empty", goos: "windows", in: "", want: ""},
		{name: "windows slash then non-drive", goos: "windows", in: "/C/x", want: "/C/x"},
		{name: "windows drive after a non-slash", goos: "windows", in: "xC:/f", want: "xC:/f"},
		{name: "linux keeps the drive form", goos: "linux", in: "/C:/dir/f.json", want: "/C:/dir/f.json"},
		{name: "darwin keeps the drive form", goos: "darwin", in: "/C:/dir/f.json", want: "/C:/dir/f.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, nativePath(tt.goos, tt.in))
		})
	}
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
	// The literal 500 pins pageSize, the page that a scan holds in memory.
	require.Equal(t, []int{500, 100}, pageSizes(t, st))
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
	// Both boundaries of the frame are a hard error, not a silent skip. The reader
	// sizes its buffer from this length, so the upper bound is what stops a corrupt
	// or hostile header from asking for gigabytes.
	cases := []struct {
		name    string
		frame   []byte
		wantErr string
	}{
		// 4 is one below the empty-document minimum of 5.
		{"one below the minimum", []byte{0x04, 0x00, 0x00, 0x00}, "invalid bson document length"},
		// 16 MiB + 1 is one above BSON's document cap.
		{"one above the 16 MiB cap", []byte{0x01, 0x00, 0x00, 0x01}, "invalid bson document length"},
		// The largest value the 4-byte frame can carry, 4 GiB - 1.
		{"the largest length the frame can hold", []byte{0xff, 0xff, 0xff, 0xff}, "invalid bson document length"},
		// Exactly 16 MiB is the largest legal document, so the length itself passes
		// and the missing body is what fails. This pins the accepted side of the cap.
		{"exactly at the 16 MiB cap", []byte{0x00, 0x00, 0x00, 0x01}, "read bson document"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			u := writeDump(t, "bad.bson", tt.frame, "format=bson")
			st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
			require.NoError(t, err)
			err = st.TypedScan(context.Background(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
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

// TestOpenReaderCarriesTheDecimalMode confirms the buffered (stdin) store keeps the
// decimal mode it was opened with: dropping it would silently decode a piped dump
// under a different number policy than the same dump read from a path.
func TestOpenReaderCarriesTheDecimalMode(t *testing.T) {
	for _, dec := range []numfmt.DecimalMode{numfmt.DecimalAuto, numfmt.DecimalString} {
		t.Run(fmt.Sprintf("mode %d survives", dec), func(t *testing.T) {
			st, err := OpenReader([]byte(`{"key":"a","type":"string","value":"x"}`), FormatJSONL, dec)
			require.NoError(t, err)
			require.Equal(t, dec, st.dec)
		})
	}
}

// TestParseFileURLOpaqueForm covers the file:relative spelling, whose path url.Parse
// reports as the opaque part rather than as Path. Losing it turns a legal relative
// dump url into a "no path" rejection.
func TestParseFileURLOpaqueForm(t *testing.T) {
	p, _, _, err := parseFileURL("file:dump.rdb")
	require.NoError(t, err)
	require.Equal(t, "dump.rdb", p)

	p, forced, hints, err := parseFileURL("file:sub/d.csv?format=cassandra-csv&keys=id")
	require.NoError(t, err)
	require.Equal(t, "sub/d.csv", p)
	require.Equal(t, FormatCassandraCSV, forced)
	require.Equal(t, "id", hints.Keys)
}

// TestMaybeGunzip pins the two-byte gzip sniff. Both magic bytes must match before
// the stream is unwrapped — a dump whose first byte happens to be 0x1f is not gzip —
// and a stream too short to sniff passes through for the decoder to report.
func TestMaybeGunzip(t *testing.T) {
	gz := gzipBytes(t, []byte("payload"))
	tests := []struct {
		name    string
		in      []byte
		want    string
		wantErr string
	}{
		{name: "a gzip stream is unwrapped", in: gz, want: "payload"},
		{name: "plain json passes through", in: []byte(`{"a":1}`), want: `{"a":1}`},
		{name: "only the first magic byte matches", in: []byte{0x1f, 'x', 'y'}, want: "\x1fxy"},
		{name: "only the second magic byte matches", in: []byte{0x00, 0x8b, 'y'}, want: "\x00\x8by"},
		{name: "an empty stream passes through", in: nil, want: ""},
		{name: "a one-byte stream is too short to sniff", in: []byte{0x1f}, want: "\x1f"},
		{name: "a bare gzip magic is a broken gzip dump", in: []byte{0x1f, 0x8b}, wantErr: "open gzip dump"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := maybeGunzip(bytes.NewReader(tt.in))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				requireWrapped(t, err)
				require.Nil(t, r)
				return
			}
			require.NoError(t, err)
			got, err := io.ReadAll(r)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
	t.Run("a read failure is reported", func(t *testing.T) {
		boom := errors.New("disk gone")
		_, err := maybeGunzip(errReader{err: boom})
		require.ErrorContains(t, err, "read dump head")
		require.ErrorIs(t, err, boom)
	})
}

// TestGetWithNoKeysNeverTouchesTheDump proves the empty-request fast path is a real
// short circuit and not merely a scan that happens to find nothing: the store points
// at a path that does not exist, so any decode would fail.
func TestGetWithNoKeysNeverTouchesTheDump(t *testing.T) {
	st := &Store{path: filepath.Join(t.TempDir(), "absent.json"), format: FormatJSONL}
	got, err := st.Get(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)

	_, err = st.Get(context.Background(), []string{"a"})
	require.ErrorContains(t, err, "open dump", "a non-empty request really does open the dump")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// TestGetStopsOnceEveryKeyIsFound pins the early stop. The dump holds a full page of
// good records followed by a corrupt one, so a scan that runs past the point where
// every requested key is found fails — the only way to observe a stop that is
// otherwise invisible in the result.
func TestGetStopsOnceEveryKeyIsFound(t *testing.T) {
	var b strings.Builder
	for i := range pageSize {
		fmt.Fprintf(&b, "{\"key\":\"k%d\",\"type\":\"string\",\"value\":\"v%d\"}\n", i, i)
	}
	b.WriteString("{not json at all\n")
	u := writeDump(t, "dump.json", []byte(b.String()), "format=jsonl")
	st, err := Open(u, numfmt.DecimalAuto, CacheConfig{})
	require.NoError(t, err)

	got, err := st.Get(context.Background(), []string{"k0", "k499"})
	require.NoError(t, err, "the scan stopped before reaching the corrupt record")
	require.Equal(t, map[string]any{"k0": "v0", "k499": "v499"}, got)

	_, err = st.Get(context.Background(), []string{"k0", "missing"})
	require.Error(t, err, "a key that is never found runs the scan into the corrupt record")
}

// TestExplainPlanRouting pins which plan each request shape produces: only a bounded
// (non-scan) request with at least one key reports the key-filter plan, a scan reports
// the scan plan even when keys are present, and a predicate adds the prefilter line.
func TestExplainPlanRouting(t *testing.T) {
	tests := []struct {
		name string
		keys selector.KeySet
		pred predicate.Node
		want []string
	}{
		{
			name: "one key is a bounded filter",
			keys: selector.KeySet{Keys: []string{"a"}},
			want: []string{"decode dump file", "filter to 1 key(s) client-side"},
		},
		{
			name: "a scan with keys is still a scan",
			keys: selector.KeySet{Scan: true, Keys: []string{"a"}},
			want: []string{"decode dump file", "scan client-side"},
		},
		{
			name: "no keys and no scan is a scan",
			keys: selector.KeySet{},
			want: []string{"decode dump file", "scan client-side"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ExplainPlan(tt.keys, tt.pred, false).Ops)
		})
	}
	t.Run("a predicate adds the prefilter line", func(t *testing.T) {
		ops := ExplainPlan(selector.KeySet{Scan: true}, predicate.And{}, false).Ops
		require.Len(t, ops, 3)
		require.Contains(t, ops[1], "rawpred")
	})
}

// TestZSetValueOrdering pins the sorted-set rendering: score ascending, ties broken
// lexically by member. Three tied members in an order that is neither sorted nor its
// own reverse catch a comparator that stops distinguishing equal scores.
func TestZSetValueOrdering(t *testing.T) {
	tests := []struct {
		name    string
		entries []*model.ZSetEntry
		want    []any
	}{
		{
			name:    "scores order ascending",
			entries: []*model.ZSetEntry{{Member: "b", Score: 2}, {Member: "a", Score: 1}},
			want: []any{
				map[string]any{"member": "a", "score": float64(1)},
				map[string]any{"member": "b", "score": float64(2)},
			},
		},
		{
			name:    "equal scores break lexically by member",
			entries: []*model.ZSetEntry{{Member: "a", Score: 1}, {Member: "c", Score: 1}, {Member: "b", Score: 1}},
			want: []any{
				map[string]any{"member": "a", "score": float64(1)},
				map[string]any{"member": "b", "score": float64(1)},
				map[string]any{"member": "c", "score": float64(1)},
			},
		},
		{
			name:    "a lower score outranks a lexically earlier member",
			entries: []*model.ZSetEntry{{Member: "a", Score: 9}, {Member: "z", Score: 1}},
			want: []any{
				map[string]any{"member": "z", "score": float64(1)},
				map[string]any{"member": "a", "score": float64(9)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, zsetValue(&model.ZSetObject{Entries: tt.entries}))
		})
	}
}

// TestStreamValueOrdering pins the stream rendering: id order (milliseconds, then
// sequence), tombstoned and id-less messages skipped, and equal ids left in the order
// the dump recorded them.
func TestStreamValueOrdering(t *testing.T) {
	tests := []struct {
		name string
		msgs []*model.StreamMessage
		want []any
	}{
		{
			name: "equal milliseconds order by sequence",
			msgs: []*model.StreamMessage{
				{Id: &model.StreamId{Ms: 5, Sequence: 1}, Fields: map[string]string{"a": "1"}},
				{Id: &model.StreamId{Ms: 5, Sequence: 3}, Fields: map[string]string{"c": "3"}},
				{Id: &model.StreamId{Ms: 5, Sequence: 2}, Fields: map[string]string{"b": "2"}},
			},
			want: []any{
				map[string]any{"id": "5-1", "fields": map[string]any{"a": "1"}},
				map[string]any{"id": "5-2", "fields": map[string]any{"b": "2"}},
				map[string]any{"id": "5-3", "fields": map[string]any{"c": "3"}},
			},
		},
		{
			name: "a duplicate id keeps the recorded order",
			msgs: []*model.StreamMessage{
				{Id: &model.StreamId{Ms: 1, Sequence: 1}, Fields: map[string]string{"first": "1"}},
				{Id: &model.StreamId{Ms: 1, Sequence: 1}, Fields: map[string]string{"second": "2"}},
			},
			want: []any{
				map[string]any{"id": "1-1", "fields": map[string]any{"first": "1"}},
				map[string]any{"id": "1-1", "fields": map[string]any{"second": "2"}},
			},
		},
		{
			name: "a message with no id carries no record",
			msgs: []*model.StreamMessage{
				{Fields: map[string]string{"orphan": "x"}},
				{Id: &model.StreamId{Ms: 1, Sequence: 0}, Fields: map[string]string{"a": "1"}},
			},
			want: []any{map[string]any{"id": "1-0", "fields": map[string]any{"a": "1"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := streamValue(&model.StreamObject{Entries: []*model.StreamEntry{{Msgs: tt.msgs}}})
			require.Equal(t, tt.want, got)
		})
	}
}

// TestRDBSourceFailures pins the two ways an RDB read stops: a cancelled scan
// reports the cancellation, and an undecodable snapshot reports the parse failure.
// Both are carried out of the parser's callback, so neither may be dropped.
func TestRDBSourceFailures(t *testing.T) {
	good := buildRDB(t)
	t.Run("a cancelled scan reports the cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := rdbSource(bytes.NewReader(good), pageSize)(ctx, func([]query.Record) error { return nil })
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("a consumer error stops the scan", func(t *testing.T) {
		boom := errors.New("consumer said no")
		// The consumer refuses only its first page, so a reader that dropped the
		// error and let the trailing flush run would report success.
		f := &failFirstCall{err: boom}
		err := rdbSource(bytes.NewReader(good), 1)(context.Background(), f.accept)
		require.ErrorIs(t, err, boom)
	})
	t.Run("an undecodable snapshot is a parse error", func(t *testing.T) {
		err := rdbSource(bytes.NewReader(corruptBody(good)), pageSize)(context.Background(), func([]query.Record) error { return nil })
		require.ErrorContains(t, err, "parse rdb")
		requireWrapped(t, err)
	})
}

// failFirstCall is a page consumer that refuses only its first page, so a test can
// tell a source that propagates a consumer error from one that swallows it and
// happens to succeed on a later flush.
type failFirstCall struct {
	calls int
	err   error
}

func (f *failFirstCall) accept([]query.Record) error {
	f.calls++
	if f.calls == 1 {
		return f.err
	}
	return nil
}

// TestBSONSourceFailures pins the framed reader's two failure points: a document
// whose declared length outruns the file is a read failure, and a well-framed but
// undecodable document is a decode failure. Each keeps its own message, so a
// truncated dump is never reported as a corrupt one.
func TestBSONSourceFailures(t *testing.T) {
	drain := func(r io.Reader) error {
		return bsonSource(r, pageSize, numfmt.DecimalAuto)(context.Background(), func([]query.Record) error { return nil })
	}
	t.Run("a truncated document is a read failure", func(t *testing.T) {
		err := drain(bytes.NewReader([]byte{20, 0, 0, 0, 1, 2, 3}))
		require.ErrorContains(t, err, "read bson document")
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
	t.Run("a cancelled scan stops", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		d := mustBSON(t, bson.M{"_id": "k1"})
		called := false
		err := bsonSource(bytes.NewReader(d), pageSize, numfmt.DecimalAuto)(ctx, func([]query.Record) error { called = true; return nil })
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, called)
	})
	t.Run("an undecodable document is a decode failure", func(t *testing.T) {
		err := drain(bytes.NewReader([]byte{6, 0, 0, 0, 0xff, 0x00}))
		require.ErrorContains(t, err, "decode bson document")
		requireWrapped(t, err)
	})
	t.Run("a truncated length frame is a length read failure", func(t *testing.T) {
		err := drain(bytes.NewReader([]byte{20, 0}))
		require.ErrorContains(t, err, "read bson length")
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
	t.Run("a consumer error propagates", func(t *testing.T) {
		d, err := bson.Marshal(bson.M{"_id": "k1"})
		require.NoError(t, err)
		boom := errors.New("consumer said no")
		f := &failFirstCall{err: boom}
		require.ErrorIs(t, bsonSource(bytes.NewReader(append(d, d...)), 1, numfmt.DecimalAuto)(context.Background(), f.accept), boom)
	})
}

// TestExtJSONSourceFailures pins the mongoexport reader's error handling. The
// line-oriented form must report a malformed record rather than stopping quietly at
// it, an unexpected closing bracket is malformed input and not an end of stream, an
// undecodable document is a decode failure, and a consumer error propagates even
// when a later page would have succeeded.
func TestExtJSONSourceFailures(t *testing.T) {
	drain := func(body string, size int, fn func([]query.Record) error) error {
		return extJSONSource(strings.NewReader(body), size, numfmt.DecimalAuto)(context.Background(), fn)
	}
	ignore := func([]query.Record) error { return nil }

	t.Run("a malformed line is a decode failure", func(t *testing.T) {
		err := drain(`{"_id":1}`+"\n{oops\n", pageSize, ignore)
		require.ErrorContains(t, err, "decode mongoexport json")
		requireWrapped(t, err)
	})
	t.Run("a stray closing bracket is a decode failure", func(t *testing.T) {
		require.ErrorContains(t, drain(`{"_id":1}`+"\n]\n", pageSize, ignore), "decode mongoexport json")
	})
	t.Run("an undecodable extended json document is refused", func(t *testing.T) {
		err := drain(`{"_id":{"$oid":"not-hex"}}`, pageSize, ignore)
		require.ErrorContains(t, err, "decode extended json document")
		requireWrapped(t, err)
	})
	t.Run("a truncated array is a decode failure", func(t *testing.T) {
		require.ErrorContains(t, drain(`[{"_id":1},`, pageSize, ignore), "decode mongoexport json")
	})
	t.Run("a consumer error propagates from a full page", func(t *testing.T) {
		boom := errors.New("consumer said no")
		f := &failFirstCall{err: boom}
		require.ErrorIs(t, drain(`{"_id":1}`+"\n"+`{"_id":2}`+"\n"+`{"_id":3}`+"\n", 2, f.accept), boom)
	})
	t.Run("an empty dump yields nothing", func(t *testing.T) {
		require.NoError(t, drain("", pageSize, ignore))
	})
	t.Run("a cancelled scan stops", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := extJSONSource(strings.NewReader(`{"_id":1}`), pageSize, numfmt.DecimalAuto)(ctx, func([]query.Record) error { called = true; return nil })
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, called)
	})
	t.Run("an empty array hands over no page", func(t *testing.T) {
		called := false
		require.NoError(t, drain("[]", pageSize, func([]query.Record) error { called = true; return nil }))
		require.False(t, called)
	})
	t.Run("a one-byte malformed dump is a decode failure", func(t *testing.T) {
		require.ErrorContains(t, drain("x", pageSize, ignore), "decode mongoexport json")
	})
	t.Run("a prologue read failure is reported as such", func(t *testing.T) {
		boom := errors.New("disk gone")
		err := extJSONSource(errReader{err: boom}, pageSize, numfmt.DecimalAuto)(context.Background(), ignore)
		require.ErrorContains(t, err, "read mongoexport json")
		require.ErrorIs(t, err, boom)
	})
}

// TestExtJSONIsCanonical pins the extended-JSON dialect the reader accepts. It
// decodes in canonical mode, so a canonical $date (a $numberLong wrapper) is read
// and the relaxed spelling (an ISO-8601 string) is refused rather than silently
// read under a different dialect than a live scan uses.
func TestExtJSONIsCanonical(t *testing.T) {
	recs := map[string]query.Record{}
	collectInto := func(batch []query.Record) error {
		for _, r := range batch {
			recs[r.Key] = r
		}
		return nil
	}
	body := `{"_id":"a","when":{"$date":{"$numberLong":"1577836800000"}}}` + "\n"
	require.NoError(t, extJSONSource(strings.NewReader(body), pageSize, numfmt.DecimalAuto)(context.Background(), collectInto))
	require.Contains(t, recs, "a")

	relaxed := `{"_id":"a","when":{"$date":"2020-01-01T00:00:00Z"}}` + "\n"
	err := extJSONSource(strings.NewReader(relaxed), pageSize, numfmt.DecimalAuto)(context.Background(), collectInto)
	require.ErrorContains(t, err, "decode extended json document")
}

// TestURLFailures pins the error that each entry point gives for a URL it cannot
// use. The error names the real cause, not a later failure on an empty path.
func TestURLFailures(t *testing.T) {
	t.Run("Open refuses a non-file url", func(t *testing.T) {
		st, err := Open("redis://h/0", numfmt.DecimalAuto, CacheConfig{})
		require.ErrorContains(t, err, "not a file url")
		require.Nil(t, st)
	})
	t.Run("Open refuses a dump whose format it cannot detect", func(t *testing.T) {
		st, err := Open(writeDump(t, "notes.txt", []byte("hello there\n"), ""), numfmt.DecimalAuto, CacheConfig{})
		require.ErrorContains(t, err, "cannot detect dump format")
		require.Nil(t, st)
	})
	t.Run("DumpPath refuses a non-file url", func(t *testing.T) {
		_, err := DumpPath("redis://h/0")
		require.ErrorContains(t, err, "not a file url")
	})
	t.Run("an unparsable url is a parse error", func(t *testing.T) {
		_, _, _, err := parseFileURL("file://%zz/x")
		require.ErrorContains(t, err, "parse file url")
		requireWrapped(t, err)
	})
	t.Run("OpenReader refuses an empty buffer", func(t *testing.T) {
		st, err := OpenReader(nil, FormatUnknown, numfmt.DecimalAuto)
		require.ErrorContains(t, err, "dump is empty")
		require.Nil(t, st)
	})
}

// TestBrokenGzipDumpIsAnError gives a dump that has the gzip magic but no gzip
// header. The plain scan and the prefiltered scan both report it.
func TestBrokenGzipDumpIsAnError(t *testing.T) {
	st, err := OpenReader([]byte{0x1f, 0x8b}, FormatJSONL, numfmt.DecimalAuto)
	require.NoError(t, err)
	err = st.TypedScan(context.Background(), func([]query.Record) error { return nil })
	require.ErrorContains(t, err, "open gzip dump")
	err = st.ScanFiltered(context.Background(), matchEverything, func(map[string]any) error { return nil })
	require.ErrorContains(t, err, "open gzip dump")
}

// TestExtJSONSourceReportsAnEOFReadErrorInsideAnArray gives the mongoexport
// reader a reader that fails inside a --jsonArray dump with an error that wraps
// io.EOF. An array must end with its closing bracket, so the scan reports the
// error and does not end in silence with a truncated array.
func TestExtJSONSourceReportsAnEOFReadErrorInsideAnArray(t *testing.T) {
	gone := fmt.Errorf("disk gone: %w", io.EOF)
	tests := []struct {
		name  string
		input string
	}{
		{name: "after a comma", input: `[{"_id":"a"},`},
		{name: "after a document", input: `[{"_id":"a"}`},
		{name: "after the opening bracket", input: `[`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := extJSONSource(&dataThenErrReader{data: tt.input, err: gone}, pageSize, numfmt.DecimalAuto)
			err := src(context.Background(), func([]query.Record) error { return nil })
			require.ErrorContains(t, err, "decode mongoexport json")
			require.ErrorIs(t, err, gone)
		})
	}
}
