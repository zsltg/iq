// Package jqtruth is a test helper. It gives the predicate that the compiler pushes
// for a jq filter, and the verdict of the full jq filter on one decoded document.
// A driver test compares the two, so the test shows that a pushdown or a prefilter
// never drops a document that jq keeps.
package jqtruth

import (
	"fmt"

	"github.com/itchyny/gojq"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/pushdown"
)

// Predicate returns the predicate that pushdown.Compile builds for filter. It
// reports an error if the filter does not parse or nothing compiles.
func Predicate(filter string) (predicate.Node, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", filter, err)
	}
	pred, ok := pushdown.Compile(q)
	if !ok {
		return nil, fmt.Errorf("filter %q does not compile", filter)
	}
	return pred, nil
}

// Keeps reports whether the full jq filter keeps the decoded document doc. The
// filter runs over a one-entry object, as the engine runs it over a page. A jq error
// counts as a keep, because a dropped document would hide that error.
func Keeps(filter string, doc any) (bool, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return false, fmt.Errorf("parse %q: %w", filter, err)
	}
	// The first value of the iterator is a result or an error. Both mean keep.
	_, ok := q.Run(map[string]any{"k": doc}).Next()
	return ok, nil
}
