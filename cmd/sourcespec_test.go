package cmd

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/numfmt"
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
		{"unknown source", "nope=.[]", "run `iq ls`"},
		{"stdin is not a source", "-=.[]", "run `iq ls`"},
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

// TestSourceSpecRunConfigPropagates pins that a spec read inherits the run-wide
// settings. Decimal mode is the one that changes results — it decides how a
// fractional number reaches the filter — so a spec dropped here would quietly
// match different documents in `iq diff` than in `iq`. The rest keep the store
// decorator logging and tracing as it does on a plain query.
func TestSourceSpecRunConfigPropagates(t *testing.T) {
	var trace bytes.Buffer
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config{
		trace:        &trace,
		decimalMode:  numfmt.DecimalString,
		logger:       lg,
		noCache:      true,
		noCacheIndex: true,
	}
	spec := sourceSpec{endpoint: endpoint{url: "redis://h:6379", address: "orders"}}

	got := spec.runConfig(cfg)

	require.Equal(t, "redis://h:6379", got.url, "the spec's own url")
	require.Equal(t, "orders", got.address, "the spec's own keyspace")
	require.Same(t, &trace, got.trace)
	require.Equal(t, numfmt.DecimalString, got.decimalMode)
	require.Same(t, lg, got.logger)
	require.True(t, got.noCache)
	require.True(t, got.noCacheIndex)
}

// TestConfigRunOptions pins the engine options a spec read runs under: pushdown
// follows --no-compile inverted, and the logger is handed through so the scan
// strategy record still fires.
func TestConfigRunOptions(t *testing.T) {
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))

	on := (&config{noCompile: false, logger: lg}).runOptions()
	off := (&config{noCompile: true, logger: lg}).runOptions()

	require.True(t, on.Compile, "pushdown is on unless --no-compile")
	require.False(t, off.Compile, "--no-compile turns pushdown off")
	require.Same(t, lg, on.Logger)
}
