package file

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format identifies how a dump file is decoded.
type Format int

const (
	// FormatUnknown means detection has not run (or a ?format= was absent).
	FormatUnknown Format = iota
	// FormatJSONL is iq's own typed dump: {key,type,value} records as JSON Lines or a
	// single JSON array (both decode through query.JSONSource).
	FormatJSONL
	// FormatYAML is iq's typed dump as YAML documents of {key,type,value}.
	FormatYAML
	// FormatMongoexport is mongoexport output: Extended JSON, one document per line
	// or a single JSON array.
	FormatMongoexport
	// FormatBSON is mongodump output: concatenated raw BSON documents.
	FormatBSON
	// FormatRDB is a Redis RDB snapshot.
	FormatRDB
	// FormatDynamoDBJSON is DynamoDB's typed attribute-value JSON: a native S3
	// export (NDJSON, one {"Item":{…}} per line) or `aws dynamodb scan` output (a
	// {"Items":[…]} object). Not content-sniffable (it shares the leading '{' with
	// mongoexport), so it needs an explicit ?format=/--from-format.
	FormatDynamoDBJSON
	// FormatCassandraCSV is cqlsh `COPY … TO` CSV output. Not content-sniffable
	// (arbitrary CSV), so it needs an explicit ?format=/--from-format plus a
	// ?keys= key schema and optional ?types= column types.
	FormatCassandraCSV
	// FormatNeo4jJSON is APOC's JSON export (apoc.export.json.*): JSON Lines (default)
	// or a single JSON array of {"type":"node",…}/{"type":"relationship",…} objects.
	// Not content-sniffable (it shares the leading '{'/'[' with mongoexport and carries
	// no {key,value} envelope), so it needs an explicit ?format=/--from-format plus a
	// ?label=<Label> or ?rel=<Type> keyspace selector.
	FormatNeo4jJSON
)

// String returns the format's canonical name — the primary synonym ParseFormat
// accepts — so a detected format round-trips through a ?format= / --from-format
// value and reads the same in `iq ls -v`.
func (f Format) String() string {
	switch f {
	case FormatJSONL:
		return "jsonl"
	case FormatYAML:
		return "yaml"
	case FormatMongoexport:
		return "mongoexport"
	case FormatBSON:
		return "bson"
	case FormatRDB:
		return "rdb"
	case FormatDynamoDBJSON:
		return "dynamodb-json"
	case FormatCassandraCSV:
		return "cassandra-csv"
	case FormatNeo4jJSON:
		return "neo4j-json"
	default:
		return "unknown"
	}
}

// ParseFormat maps a ?format= / --from-format value to a Format, accepting the
// synonyms a user is likely to reach for. Exported so the cmd move importer shares
// the same names as a file:// URL's ?format=.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "jsonl", "json", "typed-jsonl", "typed":
		return FormatJSONL, nil
	case "yaml", "yml":
		return FormatYAML, nil
	case "mongoexport", "extjson", "ejson":
		return FormatMongoexport, nil
	case "bson", "mongodump":
		return FormatBSON, nil
	case "rdb", "redis":
		return FormatRDB, nil
	case "dynamodb-json", "ddb-json", "dynamodb", "ddb":
		return FormatDynamoDBJSON, nil
	case "cassandra-csv", "cql-csv", "cassandra", "cql":
		return FormatCassandraCSV, nil
	case "neo4j-json", "neo4j", "apoc-json", "apoc":
		return FormatNeo4jJSON, nil
	default:
		return FormatUnknown, fmt.Errorf("unknown dump format %q: want jsonl, json, yaml, mongoexport, bson, rdb, dynamodb-json, cassandra-csv, or neo4j-json", s)
	}
}

// FormatInfo describes one dump format a file:// source can read, for `iq driver ls
// -v`: the format, whether content detection recognizes it (else it must be forced
// with ?format=/--from-format), and the tool or shape that produces it.
type FormatInfo struct {
	Format Format
	Auto   bool
	Source string
}

// Name is the format's canonical ?format= value.
func (fi FormatInfo) Name() string { return fi.Format.String() }

// SupportedFormats lists the dump formats a file:// source can read, in detection
// order. It is the single source of truth behind `iq driver ls -v`, so a new reader
// is advertised by adding one entry here. Auto tracks classify(): a format that
// cannot be content-sniffed (it shares a lead byte with another, or is schemaless)
// advertises ?format= instead.
func SupportedFormats() []FormatInfo {
	return []FormatInfo{
		{FormatJSONL, true, "iq typed JSON Lines / array"},
		{FormatYAML, true, "iq typed YAML"},
		{FormatMongoexport, true, "mongoexport Extended JSON"},
		{FormatBSON, true, "mongodump BSON"},
		{FormatRDB, true, "Redis RDB snapshot"},
		{FormatDynamoDBJSON, false, "DynamoDB S3 export / scan JSON"},
		{FormatCassandraCSV, false, "cqlsh COPY TO CSV"},
		{FormatNeo4jJSON, false, "Neo4j APOC JSON export"},
	}
}

// rdbMagic is the five-byte signature every RDB file opens with.
var rdbMagic = []byte("REDIS")

// detectFormat sniffs a dump file's format from its leading bytes plus its
// extension. Detection is reliable for the binary formats (RDB has a magic
// signature; BSON a length-framed structure) and disambiguates the JSON-text and
// YAML families by the first record's shape — iq's typed dumps carry a
// {key,…,value} envelope. A .yaml/.yml extension still forces YAML for a dump whose
// first record overflows the sniff window; any case can be forced with ?format=.
func detectFormat(path string) (Format, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied dump file, by design.
	if err != nil {
		return FormatUnknown, fmt.Errorf("open dump %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	head, err := bufio.NewReader(f).Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return FormatUnknown, fmt.Errorf("read dump head %q: %w", path, err)
	}
	return classify(head, path)
}

// detectBytes sniffs the format of an in-memory dump buffer (piped stdin). It has no
// filename, so it cannot fall back to a .yaml/.yml extension; a typed YAML dump is
// recognized by its {key,…,value} envelope instead.
func detectBytes(data []byte) (Format, error) {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	return classify(head, "")
}

// classify maps a dump's leading bytes (and optional filename) to a Format.
func classify(head []byte, name string) (Format, error) {
	if len(head) == 0 {
		return FormatUnknown, errors.New("dump is empty")
	}
	if bytes.HasPrefix(head, rdbMagic) {
		return FormatRDB, nil
	}
	lname := strings.ToLower(name)
	if strings.HasSuffix(lname, ".yaml") || strings.HasSuffix(lname, ".yml") {
		return FormatYAML, nil
	}
	trimmed := bytes.TrimLeft(head, " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return detectJSONFamily(trimmed), nil
	}
	if strings.HasSuffix(lname, ".bson") || looksLikeBSON(head) {
		return FormatBSON, nil
	}
	// iq's typed YAML is block-style text (no leading '{'/'[' to route it into the
	// JSON family) and iq is its only producer, so — as for JSONL — a first document
	// carrying the {key,…,value} envelope identifies it.
	if obj, ok := firstYAMLObject(trimmed); ok {
		_, hasKey := obj["key"]
		_, hasValue := obj["value"]
		if hasKey && hasValue {
			return FormatYAML, nil
		}
	}
	return FormatUnknown, errors.New("cannot detect dump format; pass ?format= or --from-format (jsonl, json, yaml, mongoexport, bson, rdb)")
}

// detectJSONFamily distinguishes iq's typed dump from mongoexport by the first
// object's shape — a {key,…,value} envelope is iq's typed dump, anything else a
// mongo document. It handles both a top-level object and the first element of a
// JSON array (mongoexport --jsonArray, or a --typed --jsona dump).
func detectJSONFamily(trimmed []byte) Format {
	obj, ok := firstJSONObject(trimmed)
	if !ok {
		return FormatMongoexport // undecidable head; let the decoder report a real error.
	}
	_, hasKey := obj["key"]
	_, hasValue := obj["value"]
	if hasKey && hasValue {
		return FormatJSONL
	}
	return FormatMongoexport
}

// firstJSONObject decodes the first object in head — the whole head when it starts
// with '{', or the first element when it starts with a '[' array — and returns its
// top-level keys.
func firstJSONObject(head []byte) (map[string]json.RawMessage, bool) {
	h := bytes.TrimLeft(head, " \t\r\n")
	if len(h) > 0 && h[0] == '[' {
		h = bytes.TrimLeft(h[1:], " \t\r\n") // step past '[' to the first element.
	}
	var obj map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(h)).Decode(&obj); err != nil {
		return nil, false
	}
	return obj, true
}

// firstYAMLObject decodes the first document of head as a YAML mapping, so classify
// can recognize iq's typed {key,type,value} YAML dump by the same key+value envelope
// it uses for JSONL. A head truncated mid-document, or a first document that is not a
// mapping, yields no object and is left for a .yaml/.yml extension or explicit ?format=.
func firstYAMLObject(head []byte) (map[string]any, bool) {
	var obj map[string]any
	if err := yaml.NewDecoder(bytes.NewReader(head)).Decode(&obj); err != nil {
		return nil, false
	}
	return obj, obj != nil
}

// A BSON document is framed by a 4-byte little-endian length, and the spec caps a
// document at 16 MiB. bsonMinDoc is the empty document: the frame plus its
// terminator. Both the sniffer and the reader take that frame from an untrusted
// dump, so they share one bound.
const (
	bsonMinDoc = 5
	bsonMaxDoc = 16 << 20
)

// looksLikeBSON reports whether head plausibly begins a BSON document: a 4-byte
// little-endian length that is at least the empty-document size and no larger than
// BSON's 16 MiB cap.
func looksLikeBSON(head []byte) bool {
	if len(head) < bsonMinDoc {
		return false
	}
	size := int(head[0]) | int(head[1])<<8 | int(head[2])<<16 | int(head[3])<<24
	return size >= bsonMinDoc && size <= bsonMaxDoc
}
