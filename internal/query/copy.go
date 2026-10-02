package query

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/itchyny/gojq"
)

// ErrNoKey is returned when a record reaches the write boundary without a key —
// a transform emitted more than one value per source item with no --key to key
// them by, or foreign input carried no key field.
var ErrNoKey = errors.New("record has no key")

// RecordSource walks records in pages, the read side a Copier consumes. A store
// adapter supplies TypedReader.TypedScan directly; a file reader supplies a
// closure over the decoded stream, so a copy from a source and from a file share
// one path.
type RecordSource func(ctx context.Context, fn func(batch []Record) error) error

// Copier moves records from a RecordSource into a Putter in bounded pages,
// applying an optional per-record Transform (the item filter and --key logic)
// between them. It accumulates a WriteStat and streams, so memory stays O(page).
// Writes are non-atomic across keys — the WriteStat reports honestly rather
// than pretending all-or-nothing.
type Copier struct {
	Dst       Putter
	Mode      WriteMode
	PageSize  int
	Transform func(Record) ([]Record, error) // nil means identity
}

// Copy streams src through the Transform into Dst and returns the totalled stat.
// A Transform error, a store error, or a record with no key stops the copy and is
// returned with context. When dry is true nothing is written: the records are
// still read and transformed (so the reported counts are real), but every batch
// is counted as would-be writes instead of being sent to Dst.
func (c *Copier) Copy(ctx context.Context, src RecordSource, dry bool) (WriteStat, error) {
	size := c.PageSize
	if size <= 0 {
		size = 100
	}
	pages := &copyPages{dst: c.Dst, mode: c.Mode, dry: dry, size: size, buf: make([]Record, 0, size)}
	err := src(ctx, func(batch []Record) error {
		for _, r := range batch {
			outs, err := c.transform(r)
			if err != nil {
				return err
			}
			// A keyless record is not rejected here: a schemaless destination
			// (MongoDB) can mint an identity, while a keyed one (Redis) rejects it
			// at its own Put. The Copier stays driver-agnostic.
			if err := pages.add(ctx, outs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return pages.total, err
	}
	if err := pages.flush(ctx); err != nil {
		return pages.total, err
	}
	return pages.total, nil
}

// transform maps one source record to its destination records. A nil Transform is
// the identity.
func (c *Copier) transform(r Record) ([]Record, error) {
	if c.Transform == nil {
		return []Record{r}, nil
	}
	return c.Transform(r)
}

// copyPages buffers records into pages and writes each full page to dst, or only
// counts it in a dry run.
type copyPages struct {
	dst   Putter
	mode  WriteMode
	dry   bool
	size  int
	buf   []Record
	total WriteStat
}

// add buffers recs and writes each page that fills up.
func (p *copyPages) add(ctx context.Context, recs []Record) error {
	for _, r := range recs {
		p.buf = append(p.buf, r)
		if len(p.buf) >= p.size {
			if err := p.flush(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// flush writes the buffered page. The stat of a failed Put still counts, and the
// buffer is empty when the error returns.
func (p *copyPages) flush(ctx context.Context) error {
	if len(p.buf) == 0 {
		return nil
	}
	if p.dry {
		// A dry run reports what would be written without touching the store; it
		// cannot know overwrite-vs-insert without writing, so it counts intent.
		p.total.Written += len(p.buf)
		p.buf = p.buf[:0]
		return nil
	}
	st, err := p.dst.Put(ctx, p.buf, p.mode)
	p.total.add(st)
	p.buf = p.buf[:0]
	return err
}

// TransformOptions configures a per-record transform for a copy. All fields are
// optional; the zero value is the identity transform (inherit key, type, value).
type TransformOptions struct {
	Filter    string // jq expression applied to each value; "" is identity
	Key       string // jq expression yielding each output's key; "" inherits
	KeyField  string // object field to take the key from; "" is unused
	KeyPrefix string // prepended to every derived/inherited key
	Type      string // Type stamped on a reshaped output; "" leaves it unset
}

// empty reports whether opts requests no transformation at all, so the caller can
// skip building a Transform (and gojq) entirely for a plain copy.
func (o TransformOptions) empty() bool {
	return o == TransformOptions{}
}

// NewTransform compiles opts into a record transform. It fails fast on a bad jq
// expression. The returned func maps one source record to its destination
// records, applying these keying rules:
//   - no Filter: identity value, Type preserved, key = source key (or KeyField).
//   - Filter set, one output: key = source key unless Key/KeyField overrides.
//   - Filter set, many outputs: each needs its own key, so Key or KeyField is
//     required; otherwise it is ErrNoKey (a single source key cannot cover them).
//
// A reshaping Filter stamps Type from opts.Type (may be empty; a typed
// destination then rejects the record, so the ambiguity surfaces at the edge that
// knows it needs a type).
func NewTransform(opts TransformOptions) (func(Record) ([]Record, error), error) {
	if opts.empty() {
		return nil, nil
	}
	filter, err := compileJQ(opts.Filter, "item filter")
	if err != nil {
		return nil, err
	}
	key, err := compileJQ(opts.Key, "--key")
	if err != nil {
		return nil, err
	}
	return transform{opts: opts, filter: filter, key: key}.apply, nil
}

// compileJQ parses and compiles one jq expression. An empty source gives a nil
// program. label names the expression in the error.
func compileJQ(src, label string) (*gojq.Code, error) {
	if src == "" {
		return nil, nil
	}
	q, err := gojq.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", label, err)
	}
	return code, nil
}

// transform is a compiled TransformOptions: the item filter and the key expression,
// each nil when its option is empty.
type transform struct {
	opts   TransformOptions
	filter *gojq.Code
	key    *gojq.Code
}

// apply maps one source record to its destination records.
func (t transform) apply(r Record) ([]Record, error) {
	values, err := applyFilter(t.filter, r.Value)
	if err != nil {
		return nil, err
	}
	reshaped := t.filter != nil
	out := make([]Record, 0, len(values))
	for _, v := range values {
		key, err := t.keyFor(r.Key, v, len(values) == 1)
		if err != nil {
			return nil, err
		}
		rec := Record{Key: key, Value: v, Type: r.Type}
		if reshaped {
			rec.Type = t.opts.Type // a reshaped value's original type no longer applies.
		}
		out = append(out, rec)
	}
	return out, nil
}

// applyFilter runs code over one value and collects its outputs. A nil code is
// the identity (the value passes through unchanged). A jq runtime error stops the
// copy — a filter that fails on one document must not silently drop it.
func applyFilter(code *gojq.Code, value any) ([]any, error) {
	if code == nil {
		return []any{value}, nil
	}
	iter := code.Run(value)
	var out []any
	for {
		v, ok := iter.Next()
		if !ok {
			return out, nil
		}
		if err, ok := v.(error); ok {
			return nil, fmt.Errorf("apply item filter: %w", err)
		}
		out = append(out, v)
	}
}

// keyFor resolves the destination key for one output value under the keying
// rules. single reports whether the value is the sole output of its source item,
// which is what lets a 1:1 transform inherit the source key.
func (t transform) keyFor(sourceKey string, value any, single bool) (string, error) {
	var key string
	switch {
	case t.key != nil:
		k, err := runKey(t.key, value)
		if err != nil {
			return "", err
		}
		key = k
	case t.opts.KeyField != "":
		k, err := keyFromField(value, t.opts.KeyField)
		if err != nil {
			return "", err
		}
		key = k
	case single:
		key = sourceKey
	default:
		return "", fmt.Errorf("%w: the item filter emits multiple values per item; add --key or --key-field", ErrNoKey)
	}
	if key == "" {
		return "", ErrNoKey
	}
	return t.opts.KeyPrefix + key, nil
}

// runKey evaluates a --key expression over one output value, requiring exactly
// one scalar result so a key is unambiguous.
func runKey(code *gojq.Code, value any) (string, error) {
	iter := code.Run(value)
	v, ok := iter.Next()
	if !ok {
		return "", fmt.Errorf("--key produced no value")
	}
	if err, ok := v.(error); ok {
		return "", fmt.Errorf("apply --key: %w", err)
	}
	if _, ok := iter.Next(); ok {
		return "", fmt.Errorf("--key produced more than one value")
	}
	return scalarKey(v)
}

// keyFromField extracts a key from an object field, the shape a foreign JSON
// record is keyed by. A non-object or a missing field is a clear error.
func keyFromField(value any, field string) (string, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("--key-field %q: value is not an object", field)
	}
	v, ok := obj[field]
	if !ok {
		return "", fmt.Errorf("--key-field %q: field absent", field)
	}
	return scalarKey(v)
}

// scalarKey renders a jq scalar as a key string, refusing a composite value so a
// key is never a stringified object or array.
func scalarKey(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case *big.Int:
		return t.String(), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	default:
		return "", fmt.Errorf("key must be a scalar, got %T", v)
	}
}
