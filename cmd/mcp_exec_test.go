package cmd

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/query"
)

// execStore is a store whose Query returns a canned reply and records the row
// limit that reached it.
type execStore struct {
	fakeInspectStore
	reply any
	seen  *int
	cut   bool // the store reports a row cut, the way a map-reply driver does
}

func (s execStore) Query(ctx context.Context, _ []string) (any, error) {
	if n, ok := query.RowLimit(ctx); ok {
		*s.seen = n
	}
	if s.cut {
		query.NoteRowCut(ctx)
	}
	return s.reply, nil
}

// useExecDriver registers the execfake scheme and a source named fake that
// answers every command with reply. The returned pointer holds the row limit
// that the driver saw, or zero when it saw none.
func useExecDriver(t *testing.T, reply any) *int {
	t.Helper()
	return useExecDriverCut(t, reply, false)
}

// useExecDriverCut is useExecDriver for a store that reports a row cut when cut
// is set.
func useExecDriverCut(t *testing.T, reply any, cut bool) *int {
	t.Helper()
	seen := new(int)
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name: "execfake", schemes: []string{"execfake"},
		open: func(context.Context, *config) (store, error) {
			return execStore{reply: reply, seen: seen, cut: cut}, nil
		},
	})
	c := newSeed()
	require.NoError(t, c.Add("fake", "execfake://h"))
	seedConfig(t, c)
	return seen
}

func execRows(n int) []any {
	rows := make([]any, n)
	for i := range rows {
		rows[i] = map[string]any{"id": i}
	}
	return rows
}

func TestMCPExecCapsTheReply(t *testing.T) {
	tests := []struct {
		name      string
		reply     any
		serverMax [2]int // items, bytes
		args      map[string]any
		wantRows  int // -1 when the result is not a list
		wantTrunc bool
		wantLimit int
	}{
		{
			name:      "a small list is whole and not marked",
			reply:     execRows(3),
			serverMax: [2]int{200, 256 * 1024},
			wantRows:  3,
			wantLimit: 201,
		},
		{
			name:      "a list at the item cap is not marked",
			reply:     execRows(3),
			serverMax: [2]int{3, 256 * 1024},
			wantRows:  3,
			wantLimit: 4,
		},
		{
			name:      "a list over the item cap is cut and marked",
			reply:     execRows(5),
			serverMax: [2]int{3, 256 * 1024},
			wantRows:  3,
			wantTrunc: true,
			wantLimit: 4,
		},
		{
			name:      "a list over the byte cap is cut and marked",
			reply:     execRows(5),
			serverMax: [2]int{200, 20},
			wantRows:  2,
			wantTrunc: true,
			wantLimit: 201,
		},
		{
			name:      "a per-call max_items lowers the cap",
			reply:     execRows(5),
			serverMax: [2]int{200, 256 * 1024},
			args:      map[string]any{"max_items": 2},
			wantRows:  2,
			wantTrunc: true,
			wantLimit: 3,
		},
		{
			name:      "a per-call max_items cannot raise the cap",
			reply:     execRows(5),
			serverMax: [2]int{2, 256 * 1024},
			args:      map[string]any{"max_items": 100},
			wantRows:  2,
			wantTrunc: true,
			wantLimit: 3,
		},
		{
			name:      "a per-call max_bytes lowers the cap",
			reply:     execRows(5),
			serverMax: [2]int{200, 256 * 1024},
			args:      map[string]any{"max_bytes": 20},
			wantRows:  2,
			wantTrunc: true,
			wantLimit: 201,
		},
		{
			name:      "a small document is whole and not marked",
			reply:     map[string]any{"ok": "yes"},
			serverMax: [2]int{200, 256 * 1024},
			wantRows:  -1,
			wantLimit: 201,
		},
		{
			name:      "a document over the byte cap is null and marked",
			reply:     map[string]any{"blob": strings.Repeat("x", 100)},
			serverMax: [2]int{200, 50},
			wantRows:  -1,
			wantTrunc: true,
			wantLimit: 201,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen := useExecDriver(t, tc.reply)
			cs := connectMCP(t, newTestMCPServer([]string{allowExec}, tc.serverMax[0], tc.serverMax[1]), nil)
			args := map[string]any{"source": "fake", "args": []string{"SELECT * FROM t"}}
			maps.Copy(args, tc.args)

			var out mcpExecOutput
			structOf(t, callMCP(t, cs, "iq_exec", args), &out)
			require.Equal(t, tc.wantTrunc, out.Truncated)
			require.Equal(t, tc.wantLimit, *seen)
			switch {
			case tc.wantRows >= 0:
				require.Len(t, out.Result, tc.wantRows)
			case tc.wantTrunc:
				require.Nil(t, out.Result)
			default:
				require.Equal(t, tc.reply, out.Result)
			}
		})
	}
}

func TestMCPExecRejectsNegativeCap(t *testing.T) {
	useExecDriver(t, execRows(1))
	cs := connectMCP(t, newTestMCPServer([]string{allowExec}, 200, 256*1024), nil)
	res := callMCP(t, cs, "iq_exec", map[string]any{"source": "fake", "args": []string{"x"}, "max_items": -1})
	require.True(t, res.IsError)
	require.Contains(t, contentText(res), "max_items")
}

func TestMCPExecRejectsAReplyThatCannotBeEncoded(t *testing.T) {
	tests := []struct {
		name    string
		reply   any
		wantErr string
	}{
		{name: "a document with a channel", reply: map[string]any{"c": make(chan int)}, wantErr: "encode exec result"},
		{name: "a list with a function item", reply: []any{func() {}}, wantErr: "encode result item"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useExecDriver(t, tc.reply)
			cs := connectMCP(t, newTestMCPServer([]string{allowExec}, 200, 256*1024), nil)
			res := callMCP(t, cs, "iq_exec", map[string]any{"source": "fake", "args": []string{"x"}})
			require.True(t, res.IsError)
			require.Contains(t, contentText(res), tc.wantErr)
		})
	}
}

func TestMCPExecCapEdges(t *testing.T) {
	tests := []struct {
		name      string
		reply     any
		maxItems  int
		maxBytes  int
		want      any
		wantTrunc bool
	}{
		// The encoding is {"ok":"yes"}, 12 bytes.
		{name: "a document of exactly max_bytes is whole", reply: map[string]any{"ok": "yes"}, maxItems: 200, maxBytes: 12, want: map[string]any{"ok": "yes"}},
		// A byte slice encodes as one base64 string, "YWJjZGVmZ2g=", 14 bytes.
		{name: "a byte slice under the cap is whole", reply: []byte("abcdefgh"), maxItems: 200, maxBytes: 14, want: "YWJjZGVmZ2g="},
		{name: "a byte slice over the cap is null and marked", reply: []byte("abcdefgh"), maxItems: 200, maxBytes: 13, wantTrunc: true},
		{name: "an empty list is an empty array", reply: []any{}, maxItems: 200, maxBytes: 256 * 1024, want: []any{}},
		// The long string goes over the byte cap. The function item would fail
		// to encode, so the run must stop before it.
		{name: "the run stops at the cap", reply: []any{1, 2, strings.Repeat("x", 50), func() {}}, maxItems: 200, maxBytes: 20, want: []any{float64(1), float64(2)}, wantTrunc: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useExecDriver(t, tc.reply)
			cs := connectMCP(t, newTestMCPServer([]string{allowExec}, tc.maxItems, tc.maxBytes), nil)
			res := callMCP(t, cs, "iq_exec", map[string]any{"source": "fake", "args": []string{"x"}})
			require.False(t, res.IsError)
			var out mcpExecOutput
			structOf(t, res, &out)
			require.Equal(t, tc.wantTrunc, out.Truncated)
			require.Equal(t, tc.want, out.Result)
		})
	}
}

// TestCapExecDocumentWrapsTheEncodeError checks that the encode error keeps its
// cause, so a caller can still find the json error with errors.As.
func TestCapExecDocumentWrapsTheEncodeError(t *testing.T) {
	_, err := capExecDocument(map[string]any{"c": make(chan int)}, queryBounds{maxItems: 10, maxBytes: 1 << 10})

	var jsonErr *json.UnsupportedTypeError
	require.ErrorAs(t, err, &jsonErr)
	require.ErrorContains(t, err, "encode exec result")
}

func TestMCPExecMarksACutMapReply(t *testing.T) {
	tests := []struct {
		name      string
		cut       bool
		wantTrunc bool
	}{
		{"a map the driver cut is marked", true, true},
		{"a map the driver did not cut is not marked", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := map[string]any{"docs": []any{"a", "b"}}
			useExecDriverCut(t, reply, tt.cut)
			cs := connectMCP(t, newTestMCPServer([]string{allowExec}, 200, 256*1024), nil)

			var out mcpExecOutput
			structOf(t, callMCP(t, cs, "iq_exec", map[string]any{"source": "fake", "args": []string{"x"}}), &out)

			require.Equal(t, tt.wantTrunc, out.Truncated)
			require.Equal(t, reply, out.Result)
		})
	}
}
