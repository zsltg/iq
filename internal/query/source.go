package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/itchyny/gojq"
)

// UsesSource reports whether filter calls the in-filter source() function. Such
// a filter reads entirely through named sources and runs over a null input, so
// the caller routes it to a CrossEngine instead of opening a primary store. It
// asks gojq's compiler, which flags source() as an undefined function when it is
// not provided; a filter that does not parse returns the parse error.
func UsesSource(filter string) (bool, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return false, fmt.Errorf("parse expression: %w", err)
	}
	_, err = gojq.Compile(q)
	return err != nil && strings.Contains(err.Error(), "not defined: source/"), nil
}

// CrossEngine runs a jq filter whose data comes entirely from source(name;
// filter) calls, over a null input — there is no primary store. It is the engine
// for a cross-source filter (one UsesSource reports true for).
type CrossEngine struct {
	opener SourceOpener
}

// NewCrossEngine returns a CrossEngine that reaches sources through opener.
func NewCrossEngine(opener SourceOpener) *CrossEngine {
	return &CrossEngine{opener: opener}
}

// Run compiles filter with the source() function bound and runs it over a null
// input, calling emit for each produced value. A blank filter, a parse or
// compile failure, or a jq runtime error is returned with context.
func (e *CrossEngine) Run(ctx context.Context, filter string, opts RunOptions, emit func(v any) error) error {
	if filter == "" {
		return ErrEmptyExpression
	}
	q, err := gojq.Parse(filter)
	if err != nil {
		return fmt.Errorf("parse expression: %w", err)
	}
	code, err := gojq.Compile(q, gojq.WithIterFunction("source", 1, 2, sourceFunc(ctx, opts, e.opener)))
	if err != nil {
		return fmt.Errorf("compile expression: %w", err)
	}
	iter := code.RunWithContext(ctx, nil)
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, ok := v.(error); ok {
			return fmt.Errorf("run expression: %w", err)
		}
		if err := emit(v); err != nil {
			return err
		}
	}
}

// sourceFunc builds the source(name; filter) implementation for one run. It
// resolves name through opener, runs the string sub-filter against that source
// with a nested engine (so a nested source() works too), and yields the results;
// errors are yielded as jq error values. Both arguments are string values, so
// the sub-filter is passed quoted, e.g.
// source("orders"; ".[] | select(.total > 99)"). A one-argument source("name")
// yields the whole source (equivalent to source("name"; ".")).
func sourceFunc(ctx context.Context, opts RunOptions, opener SourceOpener) func(any, []any) gojq.Iter {
	return func(_ any, args []any) gojq.Iter {
		name, ok := args[0].(string)
		if !ok {
			return gojq.NewIter[any](fmt.Errorf("source: name must be a string, got %T", args[0]))
		}
		filter := "."
		if len(args) > 1 {
			filter, ok = args[1].(string)
			if !ok {
				return gojq.NewIter[any](fmt.Errorf("source(%q): filter must be a string, got %T", name, args[1]))
			}
		}
		store, err := opener.Open(ctx, name)
		if err != nil {
			return gojq.NewIter[any](fmt.Errorf("source %q: %w", name, err))
		}
		var out []any
		// opts flows through unchanged, so a RunOptions.OnPage progress hook set
		// by the CLI ticks for each source's scan too (counts aggregate across
		// sources, which is the intended cross-source scan total).
		if err := NewJQEngine(store).Run(ctx, filter, opts, func(v any) error {
			out = append(out, v)
			return nil
		}); err != nil {
			return gojq.NewIter[any](fmt.Errorf("source %q: %w", name, err))
		}
		return gojq.NewIter(out...)
	}
}
