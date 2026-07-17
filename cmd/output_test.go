package cmd

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSelectFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         config
		want        outputFormat
		wantErr     bool
		errContains string
	}{
		{name: "none defaults to json", cfg: config{}, want: formatJSON},
		{name: "json", cfg: config{json: true}, want: formatJSON},
		{name: "jsona", cfg: config{jsonArray: true}, want: formatJSONArray},
		{name: "jsonl", cfg: config{jsonl: true}, want: formatJSONL},
		{name: "yaml", cfg: config{yaml: true}, want: formatYAML},
		{name: "raw selects the values rendering", cfg: config{raw: true}, want: formatValues},
		{name: "format json", cfg: config{format: "json"}, want: formatJSON},
		{name: "format jsonl", cfg: config{format: "jsonl"}, want: formatJSONL},
		{name: "format jsona", cfg: config{format: "jsona"}, want: formatJSONArray},
		{name: "format yaml", cfg: config{format: "yaml"}, want: formatYAML},
		{name: "format values", cfg: config{format: "values"}, want: formatValues},
		{name: "format raw aliases values", cfg: config{format: "raw"}, want: formatValues},
		{name: "format is case-insensitive and trimmed", cfg: config{format: " JSON "}, want: formatJSON},
		{name: "gron", cfg: config{gron: true}, want: formatGron},
		{name: "grona", cfg: config{gronArray: true}, want: formatGronArray},
		{name: "format gron", cfg: config{format: "gron"}, want: formatGron},
		{name: "format grona", cfg: config{format: "grona"}, want: formatGronArray},
		{name: "invalid format value errors", cfg: config{format: "csv"}, wantErr: true, errContains: `invalid --format "csv"`},
		{name: "two booleans is a conflict", cfg: config{json: true, yaml: true}, wantErr: true, errContains: "--format, --json, --jsona, --jsonl, --yaml, --raw, --gron, --grona"},
		{name: "format with a boolean is a conflict", cfg: config{format: "json", jsonl: true}, wantErr: true, errContains: "--format, --json, --jsona, --jsonl, --yaml, --raw, --gron, --grona"},
		{name: "gron with json is a conflict", cfg: config{gron: true, json: true}, wantErr: true, errContains: "--format, --json, --jsona, --jsonl, --yaml, --raw, --gron, --grona"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := selectFormat(&tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestValidateFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "empty is unset and allowed", in: ""},
		{name: "whitespace is treated as unset", in: "   "},
		{name: "json", in: "json"},
		{name: "jsonl", in: "jsonl"},
		{name: "jsona", in: "jsona"},
		{name: "yaml", in: "yaml"},
		{name: "values", in: "values"},
		{name: "raw alias", in: "raw"},
		{name: "gron", in: "gron"},
		{name: "grona", in: "grona"},
		{name: "case-insensitive and trimmed", in: " YAML "},
		{name: "unknown value errors", in: "csv", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateFormat(tt.in)
			if tt.wantErr {
				require.ErrorContains(t, err, "invalid --format")
				return
			}
			require.NoError(t, err)
		})
	}
}

// mustBigInt parses s as an arbitrary-precision integer, failing the test if it
// is not a valid integer literal.
func mustBigInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("invalid big integer literal: " + s)
	}
	return n
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
			name: "jsona wraps values in one document",
			fmt:  formatJSONArray,
			vals: []any{obj, "bob"},
			want: "[\n  {\n    \"name\": \"alice\",\n    \"year\": 2020\n  },\n  \"bob\"\n]\n",
		},
		{
			name: "jsona of one value",
			fmt:  formatJSONArray,
			vals: []any{"bob"},
			want: "[\n  \"bob\"\n]\n",
		},
		{
			name: "jsona of nothing is empty",
			fmt:  formatJSONArray,
			vals: nil,
			want: "[]\n",
		},
		{
			name:    "jsona compact is a single line",
			fmt:     formatJSONArray,
			compact: true,
			vals:    []any{obj, "bob"},
			want:    "[{\"name\":\"alice\",\"year\":2020},\"bob\"]\n",
		},
		{
			name:    "jsona compact of one value",
			fmt:     formatJSONArray,
			compact: true,
			vals:    []any{"bob"},
			want:    "[\"bob\"]\n",
		},
		{
			name:    "jsona compact of nothing is empty",
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
		{
			name: "gron flattens nested, sorts keys, declares empty containers",
			fmt:  formatGron,
			vals: []any{map[string]any{"b": []any{1, "x"}, "a": map[string]any{}}},
			want: "json = {};\njson.a = {};\njson.b = [];\njson.b[0] = 1;\njson.b[1] = \"x\";\n",
		},
		{
			name: "gron quotes non-identifier keys and keeps them verbatim",
			fmt:  formatGron,
			vals: []any{map[string]any{"odd key": true, "q\"uote": "a<b>&c", "_x$1": 1, "π": 2}},
			want: "json = {};\njson._x$1 = 1;\njson[\"odd key\"] = true;\njson[\"q\\\"uote\"] = \"a<b>&c\";\njson[\"π\"] = 2;\n",
		},
		{
			name: "gron of an empty top-level array declares only the root",
			fmt:  formatGron,
			vals: []any{[]any{}},
			want: "json = [];\n",
		},
		{
			name: "gron renders a big integer bare",
			fmt:  formatGron,
			vals: []any{mustBigInt("12345678901234567890")},
			want: "json = 12345678901234567890;\n",
		},
		{
			name: "gron repeats the json root across results, including nil",
			fmt:  formatGron,
			vals: []any{nil, "s"},
			want: "json = null;\njson = \"s\";\n",
		},
		{
			name:    "gron ignores compact",
			fmt:     formatGron,
			compact: true,
			vals:    []any{map[string]any{"b": []any{1, "x"}, "a": map[string]any{}}},
			want:    "json = {};\njson.a = {};\njson.b = [];\njson.b[0] = 1;\njson.b[1] = \"x\";\n",
		},
		{
			name: "grona indexes each result under one array declaration",
			fmt:  formatGronArray,
			vals: []any{map[string]any{"a": 1}, "s"},
			want: "json = [];\njson[0] = {};\njson[0].a = 1;\njson[1] = \"s\";\n",
		},
		{
			name: "grona of an empty stream declares an empty array",
			fmt:  formatGronArray,
			vals: nil,
			want: "json = [];\n",
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
	// Without flush, jsona and yaml leave the document unterminated; flush is
	// what closes them, so callers must run it after the engine finishes.
	t.Run("jsona is unterminated before flush", func(t *testing.T) {
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
	t.Run("gron streams fully so flush adds nothing", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		fm := newFormatter(formatGron, &b, false)
		require.NoError(t, fm.emit("bob"))
		before := b.String()
		require.NoError(t, fm.flush())
		require.Equal(t, before, b.String())
	})
	t.Run("grona flush after an emit adds nothing", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		fm := newFormatter(formatGronArray, &b, false)
		require.NoError(t, fm.emit("bob"))
		before := b.String()
		require.NoError(t, fm.flush())
		require.Equal(t, before, b.String())
	})
}

func TestGronFormatterPropagatesWriteErrors(t *testing.T) {
	t.Parallel()
	// Each gron statement is one Write, so failAt isolates a single guard: if any
	// error return were dropped, the later writes still succeed and the missing
	// propagation surfaces as a nil result. The value/failAt pairs cover every
	// write-error guard in emit, walk, and line.
	tests := []struct {
		name string
		fmt  outputFormat
		val  any
		at   int
	}{
		{name: "gron map declaration write fails", fmt: formatGron, val: map[string]any{"a": 1}, at: 1},
		{name: "gron map child recursion write fails", fmt: formatGron, val: map[string]any{"a": 1}, at: 2},
		{name: "gron array declaration write fails", fmt: formatGron, val: []any{1}, at: 1},
		{name: "gron array element recursion write fails", fmt: formatGron, val: []any{1}, at: 2},
		{name: "grona leading declaration write fails", fmt: formatGronArray, val: "s", at: 1},
		{name: "grona walk propagation write fails", fmt: formatGronArray, val: "s", at: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fm := newFormatter(tt.fmt, &failAt{at: tt.at}, false)
			require.ErrorContains(t, fm.emit(tt.val), "write result")
		})
	}
}

func TestGronFormatterPropagatesEncodeError(t *testing.T) {
	t.Parallel()
	// A value that cannot JSON-encode (a channel) must surface the compact
	// encoder's error rather than being swallowed on the walk's default path.
	for _, f := range []outputFormat{formatGron, formatGronArray} {
		t.Run(string(f), func(t *testing.T) {
			t.Parallel()
			fm := newFormatter(f, &bytes.Buffer{}, false)
			require.ErrorContains(t, fm.emit(make(chan int)), "encode result")
		})
	}
}

func TestRootRejectsMutuallyExclusiveFormatFlags(t *testing.T) {
	t.Parallel()
	// Cobra's MarkFlagsMutuallyExclusive rejects conflicting format flags during
	// Execute, before any store I/O; the new gron/grona flags join that group.
	tests := []struct {
		name string
		args []string
	}{
		{name: "gron with json", args: []string{"--gron", "--json", "."}},
		{name: "grona with raw", args: []string{"--grona", "--raw", "."}},
		{name: "gron with grona", args: []string{"--gron", "--grona", "."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, _ := newRootCmd()
			_, err := runCmd(t, root, tt.args...)
			require.ErrorContains(t, err, "none of the others can be")
		})
	}
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

func TestWriteStructuredPropagatesWriteError(t *testing.T) {
	t.Parallel()
	// A failing writer must surface the encoder error on both the YAML and JSON
	// paths, rather than being swallowed.
	require.Error(t, writeStructured(&errAfter{0}, map[string]any{"a": 1}, true))
	require.Error(t, writeStructured(&errAfter{0}, map[string]any{"a": 1}, false))
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
