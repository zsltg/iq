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
	default:
		return FormatUnknown, fmt.Errorf("unknown dump format %q: want jsonl, json, yaml, mongoexport, bson, rdb, or dynamodb-json", s)
	}
}

// rdbMagic is the five-byte signature every RDB file opens with.
var rdbMagic = []byte("REDIS")

// detectFormat sniffs a dump file's format from its leading bytes plus its
// extension. Detection is reliable for the binary formats (RDB has a magic
// signature; BSON a length-framed structure) and disambiguates the JSON-text family
// by the first record's shape; YAML is not content-sniffable, so it relies on a
// .yaml/.yml extension (or ?format=/--from-format). Any case can be forced.
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
// filename, so it cannot fall back to an extension — YAML needs an explicit hint.
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

// looksLikeBSON reports whether head plausibly begins a BSON document: a 4-byte
// little-endian length that is at least the empty-document size and no larger than
// BSON's 16 MiB cap.
func looksLikeBSON(head []byte) bool {
	if len(head) < 5 {
		return false
	}
	size := int(head[0]) | int(head[1])<<8 | int(head[2])<<16 | int(head[3])<<24
	const minDoc, maxDoc = 5, 16 << 20
	return size >= minDoc && size <= maxDoc
}
