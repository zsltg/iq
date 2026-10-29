package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/numfmt"
)

// TestPlanSourceCarriesTheDecimalMode pins that a plan reads the decimal mode of the
// run. The Neo4j plan leaves out an equality with a number text in string mode, so its
// plan has no server filter. In auto mode the plan has one.
func TestPlanSourceCarriesTheDecimalMode(t *testing.T) {
	const url = "neo4j://h/db?label=P"
	fromConfig := func(mode numfmt.DecimalMode) planSource {
		cfg := &config{url: url, decimalMode: mode}
		return cfg.planSource()
	}
	fromEndpoint := func(mode numfmt.DecimalMode) planSource {
		return endpoint{url: url}.planSource(mode)
	}
	tests := []struct {
		name       string
		source     func(mode numfmt.DecimalMode) planSource
		mode       numfmt.DecimalMode
		wantFilter bool
	}{
		{"config, auto mode", fromConfig, numfmt.DecimalAuto, true},
		{"config, string mode", fromConfig, numfmt.DecimalString, false},
		{"endpoint, auto mode", fromEndpoint, numfmt.DecimalAuto, true},
		{"endpoint, string mode", fromEndpoint, numfmt.DecimalString, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp, err := buildSourcePlan(tt.source(tt.mode), `.[] | select(.price == "1.5")`, true, false)
			require.NoError(t, err)
			require.True(t, sp.hasPlan)
			require.Equal(t, tt.wantFilter, sp.filter != nil)
		})
	}
}
