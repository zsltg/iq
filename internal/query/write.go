package query

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"unicode/utf8"
)

// NonObjectValueError is the uniform error a keyed document store returns when a
// record's value is not a JSON object. Wrapping a scalar as {value: ...} breaks
// the round-trip (a "42" copied from Redis comes back as {"value":"42"}). The store
// rejects it and points the user at an explicit transform. driver names the backend.
// key is the record key the value arrived under.
func NonObjectValueError(driver, key string) error {
	return fmt.Errorf(
		`%s: value for key %q is not a JSON object; transform explicitly, e.g. iq 'if type == "object" then . else {value: .} end' --insert <dest>`,
		driver, key,
	)
}

// CheckKeyUTF8 makes sure that a record key is valid UTF-8. A store that writes
// text as JSON, or as an SDK string, replaces each invalid byte with U+FFFD. So two
// different keys would become one key. Such a store calls this before it writes.
// A store that keeps the bytes exactly does not call it. driver names the backend.
func CheckKeyUTF8(driver, key string) error {
	if utf8.ValidString(key) {
		return nil
	}
	return fmt.Errorf("%s: record key %q is not valid UTF-8; the backend would change it to U+FFFD", driver, key)
}

// CheckKeysUTF8 makes sure that every key in keys is valid UTF-8. A store that
// deletes by a key sent as JSON or as an SDK string calls it before the first
// delete, so a key like "\xff" cannot delete the item stored under "\uFFFD".
// driver names the backend.
func CheckKeysUTF8(driver string, keys []string) error {
	for _, k := range keys {
		if err := CheckKeyUTF8(driver, k); err != nil {
			return err
		}
	}
	return nil
}

// CheckValueUTF8 makes sure that every string in a record value is valid UTF-8,
// both the string values and the names of the objects. It looks inside objects and
// arrays. The error names the field path, like "a.b[2]". A store that writes text
// as JSON, or as an SDK string, calls it before any write of the record, because
// the backend would change each invalid byte to U+FFFD. key is the record key and
// driver names the backend.
func CheckValueUTF8(driver, key string, value any) error {
	if validUTF8Tree(value) {
		return nil
	}
	return checkValueUTF8(driver, key, "", value)
}

// validUTF8Tree is the fast yes or no check of CheckValueUTF8. It builds no path
// and sorts no names, so the usual valid record costs little.
func validUTF8Tree(value any) bool {
	switch t := value.(type) {
	case string:
		return utf8.ValidString(t)
	case map[string]any:
		for name, v := range t {
			if !utf8.ValidString(name) || !validUTF8Tree(v) {
				return false
			}
		}
	case []any:
		return !slices.ContainsFunc(t, func(e any) bool { return !validUTF8Tree(e) })
	}
	return true
}

// checkValueUTF8 checks the value at path. The top level has an empty path.
func checkValueUTF8(driver, key, path string, value any) error {
	switch t := value.(type) {
	case string:
		if !utf8.ValidString(t) {
			return fmt.Errorf("%s: key %q: %s holds a string that is not valid UTF-8; the backend would change it to U+FFFD",
				driver, key, fieldLabel(path))
		}
	case map[string]any:
		return checkObjectUTF8(driver, key, path, t)
	case []any:
		for i, e := range t {
			if err := checkValueUTF8(driver, key, path+"["+strconv.Itoa(i)+"]", e); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkObjectUTF8 checks the names and the values of the object at path. It goes
// through the names in sorted order, so the same input always gives the same error.
func checkObjectUTF8(driver, key, path string, obj map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		at := name
		if path != "" {
			at = path + "." + name
		}
		if !utf8.ValidString(name) {
			return fmt.Errorf("%s: key %q: the field name %q is not valid UTF-8; the backend would change it to U+FFFD",
				driver, key, at)
		}
		if err := checkValueUTF8(driver, key, at, obj[name]); err != nil {
			return err
		}
	}
	return nil
}

// fieldLabel names the value at path in an error. The top level has no path.
func fieldLabel(path string) string {
	if path == "" {
		return "the value"
	}
	return "field " + strconv.Quote(path)
}

// Record is one typed item crossing the write boundary. Type is a driver-neutral
// discriminator. It is "" (or "document") for a schemaless store such as MongoDB.
// For Redis it is the native Redis type ("string", "hash", "list", "set", "zset",
// "stream", "json"). Value is the JSON-ready normalized value, the same shape the
// read path produces, so a copy is a read followed by its inverse write.
type Record struct {
	Key   string
	Type  string
	Value any
}

// WriteMode selects how a Putter reconciles a record whose key already exists.
type WriteMode int

const (
	// Upsert overwrites an existing key and inserts a new one. It is idempotent,
	// so a re-run of the same copy converges. It is the default for migration/backfill.
	Upsert WriteMode = iota
	// InsertOnly writes only keys that do not yet exist, skipping the rest and
	// counting them, so an accidental clobber is impossible.
	InsertOnly
)

// A --replace copy is not a WriteMode. It is Clear (the destination once) followed
// by an Upsert copy. So the command composes Clearer with the copy, and the
// Putter does not carry a per-batch replace mode.

// WriteStat reports the outcome of a batch of writes. A copy accumulates it across all
// batches and renders it to the user. So a non-atomic multi-key write reports what it
// did and does not pretend to be all-or-nothing. If a backend cannot report an
// overwrite natively, the driver counts Overwritten from a key pre-read taken before
// the batch. This is accounting only. It never changes which records are written. It is
// not atomic with the writes, so it can be off by one under a concurrent write to the
// same key.
type WriteStat struct {
	Written     int
	Overwritten int
	Skipped     int
}

// add folds other into s, so a Copier can total per-batch stats.
func (s *WriteStat) add(other WriteStat) {
	s.Written += other.Written
	s.Overwritten += other.Overwritten
	s.Skipped += other.Skipped
}

// The write surface is split into optional capability interfaces, like the read path's
// FilteredScanner, Estimator, and SourceOpener. A driver implements the capabilities
// its backend can offer. A command type-asserts and rejects cleanly when a capability
// is absent. A new backend therefore never edits the commands, and no backend must fake
// an operation its model lacks.

// Putter is the destination capability a copy writes through.
type Putter interface {
	// Put writes a batch of records under mode and returns the outcome. It never
	// silently drops a record: a record it cannot represent is a returned error.
	Put(ctx context.Context, batch []Record, mode WriteMode) (WriteStat, error)
}

// Clearer empties the container but keeps it (the `iq data clear` command).
type Clearer interface {
	Clear(ctx context.Context) error
}

// Dropper removes the container entirely (the `iq data drop` command). A backend
// whose container cannot be removed, only emptied (a Redis DB index), does
// not implement it, and the command reports the operation unsupported.
type Dropper interface {
	Drop(ctx context.Context) error
}

// Deleter removes a named set of keys from the container, keeping the container
// (the `iq data delete` command). A backend whose model has no per-key delete (an
// InfluxDB point has no stable row identity) does not implement it. The
// command reports the operation unsupported, like the Dropper idiom.
type Deleter interface {
	// Delete removes each key in keys, returning how many were removed and how
	// many were already absent. A missing key is not an error (delete is
	// idempotent, so a re-run converges). It never removes a key not named, and
	// is bounded by ctx. On success Deleted + Missing == len(keys).
	Delete(ctx context.Context, keys []string) (DeleteStat, error)
}

// DeleteStat reports the outcome of a delete batch and, like WriteStat, is honest about
// non-atomicity. Deleted counts keys that existed and were removed. Missing counts keys
// that were already absent. If a backend cannot tell the two apart without a
// pre-read (DynamoDB BatchWriteItem is silent on absence), the driver takes a
// bounded key pre-read. Its count is accounting only. It never changes which
// keys are removed. It is not atomic with the delete, so it can be off by one
// under a concurrent write.
type DeleteStat struct {
	Deleted int
	Missing int
}

// TypedReader lets a copy read each item with its native type preserved, so a
// round-trip reconstructs the exact structure. For MongoDB this is trivial
// because every item is a document. Redis carries the TYPE it already reads per key.
type TypedReader interface {
	// TypedScan walks the whole source, handing fn each page of records as it is
	// fetched, so a streaming caller holds only one page in memory. Bounded by ctx.
	// It stops at the first error from fn or the store.
	TypedScan(ctx context.Context, fn func(batch []Record) error) error
}
