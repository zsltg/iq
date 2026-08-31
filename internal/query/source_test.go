package query_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// fakeOpener resolves names to fake KVStores for the source() function. openErr,
// when set, is the failure Open reports for an unknown name, so a test can follow
// one specific error out through the wraps.
type fakeOpener struct {
	stores  map[string]query.KVStore
	openErr error
	gotCtx  *context.Context // the ctx Open was handed, so a dropped one is visible
}

func (o fakeOpener) Open(ctx context.Context, name string) (query.KVStore, error) {
	if o.gotCtx != nil {
		*o.gotCtx = ctx
	}
	st, ok := o.stores[name]
	if !ok {
		if o.openErr != nil {
			return nil, o.openErr
		}
		return nil, fmt.Errorf("unknown source %q", name)
	}
	return st, nil
}

func runCross(opener query.SourceOpener, filter string, opts query.RunOptions) ([]any, error) {
	var out []any
	err := query.NewCrossEngine(opener).Run(context.Background(), filter, opts, func(v any) error {
		out = append(out, v)
		return nil
	})
	return out, err
}

func TestUsesSource(t *testing.T) {
	tests := []struct {
		filter string
		want   bool
	}{
		{`source("a"; ".x")`, true},
		{`INDEX(source("a";".[]"); .id) as $u | source("b";".[]")`, true},
		{`.foo | source("b")`, true},
		{`.foo | .bar`, false},
		{`keys`, false},
		// A user-defined source is the user's own function, not the cross-source
		// builtin, so it is not routed to the cross engine.
		{`def source: .x; source`, false},
		// An unknown non-source function is not cross-source; the primary path
		// surfaces its compile error unchanged.
		{`.foo | nope("x")`, false},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			got, err := query.UsesSource(tt.filter)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	t.Run("parse error", func(t *testing.T) {
		_, err := query.UsesSource("@@@ not jq")
		require.Error(t, err)
		// Naming the step is done by wrapping, so gojq's own parse error has to
		// stay reachable underneath.
		require.Error(t, errors.Unwrap(err), "the parse error must stay unwrappable")
	})
}

// TestUsesSourceArity pins the arity the probe binding declares. source() takes
// one or two arguments, so a call outside that range is not the cross-source
// builtin at all and must not be routed to the cross engine — a widened probe
// would claim a filter that the real binding then refuses to compile.
func TestUsesSourceArity(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   bool
	}{
		{name: "one argument is cross-source", filter: `source("a")`, want: true},
		{name: "two arguments are cross-source", filter: `source("a"; ".")`, want: true},
		{name: "no argument is not", filter: `source`, want: false},
		{name: "three arguments are not", filter: `source("a"; "."; ".")`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := query.UsesSource(tt.filter)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSourceFunction(t *testing.T) {
	opener := fakeOpener{stores: map[string]query.KVStore{
		"users": &fakeKV{
			values: map[string]any{
				"u1": map[string]any{"id": "u1", "name": "Ann"},
				"u2": map[string]any{"id": "u2", "name": "Bob"},
			},
			scanKeys: []string{"u1", "u2"},
		},
		"orders": &fakeKV{
			values:   map[string]any{"o1": map[string]any{"userId": "u1", "total": 10}},
			scanKeys: []string{"o1"},
		},
	}}

	t.Run("streams a named source", func(t *testing.T) {
		out, err := runCross(opener, `source("users"; ".[]") | .name`, query.RunOptions{})
		require.NoError(t, err)
		require.Equal(t, []any{"Ann", "Bob"}, out) // .[] over an object is key-sorted
	})

	t.Run("bounded read from a named source", func(t *testing.T) {
		out, err := runCross(opener, `source("users"; ".u1.name")`, query.RunOptions{})
		require.NoError(t, err)
		require.Equal(t, []any{"Ann"}, out)
	})

	t.Run("cross-source join in one filter", func(t *testing.T) {
		filter := `INDEX(source("users"; ".[]"); .id) as $u
			| source("orders"; ".[]")
			| {name: $u[.userId].name, total}`
		out, err := runCross(opener, filter, query.RunOptions{})
		require.NoError(t, err)
		require.Equal(t, []any{map[string]any{"name": "Ann", "total": 10}}, out)
	})

	t.Run("whole source needs unbounded", func(t *testing.T) {
		_, err := runCross(opener, `source("users")`, query.RunOptions{})
		require.ErrorIs(t, err, query.ErrScanNotAllowed)

		out, err := runCross(opener, `source("users") | keys`, query.RunOptions{Unbounded: true})
		require.NoError(t, err)
		require.Equal(t, []any{[]any{"u1", "u2"}}, out)
	})

	t.Run("unknown source errors", func(t *testing.T) {
		_, err := runCross(opener, `source("nope"; ".")`, query.RunOptions{})
		require.ErrorContains(t, err, `source "nope"`)
		require.ErrorContains(t, err, "unknown source")
	})

	t.Run("non-string filter argument errors", func(t *testing.T) {
		_, err := runCross(opener, `source("users"; .x)`, query.RunOptions{})
		require.ErrorContains(t, err, "filter must be a string")
	})

	t.Run("non-string name errors", func(t *testing.T) {
		_, err := runCross(opener, `source(1; ".")`, query.RunOptions{})
		require.ErrorContains(t, err, "name must be a string")
		// The message reports the type of the offending argument, the name; naming
		// the filter's type instead would read as "got string" for this call.
		require.ErrorContains(t, err, "got int", "the reported type is the name's, not the filter's")
	})

	t.Run("the filter's type is reported for a non-string filter", func(t *testing.T) {
		_, err := runCross(opener, `source("users"; 1)`, query.RunOptions{})
		require.ErrorContains(t, err, "filter must be a string")
		require.ErrorContains(t, err, `source("users")`, "the message names the source")
		require.ErrorContains(t, err, "got int", "the reported type is the filter's, not the name's")
	})

	t.Run("the source error stays unwrappable", func(t *testing.T) {
		sentinel := errors.New("open boom")
		failing := fakeOpener{stores: map[string]query.KVStore{}, openErr: sentinel}
		_, err := runCross(failing, `source("gone"; ".")`, query.RunOptions{})
		require.ErrorContains(t, err, `source "gone"`)
		// The opener's failure crosses two wraps on its way out; formatting it in
		// at either one severs errors.Is for the caller that has to classify it.
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("the caller's context reaches the opener", func(t *testing.T) {
		var got context.Context
		recording := fakeOpener{stores: opener.stores, gotCtx: &got}
		var out []any
		ctx := context.WithValue(context.Background(), ctxMarker{}, "marker")
		err := query.NewCrossEngine(recording).Run(ctx, `source("users"; ".u1.name")`, query.RunOptions{},
			func(v any) error { out = append(out, v); return nil })
		require.NoError(t, err)
		require.Equal(t, []any{"Ann"}, out)
		require.NotNil(t, got, "Open must receive a non-nil context")
		require.Equal(t, "marker", got.Value(ctxMarker{}), "the caller's context must reach the opener")
	})
}

// TestCrossEngineSourceArity pins the arity of the bound source() function. One
// or two arguments are the whole surface: a zero-argument call has no name to
// resolve and a three-argument one carries an argument the implementation would
// silently ignore, so both must fail at compile rather than run.
func TestCrossEngineSourceArity(t *testing.T) {
	opener := fakeOpener{stores: map[string]query.KVStore{
		"users": &fakeKV{values: map[string]any{"u1": 1}, scanKeys: []string{"u1"}},
	}}
	tests := []struct {
		name   string
		filter string
	}{
		{name: "no argument", filter: `source`},
		{name: "three arguments", filter: `source("users"; "."; ".")`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runCross(opener, tt.filter, query.RunOptions{})
			require.ErrorContains(t, err, "compile expression")
		})
	}
}

func TestCrossEngineErrors(t *testing.T) {
	opener := fakeOpener{stores: map[string]query.KVStore{}}

	t.Run("blank filter", func(t *testing.T) {
		_, err := runCross(opener, "", query.RunOptions{})
		require.ErrorIs(t, err, query.ErrEmptyExpression)
	})

	t.Run("parse error", func(t *testing.T) {
		_, err := runCross(opener, "@@@ not jq", query.RunOptions{})
		require.ErrorContains(t, err, "parse expression")
	})
}
