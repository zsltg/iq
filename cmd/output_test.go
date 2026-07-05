package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSelectFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     config
		want    outputFormat
		wantErr bool
	}{
		{name: "none defaults to json", cfg: config{}, want: formatJSON},
		{name: "json", cfg: config{json: true}, want: formatJSON},
		{name: "json-array", cfg: config{jsonArray: true}, want: formatJSONArray},
		{name: "jsonl", cfg: config{jsonl: true}, want: formatJSONL},
		{name: "yaml", cfg: config{yaml: true}, want: formatYAML},
		{name: "raw selects the values rendering", cfg: config{raw: true}, want: formatValues},
		{name: "two set is a conflict", cfg: config{json: true, yaml: true}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := selectFormat(&tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				// The message names every format flag so the user can recover.
				require.Contains(t, err.Error(), "--json, --json-array, --jsonl, --yaml, --raw")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// render runs the formatter for f (compact toggling single-line output) over
// vals (emit each, then flush) and returns everything written.
func renderFormatter(t *testing.T, f outputFormat, compact bool, vals ...any) string {
	t.Helper()
	var b bytes.Buffer
	fm := newFormatter(f, &b, compact)
	for _, v := range vals {
		require.NoError(t, fm.emit(v))
	}
	require.NoError(t, fm.flush())
	return b.String()
}

func TestFormatterOutput(t *testing.T) {
	t.Parallel()
	obj := map[string]any{"name": "alice", "year": 2020}
	tests := []struct {
		name    string
		fmt     outputFormat
		compact bool
		vals    []any
		want    string
	}{
		{
			name: "json is a pretty stream",
			fmt:  formatJSON,
			vals: []any{obj, "bob"},
			want: "{\n  \"name\": \"alice\",\n  \"year\": 2020\n}\n\"bob\"\n",
		},
		{
			name:    "json compact is one value per line",
			fmt:     formatJSON,
			compact: true,
			vals:    []any{obj, "bob"},
			want:    "{\"name\":\"alice\",\"year\":2020}\n\"bob\"\n",
		},
		{
			name: "json keeps angle brackets verbatim",
			fmt:  formatJSON,
			vals: []any{"a<b>&c"},
			want: "\"a<b>&c\"\n",
		},
		{
			name: "jsonl is one compact value per line",
			fmt:  formatJSONL,
			vals: []any{obj, "bob"},
			want: "{\"name\":\"alice\",\"year\":2020}\n\"bob\"\n",
		},
		{
			name: "json-array wraps values in one document",
			fmt:  formatJSONArray,
			vals: []any{obj, "bob"},
			want: "[\n  {\n    \"name\": \"alice\",\n    \"year\": 2020\n  },\n  \"bob\"\n]\n",
		},
		{
			name: "json-array of one value",
			fmt:  formatJSONArray,
			vals: []any{"bob"},
			want: "[\n  \"bob\"\n]\n",
		},
		{
			name: "json-array of nothing is empty",
			fmt:  formatJSONArray,
			vals: nil,
			want: "[]\n",
		},
		{
			name:    "json-array compact is a single line",
			fmt:     formatJSONArray,
			compact: true,
			vals:    []any{obj, "bob"},
			want:    "[{\"name\":\"alice\",\"year\":2020},\"bob\"]\n",
		},
		{
			name:    "json-array compact of one value",
			fmt:     formatJSONArray,
			compact: true,
			vals:    []any{"bob"},
			want:    "[\"bob\"]\n",
		},
		{
			name:    "json-array compact of nothing is empty",
			fmt:     formatJSONArray,
			compact: true,
			vals:    nil,
			want:    "[]\n",
		},
		{
			name: "values prints a string unquoted",
			fmt:  formatValues,
			vals: []any{"alice"},
			want: "alice\n",
		},
		{
			name: "values prints numbers and bools bare",
			fmt:  formatValues,
			vals: []any{42, 3.5, true, false},
			want: "42\n3.5\ntrue\nfalse\n",
		},
		{
			name: "values prints nil as an empty line",
			fmt:  formatValues,
			vals: []any{nil},
			want: "\n",
		},
		{
			name: "values keeps a string verbatim without escaping",
			fmt:  formatValues,
			vals: []any{"a<b>&c"},
			want: "a<b>&c\n",
		},
		{
			name: "values falls back to compact JSON for composites",
			fmt:  formatValues,
			vals: []any{obj, []any{1, 2}},
			want: "{\"name\":\"alice\",\"year\":2020}\n[1,2]\n",
		},
		{
			name: "yaml separates documents with a marker",
			fmt:  formatYAML,
			vals: []any{obj, "bob"},
			want: "name: alice\nyear: 2020\n---\nbob\n",
		},
		{
			name:    "yaml ignores compact",
			fmt:     formatYAML,
			compact: true,
			vals:    []any{obj, "bob"},
			want:    "name: alice\nyear: 2020\n---\nbob\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, renderFormatter(t, tt.fmt, tt.compact, tt.vals...))
		})
	}
}

func TestFlushCompletesDocument(t *testing.T) {
	t.Parallel()
	// Without flush, json-array and yaml leave the document unterminated; flush is
	// what closes them, so callers must run it after the engine finishes.
	t.Run("json-array is unterminated before flush", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		fm := newFormatter(formatJSONArray, &b, false)
		require.NoError(t, fm.emit("bob"))
		require.NotContains(t, b.String(), "]")
		require.NoError(t, fm.flush())
		require.Equal(t, "[\n  \"bob\"\n]\n", b.String())
	})
	t.Run("yaml flush closes the encoder cleanly", func(t *testing.T) {
		t.Parallel()
		// yaml.v3 writes each document on emit, so flush adds no bytes; it must
		// still close the encoder without error to release it.
		var b bytes.Buffer
		fm := newFormatter(formatYAML, &b, false)
		require.NoError(t, fm.emit("bob"))
		require.NoError(t, fm.flush())
		require.Equal(t, "bob\n", b.String())
	})
}

func TestFinish(t *testing.T) {
	t.Parallel()
	t.Run("returns the run error and skips flush", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		f := newFormatter(formatJSONArray, &b, false)
		sentinel := errors.New("boom")
		require.ErrorIs(t, finish(f, sentinel), sentinel)
		require.Empty(t, b.String()) // flush skipped, so the array is never closed
	})
	t.Run("flushes when the run succeeded", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		f := newFormatter(formatJSONArray, &b, false)
		require.NoError(t, finish(f, nil))
		require.Equal(t, "[]\n", b.String()) // flush closed the empty array
	})
}

func TestRunRejectsConflictingFormat(t *testing.T) {
	t.Parallel()
	// Both run paths resolve the format before touching a store, so conflicting
	// format flags are rejected without any source I/O.
	t.Run("runJQ", func(t *testing.T) {
		t.Parallel()
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.SetOut(&bytes.Buffer{})
		err := runJQ(cmd, &config{json: true, raw: true, timeout: time.Second}, ".")
		require.ErrorContains(t, err, "mutually exclusive")
	})
	t.Run("runCombine", func(t *testing.T) {
		t.Parallel()
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.SetOut(&bytes.Buffer{})
		err := runCombine(cmd, &config{from: []string{"x=."}, combine: "$x", json: true, raw: true, timeout: time.Second})
		require.ErrorContains(t, err, "mutually exclusive")
	})
}
