package cmd

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMCPToolListPerPermission pins the whole sorted tool list for each
// permission set, so a lost or doubled registration fails.
func TestMCPToolListPerPermission(t *testing.T) {
	read := []string{"iq_diff", "iq_explain", "iq_inspect", "iq_ping", "iq_query", "iq_schema", "iq_sources"}
	with := func(extra ...string) []string {
		out := append(slices.Clone(read), extra...)
		slices.Sort(out)
		return out
	}
	tests := []struct {
		name  string
		allow []string
		want  []string
	}{
		{"read only", nil, with()},
		{"writes", []string{allowWrites}, with("iq_insert")},
		{"exec", []string{allowExec}, with("iq_exec")},
		{"destructive", []string{allowDestructive}, with("iq_data_clear", "iq_data_delete", "iq_data_drop")},
		{
			"everything",
			[]string{allowWrites, allowExec, allowDestructive},
			with("iq_insert", "iq_exec", "iq_data_clear", "iq_data_delete", "iq_data_drop"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := connectMCP(t, newTestMCPServer(tt.allow, 200, 256*1024), nil)
			res, err := cs.ListTools(t.Context(), nil)
			require.NoError(t, err)
			names := make([]string, 0, len(res.Tools))
			for _, tool := range res.Tools {
				names = append(names, tool.Name)
			}
			require.Equal(t, tt.want, names)
		})
	}
}

func TestMCPQueryCheckOrder(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"filter before caps", map[string]any{"source": "snap", "filter": " ", "max_items": -1}, "a filter is required"},
		{"syntax before caps", map[string]any{"source": "snap", "filter": ".[", "max_items": -1}, "unexpected"},
		{"max_items before max_bytes", map[string]any{"source": "snap", "filter": ".[]", "max_items": -1, "max_bytes": -1}, "invalid max_items"},
		{"max_bytes before timeout", map[string]any{"source": "snap", "filter": ".[]", "max_bytes": -1, "timeout": "soon"}, "invalid max_bytes"},
		{"timeout before source", map[string]any{"source": "nope", "filter": ".[]", "timeout": "soon"}, "invalid timeout"},
		{"source before the unbounded refusal", map[string]any{"source": "nope", "filter": "length"}, `unknown source "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
			body := errorBodyOf(t, callMCP(t, cs, "iq_query", tt.args))
			require.Contains(t, body.Error.Message, tt.want)
		})
	}
}

func TestMCPQueryCrossSourceLoadError(t *testing.T) {
	seedMCP(t)
	corruptConfig(t, "")
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
	body := errorBodyOf(t, callMCP(t, cs, "iq_query", map[string]any{"filter": `source("snap"; ".[]")`}))
	require.Contains(t, body.Error.Message, "parse config")
}

func TestMCPQueryRunsUnderTheCallDeadline(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		maxWait time.Duration
	}{
		{"the server timeout", map[string]any{"source": "a", "filter": ".[]"}, 30 * time.Second},
		{"a longer call timeout is clamped", map[string]any{"source": "a", "filter": ".[]", "timeout": "1h"}, 30 * time.Second},
		{"a shorter call timeout lowers it", map[string]any{"source": "a", "filter": ".[]", "timeout": "5s"}, 5 * time.Second},
		{"a cross-source filter", map[string]any{"filter": `source("a"; ".[]")`, "timeout": "5s"}, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := useCtxDriver(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
			res := callMCP(t, cs, "iq_query", tt.args)
			require.False(t, res.IsError, contentText(res))

			require.NotEmpty(t, rec.ctxs["ctxrec://a"])
			for _, ctx := range rec.ctxs["ctxrec://a"] {
				dl, ok := ctx.Deadline()
				require.True(t, ok, "every open and read runs under the deadline of the call")
				require.LessOrEqual(t, time.Until(dl), tt.maxWait)
			}
		})
	}
}

func TestMCPExplainCheckOrder(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"filter before source", map[string]any{"source": "nope", "filter": " "}, "a filter is required"},
		{"syntax before source", map[string]any{"source": "nope", "filter": ".["}, "unexpected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
			body := errorBodyOf(t, callMCP(t, cs, "iq_explain", tt.args))
			require.Contains(t, body.Error.Message, tt.want)
		})
	}
}

func TestMCPDiffCheckOrder(t *testing.T) {
	tests := []struct {
		name string
		prep func(t *testing.T, data string)
		args map[string]any
		want string
	}{
		{"sample before the config load", corruptConfig, map[string]any{"a": "snap", "b": "snap", "sample": -1}, "invalid sample"},
		{"config load before the specs", corruptConfig, map[string]any{"a": "nosuch", "b": "snap"}, "parse config"},
		{"left before right", nil, map[string]any{"a": "ghost1", "b": "ghost2"}, `unknown source "ghost1"`},
		{"both specs parse before the stats refusal", nil, map[string]any{"a": "snap=.[]", "b": "ghost", "stats": true}, `unknown source "ghost"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := seedMCP(t)
			if tt.prep != nil {
				tt.prep(t, data)
			}
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
			body := errorBodyOf(t, callMCP(t, cs, "iq_diff", tt.args))
			require.Contains(t, body.Error.Message, tt.want)
		})
	}
}

func TestMCPInsertCheckOrder(t *testing.T) {
	both := []string{allowWrites, allowDestructive}
	tests := []struct {
		name  string
		allow []string
		args  map[string]any
		want  string
	}{
		{"source before destination", []string{allowWrites}, map[string]any{"source": " ", "destination": " "}, "insert: a source is required"},
		{"destination before replace", []string{allowWrites}, map[string]any{"source": "snap", "destination": " ", "replace": true}, "insert: a destination is required"},
		{"capability before the transform", []string{allowWrites}, map[string]any{"source": "snap", "destination": "snap", "replace": true, "filter": ".[ |"}, "destructive capability"},
		{"confirmation before the transform", both, map[string]any{"source": "snap", "destination": "snap", "replace": true, "filter": ".[ |"}, "needs confirmation"},
		{"confirmation before the source", both, map[string]any{"source": "nosuch", "destination": "snap", "replace": true}, "needs confirmation"},
		{"transform before the source", []string{allowWrites}, map[string]any{"source": "nosuch", "destination": "snap", "filter": ".[ |"}, "parse item filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedMCP(t)
			cs := connectMCP(t, newTestMCPServer(tt.allow, 200, 256*1024), nil)
			res := callMCP(t, cs, "iq_insert", tt.args)
			require.Contains(t, contentText(res), tt.want)
		})
	}
}

// TestMoveSourceErrors pins the two callers of the typed source open, the CLI
// move and iq_insert: the same text for a source that cannot be read, and a
// redacted URI for a source that cannot be opened.
func TestMoveSourceErrors(t *testing.T) {
	t.Run("iq_insert", func(t *testing.T) {
		useMoveDrivers(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)

		body := errorBodyOf(t, callMCP(t, cs, "iq_insert", map[string]any{"source": "bare", "destination": "full"}))
		require.Equal(t, "source bare (mvbare) cannot be read for a move", body.Error.Message)

		body = errorBodyOf(t, callMCP(t, cs, "iq_insert", map[string]any{"source": "down", "destination": "full"}))
		require.Contains(t, body.Error.Message, "mvfail://u:xxxxx@h")
		require.NotContains(t, body.Error.Message, "hunter2")
	})

	t.Run("the CLI move", func(t *testing.T) {
		useMoveDrivers(t)
		root, _ := newRootCmd()
		_, err := runCmd(t, root, "--src", "bare", "--insert", "full")
		require.EqualError(t, err, "source bare (mvbare) cannot be read for a move")

		root, _ = newRootCmd()
		_, err = runCmd(t, root, "--src", "down", "--insert", "full")
		require.ErrorContains(t, err, "mvfail://u:xxxxx@h")
		require.NotContains(t, err.Error(), "hunter2")
	})

	t.Run("a source that is read closes its store", func(t *testing.T) {
		log := useMoveDrivers(t)
		cs := connectMCP(t, newTestMCPServer([]string{allowWrites}, 200, 256*1024), nil)
		res := callMCP(t, cs, "iq_insert", map[string]any{"source": "full", "destination": "full", "dry_run": true})
		require.False(t, res.IsError, contentText(res))
		require.Equal(t, 2, log.closed, "the source store and the destination store")
	})
}
