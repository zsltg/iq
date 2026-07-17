package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// gronIdent matches a key that can follow a bare dot (`.key`) in a gron path.
// It is a deliberate ASCII subset of gron's unicode rule: over-quoting is always
// ungron-safe, and JS reserved words stay bare (ungron accepts them).
var gronIdent = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// gronFormatter flattens each emitted value into gron assignment statements
// (https://github.com/tomnomnom/gron), one `path = <compact JSON>;` per line:
// greppable line-by-line and reversible with ungron. Plain gron roots every
// result at a repeated `json` (ungron is last-write-wins across results); the
// indexed variant (grona) roots result N at json[N] and prefixes a leading
// `json = [];` declaration, so the whole stream ungrons back to one array.
type gronFormatter struct {
	w       io.Writer
	indexed bool // grona: root result N at json[N] instead of repeating json.
	n       int  // results emitted (grona root index / declaration latch).
	buf     bytes.Buffer
	enc     *json.Encoder
}

// emit flattens v into gron statements. Plain gron roots at json; grona writes a
// single leading `json = [];` declaration before the first result, then roots
// result N at json[N].
func (f *gronFormatter) emit(v any) error {
	if !f.indexed {
		return f.walk("json", v)
	}
	if f.n == 0 {
		if err := f.line("json", "[]"); err != nil {
			return err
		}
	}
	if err := f.walk(fmt.Sprintf("json[%d]", f.n), v); err != nil {
		return err
	}
	f.n++
	return nil
}

// flush completes the stream. Plain gron streams fully, so flush is a no-op;
// grona with zero emits writes the leading `json = [];` so an empty stream
// ungrons to [] (mirroring an empty --jsona rendering []).
func (f *gronFormatter) flush() error {
	if f.indexed && f.n == 0 {
		return f.line("json", "[]")
	}
	return nil
}

// walk writes the gron statements for v at path, depth-first and parent-before-
// children: an object emits its `= {};` declaration then recurses into sorted
// keys, an array emits `= [];` then recurses by index, and any other value emits
// `path = <compact JSON>;`. An empty container emits only its declaration; a
// top-level nil yields `path = null;`.
func (f *gronFormatter) walk(path string, v any) error {
	switch t := v.(type) {
	case map[string]any:
		if err := f.line(path, "{}"); err != nil {
			return err
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := f.walk(path+f.key(k), t[k]); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if err := f.line(path, "[]"); err != nil {
			return err
		}
		for i, e := range t {
			if err := f.walk(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
		return nil
	default:
		rhs, err := f.compact(v)
		if err != nil {
			return err
		}
		return f.line(path, rhs)
	}
}

// key renders an object key as a path segment: a bare `.key` when it is an ASCII
// identifier, else a bracketed `["<JSON-escaped>"]`. compact keeps HTML-escaping
// off, so a key like a<b stays verbatim like every other formatter.
func (f *gronFormatter) key(k string) string {
	if gronIdent.MatchString(k) {
		return "." + k
	}
	quoted, err := f.compact(k)
	if err != nil {
		// A string always JSON-encodes; fall back to a Go-quoted form defensively.
		quoted = strconv.Quote(k)
	}
	return "[" + quoted + "]"
}

// compact renders v as single-line JSON without HTML escaping, reusing one
// buffer and encoder across calls and trimming the trailing newline (so a
// *big.Int renders bare). It mirrors valuesFormatter.compact.
func (f *gronFormatter) compact(v any) (string, error) {
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

// line writes one `path = rhs;` statement followed by a newline.
func (f *gronFormatter) line(path, rhs string) error {
	if _, err := fmt.Fprintf(f.w, "%s = %s;\n", path, rhs); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}
