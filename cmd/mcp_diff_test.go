package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqconfig "github.com/zsltg/iq/internal/config"
)

// TestMCPDiffStatsRefusesAFilter pins the refusal of a filter on the stats
// layer of iq_diff. The filter can come from the filter field or from the spec
// of either side. The data layer takes the same filter.
func TestMCPDiffStatsRefusesAFilter(t *testing.T) {
	const statsFilterErr = "the stats layer diffs the backend's own introspection, which has no items to filter; drop the filter or diff data/schema"
	data := seedMCP(t)
	other := filepath.Join(filepath.Dir(data), "other.jsonl")
	require.NoError(t, os.WriteFile(other, []byte(mcpDump), 0o600))
	cf, err := iqconfig.Load()
	require.NoError(t, err)
	require.NoError(t, cf.Add("other", iqfile.URL(other)))
	require.NoError(t, cf.Save())
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"a filter in the left spec", map[string]any{"a": "snap=.[]", "b": "other", "stats": true}, statsFilterErr},
		{"a filter in the right spec", map[string]any{"a": "snap", "b": "other=.[]", "stats": true}, statsFilterErr},
		{"a filter in the filter field", map[string]any{"a": "snap", "b": "other", "stats": true, "filter": ".[]"}, statsFilterErr},
		{"the data layer takes a filter in a spec", map[string]any{"a": "snap=.[]", "b": "other", "data": true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := callMCP(t, cs, "iq_diff", tt.args)

			if tt.wantErr == "" {
				var out mcpDiffOutput
				structOf(t, res, &out)
				require.False(t, out.Differ)
				return
			}
			require.Equal(t, tt.wantErr, errorBodyOf(t, res).Error.Message)
		})
	}
}
