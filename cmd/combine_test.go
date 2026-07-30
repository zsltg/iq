package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
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
