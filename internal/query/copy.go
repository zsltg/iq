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
	page := c.PageSize
	if page <= 0 {
		page = 100
	}
	var total WriteStat
	buf := make([]Record, 0, page)

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if dry {
			// A dry run reports what would be written without touching the store; it
			// cannot know overwrite-vs-insert without writing, so it counts intent.
			total.Written += len(buf)
			buf = buf[:0]
			return nil
		}
		st, err := c.Dst.Put(ctx, buf, c.Mode)
		total.add(st)
		buf = buf[:0]
		if err != nil {
			return err
		}
		return nil
	}

	err := src(ctx, func(batch []Record) error {
		for _, r := range batch {
			outs := []Record{r}
			if c.Transform != nil {
				var terr error
				if outs, terr = c.Transform(r); terr != nil {
					return terr
				}
			}
			for _, o := range outs {
				// A keyless record is not rejected here: a schemaless destination
				// (MongoDB) can mint an identity, while a keyed one (Redis) rejects it
				// at its own Put. The Copier stays driver-agnostic.
				buf = append(buf, o)
				if len(buf) >= page {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return total, err
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
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
	var filterCode, keyCode *gojq.Code
	if opts.Filter != "" {
		q, err := gojq.Parse(opts.Filter)
		if err != nil {
			return nil, fmt.Errorf("parse item filter: %w", err)
		}
		if filterCode, err = gojq.Compile(q); err != nil {
			return nil, fmt.Errorf("compile item filter: %w", err)
		}
	}
	if opts.Key != "" {
		q, err := gojq.Parse(opts.Key)
		if err != nil {
			return nil, fmt.Errorf("parse --key: %w", err)
		}
		if keyCode, err = gojq.Compile(q); err != nil {
			return nil, fmt.Errorf("compile --key: %w", err)
		}
	}

	return func(r Record) ([]Record, error) {
		values, err := applyFilter(filterCode, r.Value)
		if err != nil {
			return nil, err
		}
		reshaped := filterCode != nil
		out := make([]Record, 0, len(values))
		for _, v := range values {
			key, err := deriveKey(opts, keyCode, r.Key, v, len(values) == 1)
			if err != nil {
				return nil, err
			}
			rec := Record{Key: key, Value: v, Type: r.Type}
			if reshaped {
				rec.Type = opts.Type // a reshaped value's original type no longer applies.
			}
			out = append(out, rec)
		}
		return out, nil
	}, nil
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

// deriveKey resolves the destination key for one output value under the keying
// rules. single reports whether the value is the sole output of its source item,
// which is what lets a 1:1 transform inherit the source key.
func deriveKey(opts TransformOptions, keyCode *gojq.Code, sourceKey string, value any, single bool) (string, error) {
	var key string
	if keyCode != nil {
		k, err := runKey(keyCode, value)
		if err != nil {
			return "", err
		}
		key = k
	} else if opts.KeyField != "" {
		k, err := keyFromField(value, opts.KeyField)
		if err != nil {
			return "", err
		}
		key = k
	} else if single {
		key = sourceKey
	} else {
		return "", fmt.Errorf("%w: the item filter emits multiple values per item; add --key or --key-field", ErrNoKey)
	}
	if key == "" {
		return "", ErrNoKey
	}
	return opts.KeyPrefix + key, nil
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
