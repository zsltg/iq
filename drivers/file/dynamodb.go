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
// {"Item":{…}} object per line) or `aws dynamodb scan`/`query` output (a {"Items":[…]}
// object). Both carry items as typed attribute-value JSON, which is decoded and
// normalized to the exact shape a live scan produces (see drivers/dynamodb), keyed by
// the primary key the ?keys= hint names. A dump records items but not the key schema,
// so ?keys= is required — without it items cannot be keyed and would collide.
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
		// export line ({"Item":{…}}); decode them in sequence so a single reader handles
		// a scan file, a multi-page scan (several wrappers), and NDJSON export alike.
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var obj map[string]json.RawMessage
			if err := jd.Decode(&obj); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("decode dynamodb dump: %w", err)
			}
			if err := emitObject(obj, emit); err != nil {
				return err
			}
		}
		return flush()
	}, nil
}

// emitObject dispatches one decoded top-level object to emit: the elements of an
// "Items" array (scan/query output) or the single "Item" (an export line). An object
// with neither is a clear error rather than a silent skip, so a wrong ?format= or an
// unexpected shape fails fast.
func emitObject(obj map[string]json.RawMessage, emit func(map[string]types.AttributeValue) error) error {
	if raw, ok := obj["Items"]; ok {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("decode dynamodb Items: %w", err)
		}
		for _, it := range items {
			item, err := iqddb.ParseItem(it)
			if err != nil {
				return err
			}
			if err := emit(item); err != nil {
				return err
			}
		}
		return nil
	}
	if raw, ok := obj["Item"]; ok {
		item, err := iqddb.ParseItem(raw)
		if err != nil {
			return err
		}
		return emit(item)
	}
	return errors.New("dynamodb dump object has neither \"Item\" nor \"Items\"; expected an S3 export or scan output")
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
