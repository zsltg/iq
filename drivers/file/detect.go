package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zsltg/iq/internal/query"
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
	// It is not content-sniffable, because it shares the leading '{'/'[' with
	// mongoexport and carries no {key,value} envelope. So it needs an explicit
	// ?format=/--from-format plus a ?label=<Label> or ?rel=<Type> keyspace selector.
	FormatNeo4jJSON
)

// String returns the canonical name of the format, the primary synonym that
// ParseFormat accepts. A detected format therefore round-trips through a
// ?format= / --from-format value and reads the same in `iq ls -v`.
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

// FormatInfo describes one dump format that a file:// source can read, for `iq
// driver ls -v`. It holds the format, whether content detection recognizes it
// (else it must be forced with ?format=/--from-format), and the tool or shape
// that produces it.
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

// detectFormat sniffs a dump file's format from its content and, for YAML, its
// extension. Detection is reliable for the binary formats (RDB has a magic
// signature, and BSON has a length-framed structure). It disambiguates the
// JSON-text and YAML families by the shape of the first complete record. iq's
// typed dumps carry a {key,…,value} envelope. A .yaml/.yml extension forces
// YAML. Any case can be forced with ?format=.
func detectFormat(path string) (Format, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied dump file, by design.
	if err != nil {
		return FormatUnknown, fmt.Errorf("open dump %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	format, _, err := detectStream(f, path, query.MaxRecordBytes)
	return format, err
}

// headSize is the number of leading bytes that detection reads first. The magic
// signature, the BSON frame, and the first byte of a text dump all fit in it.
const headSize = 512

// detectStream sniffs the format of r. The name is the dump file name, or empty
// for piped stdin, which has no extension to fall back on. The sniff reads the
// head, and for a JSON or YAML dump it reads on to the end of the first
// record. It never reads more than limit bytes past the start. A first record
// that is not complete within limit is an error, because a guess could read the
// dump as the wrong format without any message.
//
// The sniff consumes bytes from r. So the returned reader replays every byte
// that the sniff read, then continues with the rest of r. The caller reads the
// dump from that reader and does not touch r again. A small input never costs
// more memory than its own size.
func detectStream(r io.Reader, name string, limit int64) (Format, io.Reader, error) {
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, r, headSize); err != nil && !errors.Is(err, io.EOF) {
		return FormatUnknown, nil, fmt.Errorf("read dump head: %w", err)
	}
	s := &sniffer{buf: &buf, rest: r, limit: limit}
	format, err := s.classify(name)
	if err != nil {
		return FormatUnknown, nil, err
	}
	return format, io.MultiReader(&buf, r), nil
}

// sniffer reads the start of a dump for detection. The buffer holds every byte
// that it has read from rest, in order, so the caller can replay them.
type sniffer struct {
	buf   *bytes.Buffer
	rest  io.Reader
	limit int64
}

// record returns a reader over the buffered head followed by the rest of the
// dump. The bytes that it reads from rest go to the buffer. It stops at the
// limit, so a record of exactly the limit is complete and a longer one is not. Call it once, before any other read from rest.
func (s *sniffer) record() io.Reader {
	head := s.buf.Bytes()
	// A negative budget reads nothing, as for a head that is longer than the limit.
	budget := s.limit - int64(len(head))
	return io.MultiReader(bytes.NewReader(head), io.TeeReader(io.LimitReader(s.rest, budget), s.buf))
}

// overLimit reports whether the sniff has read up to the limit. A decode that
// fails after that point failed because the record does not end in the limit.
// A truncated dump of exactly the limit in size gets the same error. That case
// is too rare to tell apart.
func (s *sniffer) overLimit() bool {
	return int64(s.buf.Len()) >= s.limit
}

// errFirstRecordTooLarge is the error for a first record that does not end in
// the limit. It tells the user how to name the format.
func (s *sniffer) errFirstRecordTooLarge() error {
	return fmt.Errorf("cannot detect dump format: the first record is over %d bytes (%w). Pass ?format= or --from-format (jsonl, json, yaml, mongoexport, bson, rdb)", s.limit, errValueTooLarge)
}

// classify maps a dump's leading bytes (and optional filename) to a Format. It
// reads on from the dump only for a JSON or YAML first record.
func (s *sniffer) classify(name string) (Format, error) {
	head := s.buf.Bytes()
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
		return s.detectJSONFamily(trimmed[0] == '[')
	}
	if strings.HasSuffix(lname, ".bson") || looksLikeBSON(head) {
		return FormatBSON, nil
	}
	// iq's typed YAML is block-style text (no leading '{'/'[' to route it into the
	// JSON family) and iq is its only producer. So, as for JSONL, a first document
	// carrying the {key,…,value} envelope identifies it.
	obj, ok := s.firstYAMLObject()
	if s.overLimit() {
		return FormatUnknown, s.errFirstRecordTooLarge()
	}
	if ok {
		_, hasKey := obj["key"]
		_, hasValue := obj["value"]
		if hasKey && hasValue {
			return FormatYAML, nil
		}
	}
	return FormatUnknown, errors.New("cannot detect dump format; pass ?format= or --from-format (jsonl, json, yaml, mongoexport, bson, rdb)")
}

// detectJSONFamily distinguishes iq's typed dump from mongoexport by the shape
// of the first object. A {key,…,value} envelope is iq's typed dump. Anything
// else is a mongo document. It handles both a top-level object and the first
// element of a JSON array (mongoexport --jsonArray, or a --typed --jsona dump).
// A first record that fails to decode inside the limit is a truncated or
// invalid dump. It is read as mongoexport, so that the reader reports the real
// error. A first record that does not end in the limit is an error here.
func (s *sniffer) detectJSONFamily(array bool) (Format, error) {
	obj, ok := s.firstJSONObject(array)
	if !ok {
		if s.overLimit() {
			return FormatUnknown, s.errFirstRecordTooLarge()
		}
		return FormatMongoexport, nil
	}
	_, hasKey := obj["key"]
	_, hasValue := obj["value"]
	if hasKey && hasValue {
		return FormatJSONL, nil
	}
	return FormatMongoexport, nil
}

// firstJSONObject decodes the first object of the dump and returns its
// top-level keys. That object is the whole dump head when it starts with '{', or
// the first element when it starts with a '[' array.
func (s *sniffer) firstJSONObject(array bool) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(s.record())
	if array {
		if _, err := dec.Token(); err != nil { // step past '[' to the first element.
			return nil, false
		}
	}
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	return obj, true
}

// firstYAMLObject decodes the first document of the dump as a YAML mapping. Then
// classify can recognize iq's typed {key,type,value} YAML dump by the same
// key+value envelope that it uses for JSONL. A first document that is not a
// mapping, or that does not decode, yields no object. A YAML document that
// reaches the limit decodes as a cut-off scalar without an error. So classify
// checks the limit after this call, not only after a failure. Such a dump is left for a
// .yaml/.yml extension or explicit ?format=.
func (s *sniffer) firstYAMLObject() (map[string]any, bool) {
	var obj map[string]any
	if err := yaml.NewDecoder(s.record()).Decode(&obj); err != nil {
		return nil, false
	}
	return obj, obj != nil
}

// A BSON document is framed by a 4-byte little-endian length, and the spec caps a
// document at 16 MiB. The constant bsonMinDoc is the empty document: the frame
// plus its terminator. The 16 MiB cap is the BSON spec value. It stays apart from
// query.MaxRecordBytes, which caps a text dump value, so a change to one does not
// change the other. Both the sniffer and the reader take that frame from an
// untrusted dump, so they share one bound.
const (
	bsonMinDoc = 5
	bsonMaxDoc = 16 << 20
)

// validFrame reports whether a BSON frame length is inside the spec's bounds: at
// least the empty document and at most the 16 MiB cap.
func validFrame(size int) bool {
	return size >= bsonMinDoc && size <= bsonMaxDoc
}

// looksLikeBSON reports whether head plausibly begins a BSON document. That means
// a 4-byte little-endian length that is at least the empty-document size and no
// larger than BSON's 16 MiB cap.
func looksLikeBSON(head []byte) bool {
	if len(head) < bsonMinDoc {
		return false
	}
	size := int(head[0]) | int(head[1])<<8 | int(head[2])<<16 | int(head[3])<<24
	return validFrame(size)
}
