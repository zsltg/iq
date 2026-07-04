package query

import (
	"context"
	"fmt"

	"github.com/itchyny/gojq"
)

// Combiner runs a jq program over named, in-memory inputs — the combine step of
// a cross-source query, where each source's reduced result set is bound to a
// variable. It holds no store: the per-source reads happen before it, and the
// combine runs over a null input with the sources bound as variables.
type Combiner struct{}

// NewCombiner returns a Combiner.
func NewCombiner() *Combiner { return &Combiner{} }

// Run compiles src with names bound to values (names include the leading "$",
// matched positionally to values), runs it over a null input, and calls emit for
// each produced value. A blank program, a parse or compile failure, or a jq
// runtime error is returned with context.
func (*Combiner) Run(ctx context.Context, src string, names []string, values []any, emit func(v any) error) error {
	if src == "" {
		return ErrEmptyExpression
	}
	q, err := gojq.Parse(src)
	if err != nil {
		return fmt.Errorf("parse combine: %w", err)
	}
	code, err := gojq.Compile(q, gojq.WithVariables(names))
	if err != nil {
		return fmt.Errorf("compile combine: %w", err)
	}
	iter := code.RunWithContext(ctx, nil, values...)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, ok := v.(error); ok {
			return fmt.Errorf("run combine: %w", err)
		}
		if err := emit(v); err != nil {
			return err
		}
	}
}
