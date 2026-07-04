package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// outputFormat is the closed set of renderings the --format flag selects. A
// closed type keeps an unknown format unrepresentable past parseFormat.
type outputFormat string

const (
	formatJSON      outputFormat = "json"
	formatJSONL     outputFormat = "jsonl"
	formatJSONArray outputFormat = "json-array"
	formatValues    outputFormat = "values"
	formatYAML      outputFormat = "yaml"
)

// parseFormat validates the --format value at the boundary, returning a closed
// outputFormat or an error naming every valid choice.
func parseFormat(s string) (outputFormat, error) {
	switch outputFormat(s) {
	case formatJSON, formatJSONL, formatJSONArray, formatValues, formatYAML:
		return outputFormat(s), nil
	default:
		return "", fmt.Errorf("unknown output format %q: want one of json, jsonl, json-array, values, yaml", s)
	}
}

// formatter renders each value the query engine emits and, on flush, completes
// the output. Most formatters stream and flush is a no-op; json-array closes its
// bracket and yaml closes its encoder, so callers must flush once the engine has
// finished.
type formatter interface {
	emit(v any) error
	flush() error
}

// newFormatter builds the formatter for f, writing to w.
func newFormatter(f outputFormat, w io.Writer) formatter {
	switch f {
	case formatJSONL:
		return &jsonFormatter{enc: newJSONEncoder(w, false)}
	case formatJSONArray:
		return &jsonArrayFormatter{w: w}
	case formatValues:
		return &valuesFormatter{w: w}
	case formatYAML:
		return &yamlFormatter{enc: yaml.NewEncoder(w)}
	default: // formatJSON
		return &jsonFormatter{enc: newJSONEncoder(w, true)}
	}
}

// newJSONEncoder builds a JSON encoder that never HTML-escapes (<, >, and &
// stay verbatim; the sink is a terminal, not a web page). It indents two spaces
// when pretty is set, and otherwise emits one compact value per line.
func newJSONEncoder(w io.Writer, pretty bool) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc
}

// jsonFormatter streams one JSON value per Encode call: pretty for json, compact
// (one value per line) for jsonl. Both reproduce the prior output byte for byte.
type jsonFormatter struct{ enc *json.Encoder }

func (f *jsonFormatter) emit(v any) error {
	if err := f.enc.Encode(v); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}

func (f *jsonFormatter) flush() error { return nil }

// jsonArrayFormatter streams a single pretty JSON array wrapping every emitted
// value. It writes incrementally, tracking whether an element has been written so
// it can place separators; flush closes the array, emitting [] for an empty
// result.
type jsonArrayFormatter struct {
	w       io.Writer
	started bool
}

func (f *jsonArrayFormatter) emit(v any) error {
	elem, err := indentElem(v)
	if err != nil {
		return err
	}
	sep := ",\n"
	if !f.started {
		sep = "[\n"
		f.started = true
	}
	if _, err := io.WriteString(f.w, sep+elem); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

func (f *jsonArrayFormatter) flush() error {
	tail := "\n]\n"
	if !f.started {
		tail = "[]\n"
	}
	if _, err := io.WriteString(f.w, tail); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// indentElem renders v as a pretty JSON element sitting one level inside an
// array: SetIndent's prefix indents every line but the first, so the opening
// line is indented by hand.
func indentElem(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("  ", "  ")
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return "  " + strings.TrimRight(buf.String(), "\n"), nil
}

// valuesFormatter prints jq scalars unquoted, one per line, for shell
// substitution. A composite value (object or array) has no bare form, so it
// falls back to compact JSON on its line; nothing is lost.
type valuesFormatter struct {
	w   io.Writer
	buf bytes.Buffer
	enc *json.Encoder
}

func (f *valuesFormatter) emit(v any) error {
	switch t := v.(type) {
	case string:
		return f.line(t)
	case nil:
		return f.line("")
	default:
		s, err := f.compact(t)
		if err != nil {
			return err
		}
		return f.line(s)
	}
}

func (f *valuesFormatter) flush() error { return nil }

// compact renders v as single-line JSON without HTML escaping, reusing one
// buffer and encoder across calls and trimming the trailing newline.
func (f *valuesFormatter) compact(v any) (string, error) {
	f.buf.Reset()
	if f.enc == nil {
		f.enc = json.NewEncoder(&f.buf)
		f.enc.SetEscapeHTML(false)
	}
	if err := f.enc.Encode(v); err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return strings.TrimRight(f.buf.String(), "\n"), nil
}

// line writes s followed by a newline.
func (f *valuesFormatter) line(s string) error {
	if _, err := fmt.Fprintln(f.w, s); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// yamlFormatter writes each value as a YAML document; yaml.v3 separates the
// documents with `---`, so a stream renders as a valid multi-document file.
type yamlFormatter struct{ enc *yaml.Encoder }

func (f *yamlFormatter) emit(v any) error {
	if err := f.enc.Encode(v); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}

// flush closes the encoder, writing any buffered document; the output is not
// complete until it runs.
func (f *yamlFormatter) flush() error {
	if err := f.enc.Close(); err != nil {
		return fmt.Errorf("close yaml: %w", err)
	}
	return nil
}
