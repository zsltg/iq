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
	// FormatJSONL is iq's own typed dump: one {key,type,value} object per line.
	FormatJSONL
	// FormatMongoexport is mongoexport output: Extended JSON, one document per line
	// or a single JSON array.
	FormatMongoexport
	// FormatBSON is mongodump output: concatenated raw BSON documents.
	FormatBSON
	// FormatRDB is a Redis RDB snapshot.
	FormatRDB
)

// parseFormat maps a ?format= value (or copy --from-format value) to a Format,
// accepting the synonyms a user is likely to reach for.
func parseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "jsonl", "typed-jsonl", "typed":
		return FormatJSONL, nil
	case "mongoexport", "extjson", "ejson", "json":
		return FormatMongoexport, nil
	case "bson", "mongodump":
		return FormatBSON, nil
	case "rdb", "redis":
		return FormatRDB, nil
	default:
		return FormatUnknown, fmt.Errorf("unknown dump format %q: want rdb, bson, mongoexport, or jsonl", s)
	}
}

// rdbMagic is the five-byte signature every RDB file opens with.
var rdbMagic = []byte("REDIS")

// detectFormat sniffs a dump file's format from its leading bytes, falling back to
// the extension for the ambiguous JSON-text family. Detection is reliable for the
// binary formats (RDB has a magic signature; BSON a length-framed structure); the
// JSON-text formats are disambiguated by the first record's shape, and an
// ambiguous case can always be forced with ?format=.
func detectFormat(path string) (Format, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied dump file, by design.
	if err != nil {
		return FormatUnknown, fmt.Errorf("open dump %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReader(f)
	head, err := br.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return FormatUnknown, fmt.Errorf("read dump head %q: %w", path, err)
	}
	if len(head) == 0 {
		return FormatUnknown, fmt.Errorf("dump %q is empty", path)
	}

	if bytes.HasPrefix(head, rdbMagic) {
		return FormatRDB, nil
	}

	trimmed := bytes.TrimLeft(head, " \t\r\n")
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return detectJSONFamily(trimmed), nil
	}

	// Not RDB and not JSON text: assume mongodump BSON (a length-framed binary
	// document stream). The extension confirms the common case.
	if strings.HasSuffix(strings.ToLower(path), ".bson") || looksLikeBSON(head) {
		return FormatBSON, nil
	}
	return FormatUnknown, fmt.Errorf("cannot detect dump format for %q; pass ?format=rdb|bson|mongoexport|jsonl", path)
}

// detectJSONFamily distinguishes iq typed JSONL from mongoexport Extended JSON by
// the first object's shape: a {key,value} envelope is iq's own dump; anything else
// (a document, with or without $-prefixed Extended-JSON markers) is mongoexport.
func detectJSONFamily(trimmed []byte) Format {
	if trimmed[0] == '[' {
		return FormatMongoexport // a JSON array is mongoexport --jsonArray.
	}
	// Decode just the first object's keys.
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var obj map[string]json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		return FormatMongoexport // undecidable head; let the decoder report a real error.
	}
	_, hasKey := obj["key"]
	_, hasValue := obj["value"]
	if hasKey && hasValue {
		return FormatJSONL
	}
	return FormatMongoexport
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
