package file

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// typedObjectOfSize renders a typed {key,…,value} JSON object of exactly n bytes,
// so a test can place a record's closing brace on either side of the sniff
// window. The envelope is 20 bytes, the rest is key padding.
func typedObjectOfSize(t *testing.T, n int) string {
	t.Helper()
	const envelope = len(`{"key":"","value":1}`)
	require.GreaterOrEqual(t, n, envelope)
	return `{"key":"` + strings.Repeat("p", n-envelope) + `","value":1}`
}

// TestParseFormat covers every ?format= / --from-format synonym the reader
// advertises plus the rejection, so a dropped synonym or a mistyped mapping is a
// failure rather than a silently different reader.
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

// TestClassify pins the content sniffer's whole decision table: the RDB magic wins
// outright, a .yaml/.yml name forces YAML, a leading brace or bracket routes into
// the JSON family where the {key,…,value} envelope separates iq's typed dump from
// mongoexport, a .bson name or a plausible length frame selects BSON, and a typed
// YAML document is recognized by the same envelope. Anything else is a named
// error rather than a guess.
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
		{name: "yaml null document is undetectable", head: "null\n", wantErr: "cannot detect dump format"},
		{name: "plain text is undetectable", head: "hello there\n", wantErr: "cannot detect dump format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classify([]byte(tt.head), tt.file)
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

// TestFirstJSONObject covers the helper classify leans on directly, including the
// empty input its callers never pass: an unguarded index into it would panic.
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
			obj, ok := firstJSONObject([]byte(tt.head))
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

// TestLooksLikeBSON pins the four-byte little-endian length frame byte by byte and
// at both bounds: each byte carries its own weight, the minimum is the empty
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

// TestDetectFormatFileErrors covers the two failure paths of the file sniffer: an
// unopenable path is reported as an open failure whose cause survives the wrap,
// and a path that opens but cannot be read (a directory) is reported as a head
// read failure rather than misclassified.
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
	})
}

// TestDetectSniffWindow pins the 512-byte sniff window from both sides. A first
// record that ends exactly on the boundary is decodable and identifies the dump; a
// record one byte longer is truncated in the window, so the head is undecidable and
// the sniffer falls back to mongoexport rather than guessing.
func TestDetectSniffWindow(t *testing.T) {
	const trailer = "\n" + `{"key":"b","value":2}` + "\n"
	tests := []struct {
		name string
		size int
		want Format
	}{
		{name: "record ending on the boundary is decodable", size: 512, want: FormatJSONL},
		{name: "record one byte past the boundary is truncated", size: 513, want: FormatMongoexport},
	}
	for _, tt := range tests {
		t.Run(tt.name+" from a file", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dump.json")
			require.NoError(t, os.WriteFile(path, []byte(typedObjectOfSize(t, tt.size)+trailer), 0o600))
			got, err := detectFormat(path)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
		t.Run(tt.name+" from a buffer", func(t *testing.T) {
			got, err := detectBytes([]byte(typedObjectOfSize(t, tt.size) + trailer))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	t.Run("a short buffer is sniffed whole", func(t *testing.T) {
		got, err := detectBytes([]byte(`{"key":"a","value":1}` + "\n"))
		require.NoError(t, err)
		require.Equal(t, FormatJSONL, got)
	})
	t.Run("an empty buffer is an error", func(t *testing.T) {
		_, err := detectBytes(nil)
		require.ErrorContains(t, err, "dump is empty")
	})
}
