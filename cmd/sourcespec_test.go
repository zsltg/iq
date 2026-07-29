package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
)

func specConfig(t *testing.T) *iqconfig.Config {
	t.Helper()
	cf := &iqconfig.Config{Sources: map[string]iqconfig.Source{}}
	require.NoError(t, cf.Add("users", "redis://h"))
	require.NoError(t, cf.Add("shop", "mongodb://h/db?collection=orders"))
	return cf
}

// TestParseSourceSpecSplitsAtFirstEquals is the load-bearing case: a jq filter
// almost always contains "==", so cutting at the last "=" would truncate the
// address and swallow the expression. Each row pins where the boundary falls.
func TestParseSourceSpecSplitsAtFirstEquals(t *testing.T) {
	cf := specConfig(t)
	tests := []struct {
		name       string
		spec       string
		def        string
		wantHandle string
		wantAddr   string
		wantFilter string
	}{
		{"no filter takes the default", "users", "", "users", "", ""},
		{"no filter takes a supplied default", "users", ".[]", "users", "", ".[]"},
		{"plain filter", "users=.[]", "", "users", "", ".[]"},
		{"filter with equality", `users=.[] | select(.a == 1)`, "", "users", "", `.[] | select(.a == 1)`},
		{"filter with several equals", `users=.[] | select(.a == 1 and .b == 2)`, "", "users", "", `.[] | select(.a == 1 and .b == 2)`},
		{"explicit filter beats the default", "users=.[]", ".x", "users", "", ".[]"},
		{"dotted address", "shop.customers=.[]", "", "shop", "customers", ".[]"},
		{"dotted address with no filter", "shop.customers", "", "shop", "customers", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSourceSpec(cf, tt.spec, tt.def)

			require.NoError(t, err)
			require.Equal(t, tt.wantHandle, got.handle)
			require.Equal(t, tt.wantAddr, got.address)
			require.Equal(t, tt.wantFilter, got.filter)
		})
	}
}

func TestParseSourceSpecRejects(t *testing.T) {
	cf := specConfig(t)
	tests := []struct {
		name string
		spec string
		want string
	}{
		{"empty filter", "users=", "empty filter"},
		{"blank filter", "users=   ", "empty filter"},
		{"empty name", "=.[]", "expected <source>"},
		{"blank spec", "", "expected <source>"},
		{"unknown source", "nope=.[]", "unknown source"},
		{"stdin is not a source", "-=.[]", "unknown source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSourceSpec(cf, tt.spec, "")

			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestResolveSourceSpecRefusesFileEndpoints pins the one behavior the shared
// address resolver has that a read spec must not inherit: "" and "-" resolve to
// stdout and stdin endpoints for a copy, which nothing can query.
func TestResolveSourceSpecRefusesFileEndpoints(t *testing.T) {
	cf := specConfig(t)
	for _, arg := range []string{"-", ""} {
		t.Run("arg "+arg, func(t *testing.T) {
			_, err := resolveSourceSpec(cf, arg)

			require.Error(t, err)
		})
	}
}
