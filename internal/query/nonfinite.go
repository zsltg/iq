package query

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// nonFiniteKey is the context key of the non-finite notice sink.
type nonFiniteKey struct{}

// NonFiniteSink receives one call for each float that an adapter converted to
// a string because JSON cannot hold it. key names the record. field names the
// value inside the record.
type NonFiniteSink func(key, field string)

// WithNonFiniteNotice returns a context that carries sink. An adapter that
// converts a NaN or an infinity to a string reports it with NoteNonFinite.
func WithNonFiniteNotice(ctx context.Context, sink NonFiniteSink) context.Context {
	return context.WithValue(ctx, nonFiniteKey{}, sink)
}

// NoteNonFinite reports each field of the record key that an adapter converted.
// It does nothing when ctx has no sink.
func NoteNonFinite(ctx context.Context, key string, fields ...string) {
	sink, ok := ctx.Value(nonFiniteKey{}).(NonFiniteSink)
	if !ok {
		return
	}
	for _, f := range fields {
		sink(key, f)
	}
}

// NonFiniteSummary counts the conversions of one read and remembers the first
// one. It is safe for concurrent use. The zero value is ready.
type NonFiniteSummary struct {
	count      atomic.Int64
	first      sync.Once
	firstKey   string
	firstField string
}

// Sink returns the function that feeds the summary. Pass it to
// WithNonFiniteNotice.
func (s *NonFiniteSummary) Sink() NonFiniteSink {
	return func(key, field string) {
		s.first.Do(func() { s.firstKey, s.firstField = key, field })
		s.count.Add(1)
	}
}

// Text describes the conversions. It returns an empty string when nothing was
// converted. The text names the first key and field and never holds a value.
func (s *NonFiniteSummary) Text() string {
	count := s.count.Load()
	if count == 0 {
		return ""
	}
	noun := "values"
	if count == 1 {
		noun = "value"
	}
	return fmt.Sprintf("iq converted %d %s that JSON cannot hold (NaN or infinity) to strings; first: key %q, field %q",
		count, noun, s.firstKey, s.firstField)
}
