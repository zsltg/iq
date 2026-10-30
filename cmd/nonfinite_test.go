package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// nanStore is a store that reports conversions the way an adapter does. Its
// scan yields rows that hold the strings of converted values, and it tells the
// notice sink about each one. A row in hits holds the fields that it converted.
type nanStore struct {
	fakeInspectStore
	rows map[string]any
	hits map[string][]string
}

func (s nanStore) ScanBatches(ctx context.Context, fn func(map[string]any) error) error {
	for key, fields := range s.hits {
		query.NoteNonFinite(ctx, key, fields...)
	}
	return fn(s.rows)
}

func (s nanStore) Query(ctx context.Context, _ []string) (any, error) {
	for key, fields := range s.hits {
		query.NoteNonFinite(ctx, key, fields...)
	}
	return []any{map[string]any{"price": "Infinity"}}, nil
}

// useNanDriver registers the nanfake scheme and a source "n" on it.
func useNanDriver(t *testing.T, st nanStore) {
	t.Helper()
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name: "nanfake", schemes: []string{"nanfake"},
		open: func(context.Context, *config) (store, error) { return st, nil },
	})
	c := newSeed()
	require.NoError(t, c.Add("n", "nanfake://h"))
	seedConfig(t, c)
}

// runSplit runs the root command with separate stdout and stderr buffers.
func runSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root, _ := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestQueryWarnsOnceAboutConvertedValues(t *testing.T) {
	useNanDriver(t, nanStore{
		rows: map[string]any{"k1": map[string]any{"price": "NaN", "cost": "-Infinity"}},
		hits: map[string][]string{"k1": {"price", "cost"}},
	})

	stdout, stderr, err := runSplit(t, "--src", "n", ".[]")

	require.NoError(t, err)
	require.True(t, json.Valid([]byte(stdout)), stdout)
	require.NotContains(t, stdout, "warning")
	require.Contains(t, stdout, `"NaN"`)
	require.Equal(t,
		`warning: iq converted 2 values that JSON cannot hold (NaN or infinity) to strings; first: key "k1", field "price"`+"\n",
		stderr)
}

func TestQueryWithoutConversionWritesNoWarning(t *testing.T) {
	useNanDriver(t, nanStore{rows: map[string]any{"k1": map[string]any{"price": 1.5}}})

	stdout, stderr, err := runSplit(t, "--src", "n", ".[]")

	require.NoError(t, err)
	require.True(t, json.Valid([]byte(stdout)), stdout)
	require.Empty(t, stderr)
}

func TestExecWarnsAboutConvertedValues(t *testing.T) {
	useNanDriver(t, nanStore{hits: map[string][]string{"row 1": {"price"}}})

	stdout, stderr, err := runSplit(t, "exec", "--src", "n", "SELECT 1")

	require.NoError(t, err)
	require.Contains(t, stdout, "Infinity")
	require.Equal(t,
		`warning: iq converted 1 value that JSON cannot hold (NaN or infinity) to strings; first: key "row 1", field "price"`+"\n",
		stderr)
}

func TestMCPQueryNotesConvertedValues(t *testing.T) {
	useNanDriver(t, nanStore{
		rows: map[string]any{"k1": map[string]any{"price": "NaN"}},
		hits: map[string][]string{"k1": {"price"}},
	})
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	var out mcpQueryOutput
	structOf(t, callMCP(t, cs, "iq_query", map[string]any{"source": "n", "filter": ".[]"}), &out)

	require.Equal(t, 1, out.Count)
	require.Equal(t,
		`iq converted 1 value that JSON cannot hold (NaN or infinity) to strings; first: key "k1", field "price"`,
		out.Note)
}

func TestMCPQueryWithoutConversionHasNoNote(t *testing.T) {
	useNanDriver(t, nanStore{rows: map[string]any{"k1": map[string]any{"price": 1.5}}})
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_query", map[string]any{"source": "n", "filter": ".[]"})

	require.False(t, res.IsError, contentText(res))
	var raw map[string]any
	structOf(t, res, &raw)
	require.NotContains(t, raw, "note")
}

func TestMCPExecNotesConvertedValues(t *testing.T) {
	useNanDriver(t, nanStore{hits: map[string][]string{"row 1": {"price"}}})
	cs := connectMCP(t, newTestMCPServer([]string{allowExec}, 200, 256*1024), nil)

	var out mcpExecOutput
	structOf(t, callMCP(t, cs, "iq_exec", map[string]any{"source": "n", "args": []string{"SELECT 1"}}), &out)

	require.Equal(t,
		`iq converted 1 value that JSON cannot hold (NaN or infinity) to strings; first: key "row 1", field "price"`,
		out.Note)
}

func TestWarnNonFinite(t *testing.T) {
	summary := &query.NonFiniteSummary{}
	summary.Sink()("k1", "price")
	tests := []struct {
		name string
		cfg  *config
		want string
	}{
		{"no summary", &config{}, ""},
		{"empty summary", &config{nonFinite: &query.NonFiniteSummary{}}, ""},
		{"one conversion", &config{nonFinite: summary}, "warning: " + summary.Text() + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			warnNonFinite(&buf, tt.cfg)

			require.Equal(t, tt.want, buf.String())
		})
	}
}
