package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	iqddb "github.com/zsltg/iq/drivers/dynamodb"
	"github.com/zsltg/iq/internal/numfmt"
	"github.com/zsltg/iq/internal/query"
)

// dynamoSource streams a DynamoDB native dump: a table's S3 export (NDJSON, one
// {"Item":{…}} object per line) or `aws dynamodb scan`/`query` output (a
// {"Items":[…]} object). Both carry items as typed attribute-value JSON. Each
// item is decoded and normalized to the exact shape that a live scan produces
// (see drivers/dynamodb). It is keyed by the primary key that the ?keys= hint
// names. A dump records items but not the key schema, so ?keys= is required.
// Without it, items cannot be keyed, so they collide.
func dynamoSource(r io.Reader, size int, dec numfmt.DecimalMode, hints Hints) (query.RecordSource, error) {
	if hints.Keys == "" {
		return nil, errors.New("dynamodb dump needs a key schema: add ?keys=pk[:S][,sk[:N]] to the file url")
	}
	keys, err := iqddb.ParseKeySchema(hints.Keys)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, fn func(batch []query.Record) error) error {
		jd := json.NewDecoder(r)
		page := make([]query.Record, 0, size)
		flush := func() error {
			if len(page) == 0 {
				return nil
			}
			if err := fn(page); err != nil {
				return err
			}
			page = page[:0]
			return nil
		}
		emit := func(item map[string]types.AttributeValue) error {
			page = append(page, recordForItem(keys, item, dec))
			if len(page) >= size {
				return flush()
			}
			return nil
		}
		// Each top-level object is either a scan/query wrapper ({"Items":[…]}) or one
		// export line ({"Item":{…}}). Read them in sequence, so a single reader
		// handles a scan file, a multi-page scan (several wrappers), and NDJSON
		// export alike. The reader never holds more than one item at a time.
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := readObject(ctx, jd, emit)
			if err == io.EOF { //nolint:errorlint // readObject returns a bare io.EOF only at a clean end between objects.
				break
			}
			if err != nil {
				return err
			}
		}
		return flush()
	}, nil
}

// errItemsNotArray reports an "Items" value that is not a JSON array.
var errItemsNotArray = errors.New("value is not an array")

// readObject reads one top-level object from jd, key by key, and passes each item
// to emit: the elements of an "Items" array (scan/query output) or the single
// "Item" (an export line). Other keys, such as "Count", are skipped. An object
// with neither key is a clear error rather than a silent skip, so a wrong ?format=
// or an unexpected shape fails fast.
func readObject(ctx context.Context, jd *json.Decoder, emit func(map[string]types.AttributeValue) error) error {
	if err := openObject(jd); err != nil {
		return err
	}
	found, err := readMembers(ctx, jd, emit)
	if err != nil {
		return err
	}
	if !found {
		return errNoItems
	}
	return nil
}

// openObject reads the '{' that starts a top-level object. It returns a bare
// io.EOF at a clean end of the input.
func openObject(jd *json.Decoder) error {
	tok, err := jd.Token()
	if err == io.EOF { //nolint:errorlint // the decoder returns a bare io.EOF only at a clean end. A wrapped one is a reader failure.
		return io.EOF
	}
	if err != nil {
		return fmt.Errorf("decode dynamodb dump: %w", err)
	}
	if tok == nil { // a bare null decodes to an empty object
		return errNoItems
	}
	if !isDelim(tok, '{') {
		return fmt.Errorf("decode dynamodb dump: %w", errNotObject)
	}
	return nil
}

// readMembers reads the keys of an open object up to its '}'. It reports whether
// any key held items.
func readMembers(ctx context.Context, jd *json.Decoder, emit func(map[string]types.AttributeValue) error) (bool, error) {
	found := false
	for jd.More() {
		key, err := jd.Token()
		if err != nil {
			return false, fmt.Errorf("decode dynamodb dump: %w", err)
		}
		hit, err := readMember(ctx, jd, key, emit)
		if err != nil {
			return false, err
		}
		found = found || hit
	}
	if _, err := jd.Token(); err != nil { // consume '}'
		return false, fmt.Errorf("decode dynamodb dump: %w", err)
	}
	return found, nil
}

// isDelim reports whether tok is the JSON delimiter want.
func isDelim(tok json.Token, want json.Delim) bool {
	d, ok := tok.(json.Delim)
	return ok && d == want
}

var (
	errNotObject = errors.New("top-level value is not an object")
	errNoItems   = errors.New("dynamodb dump object has neither \"Item\" nor \"Items\"; expected an S3 export or scan output")
)

// readMember reads the value of one object key. It reports whether the key
// held items ("Items" or "Item"). Any other value is read and dropped.
func readMember(ctx context.Context, jd *json.Decoder, key json.Token, emit func(map[string]types.AttributeValue) error) (bool, error) {
	switch key {
	case "Items":
		return true, readItems(ctx, jd, emit)
	case "Item":
		return true, readItem(jd, emit)
	}
	var skip json.RawMessage
	if err := jd.Decode(&skip); err != nil {
		return false, fmt.Errorf("decode dynamodb dump: %w", err)
	}
	return false, nil
}

// readItems decodes the "Items" array one element at a time.
func readItems(ctx context.Context, jd *json.Decoder, emit func(map[string]types.AttributeValue) error) error {
	tok, err := jd.Token()
	if err != nil {
		return fmt.Errorf("decode dynamodb Items: %w", err)
	}
	if tok == nil { // null is an empty list
		return nil
	}
	if !isDelim(tok, '[') {
		return fmt.Errorf("decode dynamodb Items: %w", errItemsNotArray)
	}
	if err := readElements(ctx, jd, emit); err != nil {
		return err
	}
	if _, err := jd.Token(); err != nil { // consume ']'
		return fmt.Errorf("decode dynamodb Items: %w", err)
	}
	return nil
}

// readElements decodes the elements of an open array, one at a time, up to its ']'.
func readElements(ctx context.Context, jd *json.Decoder, emit func(map[string]types.AttributeValue) error) error {
	for jd.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := readItem(jd, emit); err != nil {
			return err
		}
	}
	return nil
}

// readItem decodes one attribute-value item and passes it to emit.
func readItem(jd *json.Decoder, emit func(map[string]types.AttributeValue) error) error {
	var raw json.RawMessage
	if err := jd.Decode(&raw); err != nil {
		return fmt.Errorf("decode dynamodb dump: %w", err)
	}
	item, err := iqddb.ParseItem(raw)
	if err != nil {
		return err
	}
	return emit(item)
}

// recordForItem turns one decoded item into the record a live DynamoDB scan yields:
// keyed by its primary key, type "item", value each attribute normalized.
func recordForItem(keys []iqddb.KeyAttr, item map[string]types.AttributeValue, dec numfmt.DecimalMode) query.Record {
	val := make(map[string]any, len(item))
	for name, av := range item {
		val[name] = iqddb.Normalize(av, dec)
	}
	return query.Record{Key: iqddb.KeyOf(keys, item), Type: "item", Value: val}
}
