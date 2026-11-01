package query

import (
	"context"
	"errors"
)

// ErrNativeGet is returned when a filter names keys and the read is native. A
// NativeReader scans the whole source and has no read by key, so a native read
// needs a filter that starts with .[] . The message names no flag, so the core
// stays independent of the CLI.
var ErrNativeGet = errors.New("a native read scans the whole source and cannot fetch named keys. Use a filter that starts with .[]")

// nativeStore adapts a NativeReader to the KVStore port, so the jq engine runs on
// native records. It has no FilteredScanner, so the engine never pushes a
// predicate down, and the jq filter runs on every record. Pushdown matches the
// normalized shape of a value, and a native value has another shape.
type nativeStore struct {
	reader  NativeReader
	closeFn func() error
}

// NewNativeStore returns a KVStore that serves the records of reader in their
// native form. Close calls closeFn when it is not nil. The store reads by scan
// only. Get fails with ErrNativeGet.
func NewNativeStore(reader NativeReader, closeFn func() error) KVStore {
	return &nativeStore{reader: reader, closeFn: closeFn}
}

// Get refuses a read by key. See ErrNativeGet.
func (s *nativeStore) Get(context.Context, []string) (map[string]any, error) {
	return nil, ErrNativeGet
}

// ScanBatches hands fn each page of the native scan as {key: value}.
func (s *nativeStore) ScanBatches(ctx context.Context, fn func(batch map[string]any) error) error {
	return s.reader.NativeScan(ctx, func(recs []Record) error {
		page := make(map[string]any, len(recs))
		for _, r := range recs {
			page[r.Key] = r.Value
		}
		return fn(page)
	})
}

// Close releases the store that the reader came from.
func (s *nativeStore) Close() error {
	if s.closeFn == nil {
		return nil
	}
	return s.closeFn()
}
