package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
)

// pushLog counts the scans that a pushrec store served.
type pushLog struct {
	mu       sync.Mutex
	filtered int
	plain    int
}

func (l *pushLog) count() (filtered, plain int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filtered, l.plain
}

// pushStore is a store that can filter a scan. It counts which scan it served.
type pushStore struct {
	fakeInspectStore
	log *pushLog
}

func (s pushStore) ScanBatches(_ context.Context, fn func(map[string]any) error) error {
	s.log.mu.Lock()
	s.log.plain++
	s.log.mu.Unlock()
	return fn(map[string]any{"k": map[string]any{"status": "new"}})
}

func (s pushStore) ScanFiltered(_ context.Context, _ predicate.Node, fn func(map[string]any) error) error {
	s.log.mu.Lock()
	s.log.filtered++
	s.log.mu.Unlock()
	return fn(map[string]any{"k": map[string]any{"status": "new"}})
}

// usePushDriver registers the pushrec scheme and a source "p" on it.
func usePushDriver(t *testing.T) *pushLog {
	t.Helper()
	log := &pushLog{}
	orig := drivers
	t.Cleanup(func() { drivers = orig })
	drivers = append(append([]driver{}, orig...), driver{
		name: "pushrec", schemes: []string{"pushrec"},
		open: func(context.Context, *config) (store, error) { return pushStore{log: log}, nil },
	})
	c := newSeed()
	require.NoError(t, c.Add("p", "pushrec://h"))
	seedConfig(t, c)
	return log
}

// syncBuffer is a log sink that the server goroutine and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMCPQueryPushdownFollowsTheCompileInput(t *testing.T) {
	off, on := false, true
	tests := []struct {
		name         string
		compile      *bool
		wantFiltered int
		wantPlain    int
	}{
		{"compile is on by default", nil, 1, 0},
		{"compile true pushes the filter", &on, 1, 0},
		{"compile false scans everything", &off, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := usePushDriver(t)
			cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)
			args := map[string]any{"source": "p", "filter": `.[] | select(.status == "new")`}
			if tt.compile != nil {
				args["compile"] = *tt.compile
			}

			var out mcpQueryOutput
			structOf(t, callMCP(t, cs, "iq_query", args), &out)

			require.Equal(t, 1, out.Count, "the full filter runs again on the client")
			filtered, plain := log.count()
			require.Equal(t, tt.wantFiltered, filtered, "filtered scans")
			require.Equal(t, tt.wantPlain, plain, "plain scans")
		})
	}
}

func TestMCPQueryLogsTheScanStrategy(t *testing.T) {
	usePushDriver(t)
	var sink syncBuffer
	s := newTestMCPServer(nil, 200, 256*1024)
	s.cfg.logger = slog.New(slog.NewTextHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cs := connectMCP(t, s, nil)

	res := callMCP(t, cs, "iq_query", map[string]any{"source": "p", "filter": `.[] | select(.status == "new")`})

	require.False(t, res.IsError, contentText(res))
	require.Contains(t, sink.String(), "scan strategy", "the engine logs through the run logger")
}

func TestMCPCrossQueryClosesItsSources(t *testing.T) {
	log := useMoveDrivers(t)
	cs := connectMCP(t, newTestMCPServer(nil, 200, 256*1024), nil)

	res := callMCP(t, cs, "iq_query", map[string]any{"filter": `source("full"; ".[]")`})

	require.False(t, res.IsError, contentText(res))
	require.Equal(t, 1, log.closed, "the source that source() opened is closed after the run")
}

func TestQueryCollectorEmit(t *testing.T) {
	t.Run("an item that cannot be encoded is an error", func(t *testing.T) {
		c := &queryCollector{bounds: queryBounds{maxItems: 10, maxBytes: 1000}}

		err := c.emit(math.NaN())

		require.ErrorContains(t, err, "encode result item")
		var unsupported *json.UnsupportedValueError
		require.ErrorAs(t, err, &unsupported)
		require.Empty(t, c.out.Items)
		require.False(t, c.out.Truncated)
	})

	t.Run("an item past the byte cap stops the run", func(t *testing.T) {
		c := &queryCollector{bounds: queryBounds{maxItems: 10, maxBytes: 5}}

		err := c.emit("longer than five bytes")

		require.ErrorIs(t, err, errTruncated)
		require.True(t, c.out.Truncated)
		require.Empty(t, c.out.Items)
		require.Zero(t, c.size)
	})

	t.Run("an item past the item cap stops the run", func(t *testing.T) {
		c := &queryCollector{bounds: queryBounds{maxItems: 1, maxBytes: 1000}}
		require.NoError(t, c.emit(1))

		err := c.emit(2)

		require.ErrorIs(t, err, errTruncated)
		require.True(t, c.out.Truncated)
		require.Len(t, c.out.Items, 1)
	})
}

func TestQueryRunFinish(t *testing.T) {
	r := queryRun{filter: ".[]"}

	require.NoError(t, r.finish(nil))
	require.NoError(t, r.finish(errTruncated))
	require.Error(t, r.finish(context.Canceled))
}

func TestMCPDiffLayers(t *testing.T) {
	tests := []struct {
		name string
		in   mcpDiffInput
		want diffModes
	}{
		{"no layer selects data", mcpDiffInput{}, diffModes{data: true}},
		{"data alone", mcpDiffInput{Data: true}, diffModes{data: true}},
		{"stats alone", mcpDiffInput{Stats: true}, diffModes{stats: true}},
		{"schema alone", mcpDiffInput{Schema: true}, diffModes{schema: true}},
		{"data with stats", mcpDiffInput{Data: true, Stats: true}, diffModes{data: true, stats: true}},
		{"data with schema", mcpDiffInput{Data: true, Schema: true}, diffModes{data: true, schema: true}},
		{"stats with schema", mcpDiffInput{Stats: true, Schema: true}, diffModes{stats: true, schema: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.in.layers())
		})
	}
}
