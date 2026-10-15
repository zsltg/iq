package couchbase

import (
	"bytes"
	"encoding/json"

	"github.com/couchbase/gocb/v2"

	"github.com/zsltg/iq/internal/numfmt"
)

// decodeValue parses a stored document body into JSON-ready Go values. It decodes
// numbers with UseNumber so precision survives: an integer becomes an exact int or
// *big.Int, and a fractional number follows the decimal mode. Plain json.Unmarshal
// collapses every number to a float64 and silently loses precision beyond 2^53. A
// body that is not valid JSON (a binary document) is surfaced as a string, the
// redis stringReader precedent. So a KV get never fails on non-JSON data.
func decodeValue(raw []byte, mode numfmt.DecimalMode) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	return numfmt.ConvertNumbers(v, mode)
}

// rawTranscoder hands back the stored bytes of a KV document, whatever its datatype
// flag. The driver then decodes them through decodeValue (the same UseNumber path as
// query rows) and can render a binary document as a string. The gocb
// RawJSONTranscoder refuses non-JSON datatypes, so it cannot serve that path. This
// transcoder is read-only: writes use the default JSON transcoder.
type rawTranscoder struct{}

// Decode copies the stored bytes into a *[]byte, ignoring the datatype flag.
func (rawTranscoder) Decode(data []byte, _ uint32, out any) error {
	p, ok := out.(*[]byte)
	if !ok {
		return errNonBytesTarget
	}
	*p = data
	return nil
}

// Encode is never used, because writes go through the cluster's default JSON
// transcoder. So it refuses rather than pretends to serialize.
func (rawTranscoder) Encode(any) ([]byte, uint32, error) {
	return nil, 0, errEncodeUnsupported
}

// compile-time assertion that rawTranscoder satisfies the SDK transcoder contract.
var _ gocb.Transcoder = rawTranscoder{}
