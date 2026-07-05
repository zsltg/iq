// Package render holds output helpers shared by the CLI and the backend
// adapters, so JSON is colored one way everywhere. It is an outer, presentation
// concern: the query core does not import it.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/neilotoole/jsoncolor"
)

// Encoder is a streaming JSON sink: one value per Encode call. Both the stdlib
// encoder and the colored encoder satisfy it.
type Encoder interface{ Encode(v any) error }

// jsonColors is the JSON syntax palette. It mirrors jsoncolor.DefaultColors but
// dims punctuation rather than leaving it uncolored: the encoder always writes a
// reset after each punctuation mark, so an empty punctuation color yields a
// stray, unpaired reset (`{` followed by a bare \x1b[0m). A real, faint color
// keeps every reset paired and the escape stream well-formed, and reads well —
// structural punctuation recedes so the values stand out.
var jsonColors = &jsoncolor.Colors{
	Key:    jsoncolor.Color("\x1b[34;1m"),
	String: jsoncolor.Color("\x1b[32m"),
	Number: jsoncolor.Color("\x1b[36m"),
	Bool:   jsoncolor.Color("\x1b[1m"),
	Null:   jsoncolor.Color("\x1b[2m"),
	Bytes:  jsoncolor.Color("\x1b[2m"),
	Time:   jsoncolor.Color("\x1b[32;2m"),
	Punc:   jsoncolor.Color("\x1b[2m"),
}

// NewJSONEncoder returns a streaming JSON encoder writing to w. When colored is
// set it syntax-highlights and sorts map keys (so the colored order matches the
// stdlib encoder's default); otherwise it is the stdlib encoder, byte-for-byte
// the pre-color output. It never HTML-escapes — the sink is a terminal, not a
// web page. indent is the per-level indentation ("" for compact) and prefix is
// prepended to every line after the first, nesting a value inside an array.
func NewJSONEncoder(w io.Writer, prefix, indent string, colored bool) Encoder {
	if colored {
		enc := jsoncolor.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetSortMapKeys(true)
		enc.SetColors(jsonColors)
		if indent != "" {
			enc.SetIndent(prefix, indent)
		}
		return enc
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent(prefix, indent)
	}
	return enc
}

// JSON renders v as pretty (two-space indented) JSON with the trailing newline
// trimmed, colored when colored is set. It is the string-returning form for a
// raw reply body.
func JSON(v any, colored bool) (string, error) {
	var buf bytes.Buffer
	if err := NewJSONEncoder(&buf, "", "  ", colored).Encode(v); err != nil {
		return "", fmt.Errorf("encode json: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
