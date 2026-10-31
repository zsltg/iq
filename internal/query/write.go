package query

import (
	"context"
	"fmt"
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
