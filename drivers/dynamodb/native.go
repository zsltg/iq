package dynamodb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/zsltg/iq/internal/query"
)

// ErrUnknownAttribute reports an attribute value of a type that this driver does not know.
var ErrUnknownAttribute = errors.New("unsupported attribute type")

// The native form of this driver is query.FormDynamoJSON: one DynamoDB JSON item,
// where every attribute value is an object with one type tag, for example
// {"price": {"N": "1.50"}}. Unlike the normalized form, it keeps the number text,
// the binary type, and the set types. NativeScan reads it, Put writes it, and
// ParseItem is the shared parser of the offline dump reader.

// NativeScan streams the whole table as records in the native form. The key of a
// record is the same key that TypedScan gives. The value is the DynamoDB JSON of
// the item, as plain Go maps, slices, strings, and booleans, so a jq filter runs
// on it. The scan reads the table in pages and holds one page at a time.
func (s *Store) NativeScan(ctx context.Context, fn func(batch []query.Record) error) error {
	if len(s.keys) == 0 {
		return errNoTable
	}
	batch := &nativeBatch{size: s.pageSize, fn: fn}
	p := dynamodb.NewScanPaginator(s.client, &dynamodb.ScanInput{TableName: &s.table})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("dynamodb scan: %w", err)
		}
		if err := s.addNativeItems(batch, out.Items); err != nil {
			return err
		}
	}
	return batch.flush()
}

// addNativeItems adds the native record of each item to batch.
func (s *Store) addNativeItems(batch *nativeBatch, items []map[string]types.AttributeValue) error {
	for _, item := range items {
		rec, err := s.nativeRecord(item)
		if err != nil {
			return err
		}
		if err := batch.add(rec); err != nil {
			return err
		}
	}
	return nil
}

// nativeBatch collects records and hands them to fn in pages of size records.
type nativeBatch struct {
	size int
	fn   func(batch []query.Record) error
	page []query.Record
}

// add appends rec and hands the page to fn when the page is full.
func (b *nativeBatch) add(rec query.Record) error {
	b.page = append(b.page, rec)
	if len(b.page) < b.size {
		return nil
	}
	return b.emit()
}

// flush hands the last, partial page to fn.
func (b *nativeBatch) flush() error {
	if len(b.page) == 0 {
		return nil
	}
	return b.fn(b.page)
}

// emit hands the page to fn and starts a new page.
func (b *nativeBatch) emit() error {
	if err := b.fn(b.page); err != nil {
		return err
	}
	b.page = make([]query.Record, 0, b.size)
	return nil
}

// NativeForm names the form that NativeScan emits.
func (s *Store) NativeForm() query.Form { return query.FormDynamoJSON }

// WritesForm names the native form that Put accepts besides normalized JSON.
func (s *Store) WritesForm() query.Form { return query.FormDynamoJSON }

// nativeRecord builds the native record of one scanned item.
func (s *Store) nativeRecord(item map[string]types.AttributeValue) (query.Record, error) {
	key := s.keyOf(item)
	val, err := EncodeItem(item)
	if err != nil {
		return query.Record{}, fmt.Errorf("dynamodb: item %q: %w", key, err)
	}
	return query.Record{Key: key, Type: "item", Form: s.NativeForm(), Value: val}, nil
}

// EncodeItem renders an item as DynamoDB JSON. It is the inverse of ParseItem.
// The result holds only maps, slices, strings, and booleans. A set is a slice, so
// jq reads it. It fails on an attribute type that this version does not know.
func EncodeItem(item map[string]types.AttributeValue) (map[string]any, error) {
	out := make(map[string]any, len(item))
	for name, av := range item {
		enc, err := encodeAttribute(av)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", name, err)
		}
		out[name] = enc
	}
	return out, nil
}

// encodeAttribute renders one attribute value as a one-tag object.
func encodeAttribute(av types.AttributeValue) (map[string]any, error) {
	switch t := av.(type) {
	case *types.AttributeValueMemberM:
		m, err := EncodeItem(t.Value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"M": m}, nil
	case *types.AttributeValueMemberL:
		return encodeList(t.Value)
	default:
		return encodeScalar(av)
	}
}

// encodeScalar renders an S, N, B, BOOL, or NULL value. Any other type goes to
// encodeSet.
func encodeScalar(av types.AttributeValue) (map[string]any, error) {
	switch t := av.(type) {
	case *types.AttributeValueMemberS:
		return map[string]any{"S": t.Value}, nil
	case *types.AttributeValueMemberN:
		return map[string]any{"N": t.Value}, nil
	case *types.AttributeValueMemberB:
		return map[string]any{"B": base64.StdEncoding.EncodeToString(t.Value)}, nil
	case *types.AttributeValueMemberBOOL:
		return map[string]any{"BOOL": t.Value}, nil
	case *types.AttributeValueMemberNULL:
		return map[string]any{"NULL": t.Value}, nil
	default:
		return encodeSet(av)
	}
}

// encodeList renders an L value. Each element is a one-tag object.
func encodeList(list []types.AttributeValue) (map[string]any, error) {
	out := make([]any, len(list))
	for i, e := range list {
		enc, err := encodeAttribute(e)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		out[i] = enc
	}
	return map[string]any{"L": out}, nil
}

// encodeSet renders an SS, NS, or BS value. Any other type is an error, so a new
// attribute type is never dropped.
func encodeSet(av types.AttributeValue) (map[string]any, error) {
	switch t := av.(type) {
	case *types.AttributeValueMemberSS:
		return map[string]any{"SS": stringsToAny(t.Value)}, nil
	case *types.AttributeValueMemberNS:
		return map[string]any{"NS": stringsToAny(t.Value)}, nil
	case *types.AttributeValueMemberBS:
		enc := make([]string, len(t.Value))
		for i, b := range t.Value {
			enc[i] = base64.StdEncoding.EncodeToString(b)
		}
		return map[string]any{"BS": stringsToAny(enc)}, nil
	default:
		return nil, fmt.Errorf("%w %T", ErrUnknownAttribute, av)
	}
}

// stringsToAny copies s into a slice of any, the slice type that jq reads.
func stringsToAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// nativeItem builds the item to write for a native record. The value must be an
// item in DynamoDB JSON. It is parsed strictly: a malformed type tag, a payload of
// the wrong type, or a number that DynamoDB cannot store exactly is an error that
// names the record key. The primary-key attributes decoded from the record key
// overlay the item, as they do for the normalized form.
func (s *Store) nativeItem(key string, value any) (map[string]types.AttributeValue, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dynamodb: record %q: native value must be an item object, got %T", key, value)
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("dynamodb: record %q: encode native value: %w", key, err)
	}
	item, err := ParseItem(raw)
	if err != nil {
		return nil, fmt.Errorf("dynamodb: record %q is not valid DynamoDB JSON: %w", key, err)
	}
	if err := checkNativeItem(item); err != nil {
		return nil, fmt.Errorf("dynamodb: record %q is not valid DynamoDB JSON: %w", key, err)
	}
	return s.withKey(key, item)
}

// checkNativeItem makes sure that DynamoDB can store every value of a parsed item
// exactly: each number passes checkNumber, no set is empty, and no NULL is false.
func checkNativeItem(item map[string]types.AttributeValue) error {
	for _, name := range slices.Sorted(maps.Keys(item)) {
		if err := checkNativeValue(attrPath("").field(name), item[name]); err != nil {
			return err
		}
	}
	return nil
}

// checkNativeValue checks one attribute value at path p and the values below it.
func checkNativeValue(p attrPath, av types.AttributeValue) error {
	switch t := av.(type) {
	case *types.AttributeValueMemberM:
		return checkNativeMap(p, t.Value)
	case *types.AttributeValueMemberL:
		return checkNativeList(p, t.Value)
	default:
		return checkNativeLeaf(p, av)
	}
}

// checkNativeLeaf checks a value that has no nested values.
func checkNativeLeaf(p attrPath, av types.AttributeValue) error {
	switch t := av.(type) {
	case *types.AttributeValueMemberN:
		return numberIn(p, t.Value)
	case *types.AttributeValueMemberNS:
		return checkNumberSet(p, t.Value)
	case *types.AttributeValueMemberSS:
		return nonEmptySet(p, "SS", len(t.Value))
	case *types.AttributeValueMemberBS:
		return nonEmptySet(p, "BS", len(t.Value))
	case *types.AttributeValueMemberNULL:
		return checkNativeNull(p, t.Value)
	}
	return nil
}

// checkNativeNull refuses a NULL that is false.
func checkNativeNull(p attrPath, v bool) error {
	if !v {
		return fmt.Errorf("attribute %q: NULL must be true", string(p))
	}
	return nil
}

// checkNativeMap checks the values of an M value in sorted name order.
func checkNativeMap(p attrPath, m map[string]types.AttributeValue) error {
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if err := checkNativeValue(p.field(name), m[name]); err != nil {
			return err
		}
	}
	return nil
}

// checkNativeList checks the elements of an L value in order.
func checkNativeList(p attrPath, list []types.AttributeValue) error {
	for i, e := range list {
		if err := checkNativeValue(p.element(i), e); err != nil {
			return err
		}
	}
	return nil
}

// numberIn runs checkNumber on text and adds the attribute path to a refusal.
func numberIn(p attrPath, text string) error {
	if err := checkNumber(text); err != nil {
		return p.wrap(err)
	}
	return nil
}

// checkNumberSet runs checkNumber on every member of an NS value.
func checkNumberSet(p attrPath, members []string) error {
	if err := nonEmptySet(p, "NS", len(members)); err != nil {
		return err
	}
	for _, m := range members {
		if err := numberIn(p, m); err != nil {
			return err
		}
	}
	return nil
}

// nonEmptySet refuses an empty set, which DynamoDB does not store.
func nonEmptySet(p attrPath, tag string, n int) error {
	if n == 0 {
		return fmt.Errorf("attribute %q: %s must not be empty", string(p), tag)
	}
	return nil
}

var (
	_ query.NativeReader = (*Store)(nil)
	_ query.FormWriter   = (*Store)(nil)
)
