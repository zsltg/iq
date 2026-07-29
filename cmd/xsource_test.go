package cmd

import (
	"context"
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

func TestPlanFrom(t *testing.T) {
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("users", "redis://h"))
	require.NoError(t, cf.Add("shop", "mongodb://h/db?collection=orders"))
	require.NoError(t, cf.Add("prod/books", "mongodb://h/db?collection=books"))

	t.Run("resolves a stage", func(t *testing.T) {
		stages, err := planFrom(cf, []string{"users=.a"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "users", stages[0].varName)
		require.Equal(t, "users", stages[0].spec.handle)
		require.Equal(t, ".a", stages[0].spec.filter)
		require.Equal(t, "redis://h", stages[0].spec.url)
	})

	t.Run("grouped handle sanitizes the var", func(t *testing.T) {
		stages, err := planFrom(cf, []string{"prod/books=.[]"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "prod_books", stages[0].varName)
		require.Equal(t, "prod/books", stages[0].spec.handle)
	})

	t.Run("filter may contain equals signs", func(t *testing.T) {
		stages, err := planFrom(cf, []string{"users=.[] | select(.a == 1)"})
		require.NoError(t, err)
		require.Equal(t, ".[] | select(.a == 1)", stages[0].spec.filter)
	})

	t.Run("dotted spec sets the address and binds the dotted var", func(t *testing.T) {
		stages, err := planFrom(cf, []string{"shop.customers=.[]"})
		require.NoError(t, err)
		require.Len(t, stages, 1)
		require.Equal(t, "shop", stages[0].spec.handle)
		require.Equal(t, "customers", stages[0].spec.address)
		require.Equal(t, "shop_customers", stages[0].varName)
	})

	t.Run("redis rejects a dotted spec", func(t *testing.T) {
		_, err := planFrom(cf, []string{"users.foo=.[]"})
		require.ErrorContains(t, err, "no collections")
	})

	errTests := []struct {
		name string
		spec string
		want string
	}{
		{"no equals", "users", "expected name="},
		{"empty name", "=.a", "expected <source>"},
		{"empty filter", "users=", "empty filter"},
		{"unknown source", "nope=.a", "unknown source"},
	}
	for _, tt := range errTests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := planFrom(cf, []string{tt.spec})
			require.ErrorContains(t, err, tt.want)
		})
	}

	t.Run("duplicate variable", func(t *testing.T) {
		_, err := planFrom(cf, []string{"users=.a", "users=.b"})
		require.ErrorContains(t, err, "both bind $users")
	})
}

func TestRunCombineGuards(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	t.Run("combine without from", func(t *testing.T) {
		err := runCombine(cmd, &config{combine: "."})
		require.ErrorContains(t, err, "at least one --from")
	})

	t.Run("from without combine", func(t *testing.T) {
		err := runCombine(cmd, &config{from: []string{"users=."}})
		require.ErrorContains(t, err, "requires --combine")
	})
}
