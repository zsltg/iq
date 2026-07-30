package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

func TestVarName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"orders", "orders"},
		{"prod/books", "prod_books"},
		{"my-cache", "my_cache"},
		{"a.b", "a_b"},
		{"1x", "_1x"},
		{"0y", "_0y"},
		{"9z", "_9z"},
		// The guard's three conjuncts each carry their own weight, so each needs a
		// case that only it decides. Empty: the length check is what keeps v[0]
		// from panicking. Below '0': a punctuation lead is not a digit, so it must
		// not be escaped even though it sorts under '9'. Single digit: the escape
		// reads the *first* byte, so a one-character name must not index past it.
		{"", ""},
		{"!x", "!x"},
		{"1", "_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, varName(tt.name))
		})
	}
}

func TestPlanCombine(t *testing.T) {
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("users", "redis://h"))
	require.NoError(t, cf.Add("shop", "mongodb://h/db?collection=orders"))
	require.NoError(t, cf.Add("prod/books", "mongodb://h/db?collection=books"))

	t.Run("resolves a stage", func(t *testing.T) {
		stages, err := planCombine(cf, []string{"users=.a"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "users", stages[0].varName)
		require.Equal(t, "users", stages[0].spec.handle)
		require.Equal(t, ".a", stages[0].spec.filter)
		require.Equal(t, "redis://h", stages[0].spec.url)
	})

	t.Run("grouped handle sanitizes the var", func(t *testing.T) {
		stages, err := planCombine(cf, []string{"prod/books=.[]"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "prod_books", stages[0].varName)
		require.Equal(t, "prod/books", stages[0].spec.handle)
	})

	t.Run("filter may contain equals signs", func(t *testing.T) {
		stages, err := planCombine(cf, []string{"users=.[] | select(.a == 1)"})
		require.NoError(t, err)
		require.Equal(t, ".[] | select(.a == 1)", stages[0].spec.filter)
	})

	t.Run("dotted spec sets the address and binds the dotted var", func(t *testing.T) {
		stages, err := planCombine(cf, []string{"shop.customers=.[]"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "shop", stages[0].spec.handle)
		require.Equal(t, "customers", stages[0].spec.address)
		require.Equal(t, "shop_customers", stages[0].varName)
	})

	t.Run("redis rejects a dotted spec", func(t *testing.T) {
		_, err := planCombine(cf, []string{"users.foo=.[]"})
		require.ErrorContains(t, err, "no collections")
	})

	// A spec with no "=" is legal and means the whole keyspace, the same as it
	// does for diff and schema; "." is holistic, so the engine's scan guard — not
	// this parser — is what decides whether the read is allowed.
	t.Run("bare address binds the whole keyspace", func(t *testing.T) {
		stages, err := planCombine(cf, []string{"users"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "users", stages[0].varName)
		require.Equal(t, ".", stages[0].spec.filter)
	})

	errTests := []struct {
		name    string
		spec    string
		want    string
		wrapped bool // the spec error wraps a cause, so the chain must survive
	}{
		// Every row asserts the spec context too: the inner message alone never
		// says which spec was malformed, and a combine may carry several. The
		// rows that wrap a cause also assert the chain survives.
		{"empty name", "=.a", `invalid source "=.a": `, true},
		{"empty filter", "users=", `invalid source "users=": `, true},
		{"unknown source", "nope=.a", `invalid source "nope=.a": `, true},
	}
	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := planCombine(cf, []string{tt.spec})
			require.ErrorContains(t, err, tt.want)
			// The clause context is added by wrapping, so the underlying cause has
			// to stay reachable: formatting it into the message instead would read
			// identically and silently break errors.Is/As for every caller.
			if tt.wrapped {
				require.Error(t, errors.Unwrap(err), "the cause must stay unwrappable")
			}
		})
	}

	t.Run("duplicate variable", func(t *testing.T) {
		_, err := planCombine(cf, []string{"users=.a", "users=.b"})
		require.ErrorContains(t, err, "both bind $users")
	})
}

func TestRunCombineGuards(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	// --with is checked before any source work, so a missing program never costs
	// a connection. Cobra's MinimumNArgs(1) covers the no-source case.
	tests := []struct {
		name string
		with string
	}{
		{"missing", ""},
		{"blank", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCombine(cmd, &config{}, []string{"users=."}, tt.with)
			require.ErrorContains(t, err, "needs --with")
		})
	}
}

// TestCombineCmdRequiresASource pins that `iq combine --with .` with no
// positional is refused by the arg validator rather than reaching runCombine and
// combining nothing.
func TestCombineCmdRequiresASource(t *testing.T) {
	c := newCombineCmd(&config{})
	require.Error(t, c.Args(c, nil))
	require.NoError(t, c.Args(c, []string{"users=."}))
}

// TestCombineTransform pins the two shapes --insert refuses before any source is
// opened. A combine emits over a null input, so no record carries a key to
// inherit: without --key or --key-field every record would die at the write
// boundary mid-copy, which is worse than refusing up front.
func TestCombineTransform(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config
		want string
	}{
		{"no key at all", &config{insert: "dest"}, "needs --key or --key-field"},
		{"key-prefix alone is not a key", &config{insert: "dest", keyPrefix: "x:"}, "needs --key or --key-field"},
		{"conflicting write modes", &config{insert: "dest", moveKey: ".id", noOverwrite: true, replace: true}, "mutually exclusive"},
		{"bad --key expression", &config{insert: "dest", moveKey: ".["}, "--key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := combineTransform(tt.cfg)
			require.ErrorContains(t, err, tt.want)
		})
	}

	t.Run("accepts --key", func(t *testing.T) {
		tr, err := combineTransform(&config{insert: "dest", moveKey: ".id"})
		require.NoError(t, err)
		require.NotNil(t, tr)
	})

	t.Run("accepts --key-field", func(t *testing.T) {
		tr, err := combineTransform(&config{insert: "dest", keyField: "id"})
		require.NoError(t, err)
		require.NotNil(t, tr)
	})
}

// TestCombineRecordsStreamsKeylessRecords pins the adapter between the combine's
// value stream and the write path: every emitted value becomes one keyless
// record, in order, and the combine's own error propagates instead of being
// swallowed into a short copy.
func TestCombineRecordsStreamsKeylessRecords(t *testing.T) {
	t.Run("each value becomes a record", func(t *testing.T) {
		src := combineRecords("$a[]", []string{"$a"}, []any{[]any{1, 2, 3}})
		var got []query.Record
		require.NoError(t, src(context.Background(), func(batch []query.Record) error {
			got = append(got, batch...)
			return nil
		}))
		require.Equal(t, []query.Record{{Value: 1}, {Value: 2}, {Value: 3}}, got)
	})

	t.Run("a combine error stops the walk", func(t *testing.T) {
		src := combineRecords(`$a[] | error("boom")`, []string{"$a"}, []any{[]any{1}})
		err := src(context.Background(), func([]query.Record) error { return nil })
		require.ErrorContains(t, err, "boom")
	})
}

// TestCombineRejectsWriteFlagsWithoutInsert pins that a write flag on a
// rendering run is refused rather than read and dropped — the exact defect the
// retired --from/--combine surface had.
func TestCombineRejectsWriteFlagsWithoutInsert(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("users", "redis://127.0.0.1:1/0"))
	seedConfig(t, c)

	for _, flag := range []string{"--key=.id", "--key-field=id", "--key-prefix=x:", "--type=json", "--no-overwrite", "--replace", "--force", "--dry-run"} {
		t.Run(flag, func(t *testing.T) {
			root, _ := newRootCmd()
			_, err := runCmd(t, root, "combine", "--timeout", "200ms", flag, "users=.[]", "--with", "$users")
			require.ErrorContains(t, err, "applies to --insert")
		})
	}
}

// TestCombineRefusesParquetToTerminal exercises the binary-format guard inside
// runCombine: parquet to an interactive terminal must be refused before any
// source is opened, exactly as the plain query path refuses it. The format is
// resolved ahead of the first connection, so the refusal costs no I/O.
func TestCombineRefusesParquetToTerminal(t *testing.T) {
	origTTY := binaryTTYCheck
	binaryTTYCheck = func(io.Writer) bool { return true }
	t.Cleanup(func() { binaryTTYCheck = origTTY })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runCombine(cmd, &config{format: "parquet", timeout: time.Second}, []string{"users=."}, "$users")

	require.Error(t, err)
	require.Contains(t, err.Error(), "terminal")
	require.Empty(t, buf.Bytes(), "nothing should be written when the format is refused")
}

// TestCombineExplainSurfacesAPlanError pins that a plan that cannot be built
// stops the run: --explain parses every stage's filter, so an unparseable one
// must surface its syntax error rather than fall through to a connection.
func TestCombineExplainSurfacesAPlanError(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("users", "redis://h:6379/0"))
	seedConfig(t, c)

	root, _ := newRootCmd()
	_, err := runCmd(t, root, "combine", "--explain", "users=.[", "--with", "$users")

	require.Error(t, err)
	require.NotContains(t, err.Error(), "connect", "the plan must fail before any dial")
}

// TestCombineVerboseWritesThePlanToStderr pins that --verbose alone renders the
// plan: it shares the branch with --explain but keeps running afterwards, so a
// guard that only honored --explain would silently drop the trace a -v run is
// asked for. The run itself then fails on the unreachable source, which is not
// what this asserts.
func TestCombineVerboseWritesThePlanToStderr(t *testing.T) {
	c := newSeed()
	require.NoError(t, c.Add("users", "redis://127.0.0.1:1/0"))
	seedConfig(t, c)

	root, _ := newRootCmd()
	out, _ := runCmd(t, root, "combine", "--verbose", "--timeout", "200ms", "users=.[]", "--with", "$users")

	require.Contains(t, out, "query plan")
	require.Contains(t, out, "$users  <-  users (redis)")
}
