package query

import (
	"context"
	"fmt"
)

// NonObjectValueError is the uniform error a keyed document store returns when a
// record's value is not a JSON object. Wrapping a scalar as {value: ...} would break
// the round-trip — a "42" copied from Redis would come back as {"value":"42"} — so the
// store rejects it and points the user at an explicit transform. driver names the
// backend; key is the record key the value arrived under.
func NonObjectValueError(driver, key string) error {
	return fmt.Errorf(
		`%s: value for key %q is not a JSON object; transform explicitly, e.g. --filter 'if type == "object" then . else {value: .} end'`,
		driver, key,
	)
}

// Record is one typed item crossing the write boundary. Type is a driver-neutral
// discriminator: "" (or "document") for a schemaless store such as MongoDB; the
// native Redis type ("string", "hash", "list", "set", "zset", "stream", "json")
// for Redis. Value is the JSON-ready normalized value, the same shape the read
// path produces, so a copy is a read followed by its inverse write.
type Record struct {
	Key   string
	Type  string
	Value any
}

// WriteMode selects how a Putter reconciles a record whose key already exists.
type WriteMode int

const (
	// Upsert overwrites an existing key and inserts a new one. It is idempotent,
	// so a re-run of the same copy converges — the default for migration/backfill.
	Upsert WriteMode = iota
	// InsertOnly writes only keys that do not yet exist, skipping the rest and
	// counting them, so an accidental clobber is impossible.
	InsertOnly
)

// A --replace copy is not a WriteMode: it is Clear (the destination once) followed
// by an Upsert copy, so the command composes Clearer with the copy rather than the
// Putter carrying a per-batch replace mode.

// WriteStat reports the outcome of a batch of writes. It is accumulated across a
// whole copy and rendered to the user, so a non-atomic multi-key write is honest
// about what it did rather than pretending all-or-nothing. Where a backend cannot
// report an overwrite natively, Overwritten is counted from a key pre-read taken
// before the batch: it is accounting only — it never changes which records are
// written — and, not being atomic with the writes, can skew by one under a
// concurrent write to the same key.
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

// The write surface is split into optional capability interfaces, mirroring the
// read path's FilteredScanner/Estimator/SourceOpener idiom above. A driver
// implements the capabilities its backend can meaningfully offer; a command
// type-asserts and rejects cleanly when a capability is absent, so adding a
// backend never edits the commands and no backend is forced to fake an operation
// its model lacks.

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
// whose container cannot be removed, only emptied (a Redis DB index), simply does
// not implement it, and the command reports the operation unsupported.
type Dropper interface {
	Drop(ctx context.Context) error
}

// Deleter removes a named set of keys from the container, keeping the container
// (the `iq data delete` command). A backend whose model has no per-key delete (an
// InfluxDB point has no stable row identity) simply does not implement it, and the
// command reports the operation unsupported — the Dropper idiom.
type Deleter interface {
	// Delete removes each key in keys, returning how many were removed and how
	// many were already absent. A missing key is not an error (delete is
	// idempotent, so a re-run converges). It never removes a key not named, and
	// is bounded by ctx. On success Deleted + Missing == len(keys).
	Delete(ctx context.Context, keys []string) (DeleteStat, error)
}

// DeleteStat reports the outcome of a delete batch, honest about non-atomicity the
// way WriteStat is: Deleted keys that existed and were removed, Missing keys that
// were already absent. Where a backend cannot distinguish the two without a pre-read
// (DynamoDB BatchWriteItem is silent on absence), the driver takes a bounded key
// pre-read whose count is accounting only — it never changes which keys are removed,
// and, not being atomic with the delete, can skew by one under a concurrent write.
type DeleteStat struct {
	Deleted int
	Missing int
}

// TypedReader lets a copy read each item with its native type preserved, so a
// round-trip reconstructs the exact structure. MongoDB's is trivial (every item
// is a document); Redis carries the TYPE it already reads per key.
type TypedReader interface {
	// TypedScan walks the whole source, handing fn each page of records as it is
	// fetched, so a streaming caller holds only one page in memory. Bounded by ctx;
	// it stops at the first error from fn or the store.
	TypedScan(ctx context.Context, fn func(batch []Record) error) error
}
