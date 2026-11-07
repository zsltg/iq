package file

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// typedObjectOfSize renders a typed {key,…,value} JSON object of exactly n
// bytes. A test can then place a record's closing brace on either side of the
// sniff window. The envelope is 20 bytes, and the rest is key padding.
func typedObjectOfSize(t *testing.T, n int) string {
	t.Helper()
	const envelope = len(`{"key":"","value":1}`)
	require.GreaterOrEqual(t, n, envelope)
	return `{"key":"` + strings.Repeat("p", n-envelope) + `","value":1}`
}

// TestParseFormat covers every ?format= / --from-format synonym that the reader
// advertises, plus the rejection. A dropped synonym or a mistyped mapping is
// then a failure, not a silently different reader.
func TestParseFormat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Format
	}{
		{name: "jsonl", in: "jsonl", want: FormatJSONL},
		{name: "json is jsonl", in: "json", want: FormatJSONL},
		{name: "typed-jsonl is jsonl", in: "typed-jsonl", want: FormatJSONL},
		{name: "typed is jsonl", in: "typed", want: FormatJSONL},
		{name: "yaml", in: "yaml", want: FormatYAML},
		{name: "yml is yaml", in: "yml", want: FormatYAML},
		{name: "mongoexport", in: "mongoexport", want: FormatMongoexport},
		{name: "extjson is mongoexport", in: "extjson", want: FormatMongoexport},
		{name: "ejson is mongoexport", in: "ejson", want: FormatMongoexport},
		{name: "bson", in: "bson", want: FormatBSON},
		{name: "mongodump is bson", in: "mongodump", want: FormatBSON},
		{name: "rdb", in: "rdb", want: FormatRDB},
		{name: "redis is rdb", in: "redis", want: FormatRDB},
		{name: "dynamodb-json", in: "dynamodb-json", want: FormatDynamoDBJSON},
		{name: "ddb-json is dynamodb", in: "ddb-json", want: FormatDynamoDBJSON},
		{name: "dynamodb", in: "dynamodb", want: FormatDynamoDBJSON},
		{name: "ddb is dynamodb", in: "ddb", want: FormatDynamoDBJSON},
		{name: "cassandra-csv", in: "cassandra-csv", want: FormatCassandraCSV},
		{name: "cql-csv is cassandra", in: "cql-csv", want: FormatCassandraCSV},
		{name: "cassandra", in: "cassandra", want: FormatCassandraCSV},
		{name: "cql is cassandra", in: "cql", want: FormatCassandraCSV},
		{name: "neo4j-json", in: "neo4j-json", want: FormatNeo4jJSON},
		{name: "neo4j", in: "neo4j", want: FormatNeo4jJSON},
		{name: "apoc-json is neo4j", in: "apoc-json", want: FormatNeo4jJSON},
		{name: "apoc is neo4j", in: "apoc", want: FormatNeo4jJSON},
		{name: "case and space are folded", in: "  RDB\t", want: FormatRDB},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFormat(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want.String(), got.String())
		})
	}
	t.Run("unknown value names the accepted set", func(t *testing.T) {
		got, err := ParseFormat("parquet")
		require.Error(t, err)
		require.Equal(t, FormatUnknown, got)
		require.Contains(t, err.Error(), `unknown dump format "parquet"`)
		require.Contains(t, err.Error(), "neo4j-json")
	})
}

// TestClassify pins the whole decision table of the content sniffer. The RDB
// magic wins outright. A .yaml/.yml name forces YAML. A leading brace or
// bracket routes into the JSON family, where the {key,…,value} envelope
// separates iq's typed dump from mongoexport. A .bson name or a plausible
// length frame selects BSON. A typed YAML document is recognized by the same
// envelope. Anything else is a named error, not a guess.
func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		head    string
		file    string
		want    Format
		wantErr string
	}{
		{name: "empty head is an error", head: "", wantErr: "dump is empty"},
		{name: "rdb magic wins", head: "REDIS0011\x00\x00", want: FormatRDB},
		{name: "rdb magic beats a yaml name", head: "REDIS0011", file: "d.yaml", want: FormatRDB},
		{name: "yaml extension forces yaml", head: "not: an envelope\n", file: "d.yaml", want: FormatYAML},
		{name: "yml extension forces yaml", head: "not: an envelope\n", file: "d.yml", want: FormatYAML},
		{name: "extension match is case-insensitive", head: "not: an envelope\n", file: "D.YAML", want: FormatYAML},
		{name: "typed object is jsonl", head: `{"key":"a","type":"string","value":1}`, want: FormatJSONL},
		{name: "key without value is mongoexport", head: `{"key":"a","other":1}`, want: FormatMongoexport},
		{name: "value without key is mongoexport", head: `{"value":1,"other":2}`, want: FormatMongoexport},
		{name: "mongo document is mongoexport", head: `{"_id":1,"n":2}`, want: FormatMongoexport},
		{name: "typed array is jsonl", head: `[{"key":"a","value":1}]`, want: FormatJSONL},
		{name: "mongo array is mongoexport", head: `[{"_id":1}]`, want: FormatMongoexport},
		{name: "leading whitespace still routes to json", head: "  \n\t" + `{"key":"a","value":1}`, want: FormatJSONL},
		{name: "lone brace is an undecidable json head", head: "{", want: FormatMongoexport},
		{name: "lone bracket is an undecidable json head", head: "[", want: FormatMongoexport},
		{name: "truncated object is an undecidable json head", head: `{"key":"a","valu`, want: FormatMongoexport},
		{name: "length frame looks like bson", head: "\x0c\x00\x00\x00\x02a\x00", want: FormatBSON},
		{name: "bson extension forces bson", head: "\x00\x00\x00\x00\x00", file: "d.bson", want: FormatBSON},
		{name: "typed yaml document is yaml", head: "key: a\ntype: string\nvalue: 1\n", want: FormatYAML},
		{name: "yaml key without value is undetectable", head: "key: a\nother: b\n", wantErr: "cannot detect dump format"},
		{name: "yaml value without key is undetectable", head: "value: 1\nother: b\n", wantErr: "cannot detect dump format"},
		{name: "yaml sequence is undetectable", head: "- a\n- b\n", wantErr: "cannot detect dump format"},
		{name: "yaml mapping that fails to decode is undetectable", head: "key: a\nvalue: 1\n[x]: y\n", wantErr: "cannot detect dump format"},
		{name: "yaml null document is undetectable", head: "null\n", wantErr: "cannot detect dump format"},
		{name: "plain text is undetectable", head: "hello there\n", wantErr: "cannot detect dump format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyBytes([]byte(tt.head), tt.file)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Equal(t, FormatUnknown, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestFirstJSONObject covers the helper that classify leans on directly,
// including the empty input that its callers never pass. An unguarded index into
// it panics.
func TestFirstJSONObject(t *testing.T) {
	tests := []struct {
		name string
		head string
		want []string
	}{
		{name: "empty input yields nothing", head: "", want: nil},
		{name: "whitespace only yields nothing", head: "  \n\t"},
		{name: "object yields its top-level keys", head: `{"key":"a","value":1}`, want: []string{"key", "value"}},
		{name: "array yields its first element's keys", head: `[{"key":"a"},{"other":1}]`, want: []string{"key"}},
		{name: "bare bracket yields nothing", head: "["},
		{name: "scalar yields nothing", head: "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSniffer([]byte(tt.head))
			trimmed := bytes.TrimLeft([]byte(tt.head), " \t\r\n")
			obj, ok := s.firstJSONObject(len(trimmed) > 0 && trimmed[0] == '[')
			if tt.want == nil {
				require.False(t, ok)
				require.Nil(t, obj)
				return
			}
			require.True(t, ok)
			require.Len(t, obj, len(tt.want))
			for _, k := range tt.want {
				require.Contains(t, obj, k)
			}
		})
	}
}

// TestLooksLikeBSON pins the four-byte little-endian length frame byte by byte
// and at both bounds. Each byte carries its own weight. The minimum is the empty
// document's five bytes, and the maximum is BSON's 16 MiB cap.
func TestLooksLikeBSON(t *testing.T) {
	tests := []struct {
		name string
		head []byte
		want bool
	}{
		{name: "four bytes is too short to frame", head: []byte{5, 0, 0, 0}, want: false},
		{name: "empty document is the minimum", head: []byte{5, 0, 0, 0, 0}, want: true},
		{name: "one below the minimum", head: []byte{4, 0, 0, 0, 0}, want: false},
		{name: "second byte weighs 256", head: []byte{0, 1, 0, 0, 0}, want: true},
		{name: "third byte weighs 65536", head: []byte{0, 0, 1, 0, 0}, want: true},
		{name: "fourth byte at the 16 MiB cap", head: []byte{0, 0, 0, 1, 0}, want: true},
		{name: "one byte past the cap", head: []byte{1, 0, 0, 1, 0}, want: false},
		{name: "two high bytes are far past the cap", head: []byte{0, 0, 0, 2, 0}, want: false},
		{name: "the three low bytes saturate one under the cap", head: []byte{255, 255, 255, 0, 0}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, looksLikeBSON(tt.head))
		})
	}
}

// TestDetectFormatFileErrors covers the two failure paths of the file sniffer.
// An unopenable path is reported as an open failure whose cause survives the
// wrap. A path that opens but cannot be read (a directory) is reported as a
// head read failure, not misclassified.
func TestDetectFormatFileErrors(t *testing.T) {
	t.Run("missing file reports the open failure", func(t *testing.T) {
		got, err := detectFormat(filepath.Join(t.TempDir(), "nope.jsonl"))
		require.Error(t, err)
		require.Equal(t, FormatUnknown, got)
		require.Contains(t, err.Error(), "open dump")
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
	t.Run("unreadable file reports the head read failure", func(t *testing.T) {
		got, err := detectFormat(t.TempDir()) // a directory opens but never reads.
		require.Error(t, err)
		require.Equal(t, FormatUnknown, got)
		require.Contains(t, err.Error(), "read dump head")
		requireWrapped(t, err)
	})
}

// classifyBytes sniffs data as a dump with the given file name and the real limit.
func classifyBytes(data []byte, name string) (Format, error) {
	format, _, err := detectStream(bytes.NewReader(data), name, query.MaxRecordBytes)
	return format, err
}

// newTestSniffer returns a sniffer that has read the head of data, as
// detectStream does before it classifies.
func newTestSniffer(data []byte) *sniffer {
	r := bytes.NewReader(data)
	var buf bytes.Buffer
	_, _ = io.CopyN(&buf, r, headSize)
	return &sniffer{buf: &buf, rest: r, limit: query.MaxRecordBytes}
}

// typedYAMLOfSize renders a typed YAML document of about n bytes.
func typedYAMLOfSize(n int) string {
	return "---\nkey: a\ntype: string\nvalue: " + strings.Repeat("v", n) + "\n---\nkey: b\ntype: string\nvalue: w\n"
}

// bigJSONLines renders three typed JSONL records. The first has a key of n bytes.
func bigJSONLines(n int) string {
	return `{"key":"` + strings.Repeat("p", n) + `","value":1}` + "\n" +
		`{"key":"b","value":2}` + "\n" + `{"key":"c","value":3}` + "\n"
}

// TestDetectFirstRecord checks that detection reads to the end of the first
// record, whatever its size, for a file and for piped stdin. The first record
// is over the 512-byte head and over the 4096-byte bufio buffer. A typed dump
// stays JSONL or YAML. A mongoexport dump with a large first document stays
// mongoexport.
func TestDetectFirstRecord(t *testing.T) {
	tests := []struct {
		name string
		data string
		want Format
	}{
		{name: "typed jsonl, record of 600 bytes", data: bigJSONLines(600), want: FormatJSONL},
		{name: "typed jsonl, record of 512 bytes", data: typedObjectOfSize(t, 512) + "\n", want: FormatJSONL},
		{name: "typed jsonl, record of 513 bytes", data: typedObjectOfSize(t, 513) + "\n", want: FormatJSONL},
		{name: "typed jsonl, record of 5000 bytes", data: bigJSONLines(5000), want: FormatJSONL},
		{name: "typed jsonl, record of 100000 bytes", data: bigJSONLines(100000), want: FormatJSONL},
		{name: "typed json array, record of 5000 bytes", data: "[" + typedObjectOfSize(t, 5000) + ",{\"key\":\"b\",\"value\":2}]", want: FormatJSONL},
		{name: "typed json array, record of 600 bytes", data: "[\n  " + typedObjectOfSize(t, 600) + "\n]", want: FormatJSONL},
		{name: "typed yaml, document of 5000 bytes", data: typedYAMLOfSize(5000), want: FormatYAML},
		{name: "typed yaml, document of 600 bytes", data: typedYAMLOfSize(600), want: FormatYAML},
		{name: "mongoexport, document of 5000 bytes", data: `{"_id":1,"s":"` + strings.Repeat("m", 5000) + `"}` + "\n", want: FormatMongoexport},
		{name: "mongoexport array, document of 5000 bytes", data: `[{"_id":1,"s":"` + strings.Repeat("m", 5000) + `"}]`, want: FormatMongoexport},
		{name: "truncated first record is left to the reader", data: `{"key":"` + strings.Repeat("p", 5000), want: FormatMongoexport},
		{name: "invalid first record is left to the reader", data: `{"key":"a" "value":` + strings.Repeat("1", 5000), want: FormatMongoexport},
	}
	for _, tt := range tests {
		t.Run(tt.name+" from a file", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dump")
			require.NoError(t, os.WriteFile(path, []byte(tt.data), 0o600))
			got, err := detectFormat(path)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
		t.Run(tt.name+" from stdin", func(t *testing.T) {
			got, replay, err := detectStream(iotest.OneByteReader(strings.NewReader(tt.data)), "", query.MaxRecordBytes)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			all, err := io.ReadAll(replay)
			require.NoError(t, err)
			require.Equal(t, tt.data, string(all), "the replay must hold every byte of the stream")
		})
	}
}

// TestDetectStreamReplay checks that the stream that detectStream returns holds
// every byte, for each way the sniff can end.
func TestDetectStreamReplay(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "short input", data: `{"key":"a","value":1}` + "\n"},
		{name: "rdb magic", data: "REDIS0011" + strings.Repeat("x", 2000)},
		{name: "bson frame", data: "\x0c\x00\x00\x00\x02a\x00" + strings.Repeat("x", 2000)},
		{name: "first record then many records", data: bigJSONLines(3000) + strings.Repeat(`{"key":"z","value":9}`+"\n", 500)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, replay, err := detectStream(strings.NewReader(tt.data), "", query.MaxRecordBytes)
			require.NoError(t, err)
			all, err := io.ReadAll(replay)
			require.NoError(t, err)
			require.Equal(t, tt.data, string(all))
		})
	}
}

// TestDetectStreamReadsOnlyFirstRecord checks that detection stops at the end of
// the first record. It does not read the whole stream.
func TestDetectStreamReadsOnlyFirstRecord(t *testing.T) {
	data := bigJSONLines(3000) + strings.Repeat(`{"key":"z","value":9}`+"\n", 100000)
	cr := &countReader{r: strings.NewReader(data)}
	_, _, err := detectStream(cr, "", query.MaxRecordBytes)
	require.NoError(t, err)
	require.Less(t, cr.n, 20000, "detection must not read past the first record by much")
}

// TestDetectStreamReadsOnlyHeadForBinary pins the head size. A binary dump is
// decided by its head, so the sniff reads exactly 512 bytes and no more.
func TestDetectStreamReadsOnlyHeadForBinary(t *testing.T) {
	tests := []struct {
		name string
		data string
		want Format
	}{
		{name: "rdb", data: "REDIS0011" + strings.Repeat("x", 3000), want: FormatRDB},
		{name: "bson", data: "\x0c\x00\x00\x00\x02a\x00" + strings.Repeat("x", 3000), want: FormatBSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &countReader{r: strings.NewReader(tt.data)}
			got, _, err := detectStream(cr, "", query.MaxRecordBytes)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, 512, cr.n)
		})
	}
}

// countReader counts the bytes that it passes on.
type countReader struct {
	r io.Reader
	n int
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestDetectStreamLimit pins the bound on the first record from both sides. A
// record of exactly the limit is complete. A record one byte longer is an
// error that names the way out. The same holds for JSON, a JSON array, and
// YAML.
func TestDetectStreamLimit(t *testing.T) {
	const limit = 2000
	tests := []struct {
		name    string
		data    string
		want    Format
		tooBig  bool
		wantErr string
	}{
		{name: "jsonl record of exactly the limit", data: typedObjectOfSize(t, limit), want: FormatJSONL},
		{name: "jsonl record one byte over the limit", data: typedObjectOfSize(t, limit+1), tooBig: true},
		{name: "jsonl record far over the limit", data: typedObjectOfSize(t, limit*5) + "\n", tooBig: true},
		{name: "jsonl record of exactly the limit with a newline", data: typedObjectOfSize(t, limit) + "\n" + `{"key":"b","value":2}`, want: FormatJSONL},
		{name: "array element of exactly the limit", data: "[" + typedObjectOfSize(t, limit-2) + "]", want: FormatJSONL},
		{name: "array element over the limit", data: "[" + typedObjectOfSize(t, limit) + "]", tooBig: true},
		{name: "yaml document over the limit", data: typedYAMLOfSize(limit * 2), tooBig: true},
		{name: "mongoexport document over the limit", data: `{"_id":1,"s":"` + strings.Repeat("m", limit) + `"}`, tooBig: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := detectStream(strings.NewReader(tt.data), "", limit)
			if tt.tooBig {
				require.ErrorIs(t, err, errValueTooLarge)
				require.ErrorContains(t, err, "first record is over 2000 bytes")
				require.ErrorContains(t, err, "?format=")
				require.ErrorContains(t, err, "--from-format")
				require.Equal(t, FormatUnknown, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	t.Run("a small input never reads past its end", func(t *testing.T) {
		got, _, err := detectStream(strings.NewReader(`{"key":"a","value":1}`), "", limit)
		require.NoError(t, err)
		require.Equal(t, FormatJSONL, got)
	})
	t.Run("a head longer than the limit is bounded", func(t *testing.T) {
		_, _, err := detectStream(strings.NewReader(typedObjectOfSize(t, 600)), "", 100)
		require.ErrorIs(t, err, errValueTooLarge)
	})
}

// TestDetectStreamHeadError checks that a read failure in the head is wrapped
// with context and keeps its cause.
func TestDetectStreamHeadError(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := detectStream(iotest.ErrReader(boom), "", query.MaxRecordBytes)
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "read dump head")
}

// TestOpenPipeLargeFirstRecord checks the whole stdin path. The store reads
// every record of a dump whose first record is far over the head.
func TestOpenPipeLargeFirstRecord(t *testing.T) {
	st, err := OpenPipe(strings.NewReader(bigJSONLines(20000)), FormatUnknown, numfmt.DecimalAuto)
	require.NoError(t, err)
	require.Equal(t, FormatJSONL, st.format)
	recs := collect(t, st)
	require.Len(t, recs, 3)
	require.Contains(t, recs, "b")
	require.Contains(t, recs, "c")
}

// TestValidFrame pins the BSON frame bounds at both edges: the empty document is
// the smallest frame, and 16 MiB is the largest.
func TestValidFrame(t *testing.T) {
	tests := []struct {
		name string
		size int
		want bool
	}{
		{"below the empty document", bsonMinDoc - 1, false},
		{"the empty document", bsonMinDoc, true},
		{"the 16 MiB cap", bsonMaxDoc, true},
		{"above the cap", bsonMaxDoc + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, validFrame(tt.size))
		})
	}
}
