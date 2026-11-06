package query

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// rowKeyKey is the context key of the row-key notice sink.
type rowKeyKey struct{}

// RowKeySink receives one call for each row key that an adapter showed in a form
// that a later Get, delete or copy cannot reverse. The raw key is not valid UTF-8,
// or it has the wrong width for the declared key type. shown is the text shown.
type RowKeySink func(shown string)

// WithRowKeyNotice returns a context that carries sink. An adapter that shows a
// raw row key in such a form reports it with NoteRowKey.
func WithRowKeyNotice(ctx context.Context, sink RowKeySink) context.Context {
	return context.WithValue(ctx, rowKeyKey{}, sink)
}

// NoteRowKey reports one row key that an adapter showed in a form that cannot be
// reversed. It does
// nothing when ctx has no sink.
func NoteRowKey(ctx context.Context, shown string) {
	if sink, ok := ctx.Value(rowKeyKey{}).(RowKeySink); ok {
		sink(shown)
	}
}

// RowKeySummary counts the row keys of one read that an adapter showed in a form
// that cannot be reversed and remembers the first one. It is safe for concurrent use. The zero
// value is ready.
type RowKeySummary struct {
	count atomic.Int64
	first sync.Once
	shown string
}

// Sink returns the function that feeds the summary. Pass it to
// WithRowKeyNotice.
func (s *RowKeySummary) Sink() RowKeySink {
	return func(shown string) {
		s.first.Do(func() { s.shown = shown })
		s.count.Add(1)
	}
}

// Text describes the keys. It returns an empty string when no key needs a
// notice.
func (s *RowKeySummary) Text() string {
	count := s.count.Load()
	if count == 0 {
		return ""
	}
	noun := "row keys that"
	if count == 1 {
		noun = "row key that"
	}
	return fmt.Sprintf("iq showed %d HBase %s a Get, delete or copy cannot address exactly "+
		"(the key is not valid UTF-8, or it has the wrong width for the declared ?keytype). Such a key reaches a different row. "+
		"Declare ?keytype=bytes on the source, or use --native, to address them exactly. First: %q",
		count, noun, s.shown)
}
