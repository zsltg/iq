package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zsltg/iq/internal/parquetout"
	"github.com/zsltg/iq/internal/render"
)

// outputFormat is the closed set of renderings the output-format flags select.
// A closed type keeps an unknown format unrepresentable past selectFormat.
type outputFormat string

const (
	formatJSON      outputFormat = "json"
	formatJSONL     outputFormat = "jsonl"
	formatJSONArray outputFormat = "jsona"
	formatValues    outputFormat = "values"
	formatYAML      outputFormat = "yaml"
	formatGron      outputFormat = "gron"
	formatGronArray outputFormat = "grona"
	formatParquet   outputFormat = "parquet"
)

// namedFormats maps the --format flag's value names to the rendering they select.
// The names match the shorthand boolean flags, plus "raw" as an alias for
// "values" (mirroring the --raw boolean, which selects formatValues).
var namedFormats = map[string]outputFormat{
	"json":    formatJSON,
	"jsonl":   formatJSONL,
	"jsona":   formatJSONArray,
	"yaml":    formatYAML,
	"values":  formatValues,
	"raw":     formatValues,
	"gron":    formatGron,
	"grona":   formatGronArray,
	"parquet": formatParquet,
}

// namedFormat resolves a --format value to its rendering, case-insensitively and
// trimming surrounding whitespace, reporting whether the name is known.
func namedFormat(s string) (outputFormat, bool) {
	f, ok := namedFormats[strings.ToLower(strings.TrimSpace(s))]
	return f, ok
}

// validateFormat rejects an unknown --format value at PreRun (empty means unset),
// mirroring the fail-fast posture of validateErrorFormat.
func validateFormat(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, ok := namedFormat(s); !ok {
		return fmt.Errorf("invalid --format %q: want json, jsonl, jsona, yaml, values, gron, grona, parquet, or raw", s)
	}
	return nil
}

// selectFormat resolves the chosen rendering from the mutually exclusive
// output-format flags, defaulting to pretty json when none is set (--raw selects
// the values rendering, mirroring jq's --raw-output). Cobra marks the flags
// mutually exclusive, so at most one is set on the real CLI; the >1 guard keeps
// the invariant enforced when a config is built directly (tests) and lets both
// run paths validate before any store I/O.
func selectFormat(cfg *config) (outputFormat, error) {
	set := make([]outputFormat, 0, 1)
	if cfg.format != "" {
		f, ok := namedFormat(cfg.format)
		if !ok {
			return "", fmt.Errorf("invalid --format %q: want json, jsonl, jsona, yaml, values, gron, grona, parquet, or raw", cfg.format)
		}
		set = append(set, f)
	}
	if cfg.json {
		set = append(set, formatJSON)
	}
	if cfg.jsonArray {
		set = append(set, formatJSONArray)
	}
	if cfg.jsonl {
		set = append(set, formatJSONL)
	}
	if cfg.yaml {
		set = append(set, formatYAML)
	}
	if cfg.raw {
		set = append(set, formatValues)
	}
	if cfg.gron {
		set = append(set, formatGron)
	}
	if cfg.gronArray {
		set = append(set, formatGronArray)
	}
	switch len(set) {
	case 0:
		return formatJSON, nil
	case 1:
		return set[0], nil
	default:
		return "", fmt.Errorf("output format flags are mutually exclusive: set at most one of --format, --json, --jsona, --jsonl, --yaml, --raw, --gron, --grona")
	}
}

// binaryTTYCheck reports whether the destination is an interactive terminal. It
// is a package var so tests can force the terminal branch, mirroring the
// stdinIsTerminal seam.
var binaryTTYCheck = isTerminalWriter

// guardBinaryFormat refuses to write a binary format to an interactive terminal,
// where the bytes would corrupt the screen. Parquet is the only binary format;
// the check runs on the raw destination (before any progress-meter wrapping), so
// a redirect or pipe passes. Redirecting to a file or piping is the fix.
func guardBinaryFormat(f outputFormat, w io.Writer) error {
	if f == formatParquet && binaryTTYCheck(w) {
		return errors.New("refusing to write parquet to the terminal: redirect it to a file (iq '<filter>' --format parquet > out.parquet) or pipe it")
	}
	return nil
}

// formatter renders each value the query engine emits and, on flush, completes
// the output. Most formatters stream and flush is a no-op; jsona closes its
// bracket and yaml closes its encoder, so callers must flush once the engine has
// finished.
type formatter interface {
	emit(v any) error
	flush() error
}

// newFormatter builds the formatter for f, writing to w. When compact is set,
// the pretty renderings (json, jsona) collapse to single-line output; the
// already-condensed formats (jsonl, values, yaml, gron, grona) ignore it.
func newFormatter(f outputFormat, w io.Writer, compact bool) formatter {
	switch f {
	case formatJSONL:
		return &jsonFormatter{enc: newJSONEncoder(w, false)}
	case formatJSONArray:
		return &jsonArrayFormatter{w: w, compact: compact}
	case formatValues:
		return &valuesFormatter{w: w}
	case formatGron:
		return &gronFormatter{w: w}
	case formatGronArray:
		return &gronFormatter{w: w, indexed: true}
	case formatParquet:
		return &parquetFormatter{pw: parquetout.NewWriter(w)}
	case formatYAML:
		if colorOn() {
			return &colorYAMLFormatter{w: w}
		}
		return &yamlFormatter{enc: yaml.NewEncoder(w)}
	default: // formatJSON
		return &jsonFormatter{enc: newJSONEncoder(w, !compact)}
	}
}

// newJSONEncoder builds a JSON encoder that never HTML-escapes (<, >, and &
// stay verbatim; the sink is a terminal, not a web page). It indents two spaces
// when pretty is set, and otherwise emits one compact value per line. Coloring
// follows the invocation's color mode via render, which returns the plain stdlib
// encoder when color is off, so uncolored output is byte-for-byte unchanged.
func newJSONEncoder(w io.Writer, pretty bool) render.Encoder {
	indent := ""
	if pretty {
		indent = "  "
	}
	return render.NewJSONEncoder(w, "", indent, colorOn())
}

// writeStructured renders a single value as pretty JSON (the default) or as YAML
// when yamlOut is set, for the info commands (ls, inspect, diff, driver ls,
// config keyring ls). It reuses the query path's encoders so color and formatting
// stay consistent; callers invoke it only in structured mode (-j or -y), which
// Cobra marks mutually exclusive.
func writeStructured(w io.Writer, v any, yamlOut bool) error {
	format := formatJSON
	if yamlOut {
		format = formatYAML
	}
	f := newFormatter(format, w, false)
	return finish(f, f.emit(v))
}

// jsonFormatter streams one JSON value per Encode call: pretty for json, compact
// (one value per line) for jsonl. Both reproduce the prior output byte for byte.
type jsonFormatter struct{ enc render.Encoder }

func (f *jsonFormatter) emit(v any) error {
	if err := f.enc.Encode(v); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}

func (f *jsonFormatter) flush() error { return nil }

// jsonArrayFormatter streams a single JSON array wrapping every emitted value,
// pretty by default and single-line when compact is set. It writes incrementally,
// tracking whether an element has been written so it can place separators; flush
// closes the array, emitting [] for an empty result.
type jsonArrayFormatter struct {
	w       io.Writer
	compact bool
	started bool
}

func (f *jsonArrayFormatter) emit(v any) error {
	render, open, sep := indentElem, "[\n", ",\n"
	if f.compact {
		render, open, sep = compactElem, "[", ","
	}
	elem, err := render(v)
	if err != nil {
		return err
	}
	if !f.started {
		sep = open
		f.started = true
	}
	if _, err := io.WriteString(f.w, sep+elem); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

func (f *jsonArrayFormatter) flush() error {
	tail := "\n]\n"
	if f.compact {
		tail = "]\n"
	}
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
	elem, err := encodeElem(v, true)
	if err != nil {
		return "", err
	}
	return "  " + elem, nil
}

// compactElem renders v as a single-line JSON element for a compact array,
// without HTML escaping and with the encoder's trailing newline trimmed. It is
// the pretty-less twin of indentElem.
func compactElem(v any) (string, error) {
	return encodeElem(v, false)
}

// encodeElem renders v as a JSON element with the trailing newline trimmed,
// two-space nested indentation when indent is set. It colors the value when
// color is on and emits plain JSON otherwise, so the surrounding array
// punctuation (written by jsonArrayFormatter) stays uncolored.
func encodeElem(v any, indent bool) (string, error) {
	prefix, step := "", ""
	if indent {
		prefix, step = "  ", "  "
	}
	var buf bytes.Buffer
	if err := render.NewJSONEncoder(&buf, prefix, step, colorOn()).Encode(v); err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// valuesFormatter prints jq scalars unquoted, one per line, for shell
// substitution. A composite value (object or array) has no bare form, so it
// falls back to compact JSON on its line; nothing is lost. Bare strings and
// nulls always print uncolored (that bareness is the format's contract), but the
// composite fallback and non-string scalars follow the invocation's color mode,
// like jq -r.
type valuesFormatter struct {
	w   io.Writer
	buf bytes.Buffer
	enc render.Encoder
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
// buffer and encoder across calls and trimming the trailing newline. The encoder
// syntax-highlights when the invocation's color mode is on and is the plain
// stdlib encoder otherwise, so color-off output stays byte-identical.
func (f *valuesFormatter) compact(v any) (string, error) {
	f.buf.Reset()
	if f.enc == nil {
		f.enc = render.NewJSONEncoder(&f.buf, "", "", colorOn())
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

// colorYAMLFormatter is the colored twin of yamlFormatter. It encodes each value
// to a single document with yaml.v3 (so the rendering matches the plain path),
// colorizes that document's tokens, and writes it, prefixing the `---` document
// separator for every document after the first. It buffers one document at a
// time, so a stream renders in bounded memory like the plain encoder.
type colorYAMLFormatter struct {
	w       io.Writer
	started bool
}

func (f *colorYAMLFormatter) emit(v any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close yaml: %w", err)
	}
	doc := colorizeYAML(buf.String())
	if f.started {
		doc = "---\n" + doc
	}
	f.started = true
	if _, err := io.WriteString(f.w, doc); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

func (f *colorYAMLFormatter) flush() error { return nil }

// parquetFormatter streams the emitted values to Parquet. Unlike the text
// formatters it buffers a leading sample to infer the schema, then writes one
// record batch per page in bounded memory; flush writes the final page and the
// file footer, so callers must flush once the engine has finished. The heavy
// lifting lives in internal/parquetout, keeping the Arrow dependency out of the
// core query path.
type parquetFormatter struct{ pw *parquetout.Writer }

func (f *parquetFormatter) emit(v any) error {
	if err := f.pw.Add(v); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}

func (f *parquetFormatter) flush() error { return f.pw.Close() }
