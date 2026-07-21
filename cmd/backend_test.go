package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
)

// ctxKey marks a context so a fake can prove the caller's ctx (not a substituted
// nil) reached the delegate.
type ctxKey struct{}

// markedCtx returns a context carrying the ctxKey marker.
func markedCtx() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, "marker")
}

// fakeStore is a minimal in-memory store for the logging-decorator tests. It
// records the ctx each port call received, so a test can prove the caller's ctx
// was forwarded; scanErr fires from ScanBatches after its pages, distinct from a
// per-batch fn error.
type fakeStore struct {
	pages    []map[string]any
	getVal   map[string]any
	err      error // returned by Get/Query
	scanErr  error // returned by ScanBatches/ScanFiltered after the pages
	closeErr error // returned by Close
	closed   bool
	gotCtx   context.Context
	gotQuery []string
}

func (s *fakeStore) Get(ctx context.Context, keys []string) (map[string]any, error) {
	s.gotCtx = ctx
	if s.err != nil {
		return nil, s.err
	}
	if s.getVal != nil {
		return s.getVal, nil
	}
	out := map[string]any{}
	for _, k := range keys {
		out[k] = k + "-val"
	}
	return out, nil
}

func (s *fakeStore) ScanBatches(ctx context.Context, fn func(map[string]any) error) error {
	s.gotCtx = ctx
	for _, p := range s.pages {
		if err := fn(p); err != nil {
			return err
		}
	}
	return s.scanErr
}

func (s *fakeStore) Query(ctx context.Context, args []string) (any, error) {
	s.gotCtx = ctx
	s.gotQuery = args
	if s.err != nil {
		return nil, s.err
	}
	return args, nil
}

func (s *fakeStore) Close() error                   { s.closed = true; return s.closeErr }
func (s *fakeStore) FormatRaw(v any, _ bool) string { return "raw" }

// scanFilteredStore adds the FilteredScanner capability.
type scanFilteredStore struct{ *fakeStore }

func (s scanFilteredStore) ScanFiltered(ctx context.Context, _ predicate.Node, fn func(map[string]any) error) error {
	s.gotCtx = ctx
	for _, p := range s.pages {
		if err := fn(p); err != nil {
			return err
		}
	}
	return s.scanErr
}

// estimatorStore adds the Estimator capability.
type estimatorStore struct{ *fakeStore }

func (s estimatorStore) EstimateCount(context.Context) (int64, error) { return 42, nil }

// fullStore has both optional capabilities, like most live backends.
type fullStore struct {
	scanFilteredStore
	est int64
}

func (s fullStore) EstimateCount(context.Context) (int64, error) { return s.est, nil }

// captureRecords returns a DEBUG logger writing JSON to buf, for asserting the
// decorator's records.
func captureRecords(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestLoggingStorePreservesCapabilities(t *testing.T) {
	base := &fakeStore{}
	tests := []struct {
		name         string
		st           store
		wantFiltered bool
		wantEstim    bool
	}{
		{name: "plain store", st: base, wantFiltered: false, wantEstim: false},
		{name: "filtered only", st: scanFilteredStore{base}, wantFiltered: true, wantEstim: false},
		{name: "estimator only", st: estimatorStore{base}, wantFiltered: false, wantEstim: true},
		{name: "filtered and estimator", st: fullStore{scanFilteredStore{base}, 7}, wantFiltered: true, wantEstim: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newLoggingStore(tt.st, captureRecords(&bytes.Buffer{}))
			_, isFiltered := w.(query.FilteredScanner)
			_, isEstim := w.(query.Estimator)
			require.Equal(t, tt.wantFiltered, isFiltered, "FilteredScanner must match the delegate")
			require.Equal(t, tt.wantEstim, isEstim, "Estimator must match the delegate")
		})
	}
}

// TestWrapStoreLogging pins the Enabled gate: with a DEBUG sink the store is
// wrapped (and the ctx is forwarded to Enabled), without one it is returned as-is.
func TestWrapStoreLogging(t *testing.T) {
	base := &fakeStore{}

	// A DEBUG sink -> wrapped. gateHandler enables only when the ctx is non-nil, so
	// a substituted nil ctx would leave the store unwrapped.
	debug := slog.New(&gateHandler{inner: slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})})
	wrapped := wrapStoreLogging(markedCtx(), base, debug)
	require.NotSame(t, store(base), wrapped, "a DEBUG sink must wrap the store")
	_, ok := wrapped.(*loggingStore)
	require.True(t, ok, "wrapped store must be the logging decorator")

	// No DEBUG sink -> returned unchanged.
	info := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	require.Same(t, store(base), wrapStoreLogging(markedCtx(), base, info), "no DEBUG sink returns the store unwrapped")
}

func TestLoggingStoreForwardsCtxAndValues(t *testing.T) {
	// Every port call must forward the caller's ctx to the delegate and return the
	// delegate's values unchanged.
	t.Run("Get forwards ctx and returns the value map", func(t *testing.T) {
		base := &fakeStore{getVal: map[string]any{"k": "v"}}
		w := newLoggingStore(base, captureRecords(&bytes.Buffer{}))
		out, err := w.Get(markedCtx(), []string{"k"})
		require.NoError(t, err)
		require.Equal(t, map[string]any{"k": "v"}, out, "the delegate's map must be returned")
		require.Equal(t, "marker", base.gotCtx.Value(ctxKey{}), "the caller's ctx must reach the delegate")
	})

	t.Run("ScanBatches forwards ctx and delivers every batch", func(t *testing.T) {
		base := &fakeStore{pages: []map[string]any{{"a": 1, "b": 2}, {"c": 3}}}
		w := newLoggingStore(base, captureRecords(&bytes.Buffer{}))
		var seen []map[string]any
		require.NoError(t, w.ScanBatches(markedCtx(), func(b map[string]any) error {
			seen = append(seen, b)
			return nil
		}))
		require.Equal(t, base.pages, seen, "every batch must reach the caller's fn")
		require.Equal(t, "marker", base.gotCtx.Value(ctxKey{}))
	})

	t.Run("ScanFiltered forwards ctx and delivers every batch", func(t *testing.T) {
		base := &fakeStore{pages: []map[string]any{{"a": 1}, {"b": 2}}}
		w := newLoggingStore(scanFilteredStore{base}, captureRecords(&bytes.Buffer{}))
		var seen []map[string]any
		err := w.(query.FilteredScanner).ScanFiltered(markedCtx(), nil, func(b map[string]any) error {
			seen = append(seen, b)
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, base.pages, seen)
		require.Equal(t, "marker", base.gotCtx.Value(ctxKey{}))
	})

	t.Run("Query forwards ctx and args", func(t *testing.T) {
		base := &fakeStore{}
		w := newLoggingStore(base, captureRecords(&bytes.Buffer{}))
		_, err := w.Query(markedCtx(), []string{"GET", "k"})
		require.NoError(t, err)
		require.Equal(t, []string{"GET", "k"}, base.gotQuery, "the delegate must receive the args")
		require.Equal(t, "marker", base.gotCtx.Value(ctxKey{}))
	})
}

func TestLoggingStoreRecordsGetCounts(t *testing.T) {
	base := &fakeStore{getVal: map[string]any{"a": 1, "b": 2, "c": 3}}
	var buf bytes.Buffer
	w := newLoggingStore(base, captureRecords(&buf))
	_, err := w.Get(markedCtx(), []string{"x", "y"})
	require.NoError(t, err)
	rec := findRecord(t, &buf, "store call", "op", "Get")
	require.Equal(t, 2, jsonInt(rec["keys"]), "requested key count")
	require.Equal(t, 3, jsonInt(rec["returned"]), "returned value count")
	require.Contains(t, rec, "elapsed")
}

func TestLoggingStoreRecordsScanCounts(t *testing.T) {
	tests := []struct {
		name string
		op   string
		call func(store, func(map[string]any) error) error
	}{
		{
			name: "ScanBatches",
			op:   "ScanBatches",
			call: func(w store, fn func(map[string]any) error) error { return w.ScanBatches(markedCtx(), fn) },
		},
		{
			name: "ScanFiltered",
			op:   "ScanFiltered",
			call: func(w store, fn func(map[string]any) error) error {
				return w.(query.FilteredScanner).ScanFiltered(markedCtx(), nil, fn)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &fakeStore{pages: []map[string]any{{"a": 1, "b": 2}, {"c": 3}}}
			var buf bytes.Buffer
			w := newLoggingStore(scanFilteredStore{base}, captureRecords(&buf))
			require.NoError(t, tt.call(w, func(map[string]any) error { return nil }))
			rec := findRecord(t, &buf, "store call", "op", tt.op)
			require.Equal(t, 2, jsonInt(rec["pages"]), "one page per batch")
			require.Equal(t, 3, jsonInt(rec["items"]), "total items across pages")
		})
	}
}

func TestLoggingStoreQueryCommandSplit(t *testing.T) {
	// The command verb and operand count are logged; the operand values never are.
	// The count math is pinned across the len 0 / 1 / >1 boundaries.
	tests := []struct {
		name         string
		args         []string
		wantCommand  string
		wantOperands int
	}{
		{name: "no args: empty command, zero operands", args: []string{}, wantCommand: "", wantOperands: 0},
		{name: "verb only: command set, zero operands", args: []string{"KEYS"}, wantCommand: "KEYS", wantOperands: 0},
		{name: "verb and one operand", args: []string{"GET", "secret-value"}, wantCommand: "GET", wantOperands: 1},
		{name: "verb and two operands", args: []string{"MGET", "a", "b"}, wantCommand: "MGET", wantOperands: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := newLoggingStore(&fakeStore{}, captureRecords(&buf))
			_, err := w.Query(markedCtx(), tt.args)
			require.NoError(t, err)
			rec := findRecord(t, &buf, "store call", "op", "Query")
			require.Equal(t, tt.wantCommand, rec["command"])
			require.Equal(t, tt.wantOperands, jsonInt(rec["args"]))
			require.NotContains(t, buf.String(), "secret-value", "operand values must never be logged")
		})
	}
}

// jsonInt converts a JSON-decoded number (float64) to int for exact count
// assertions, keeping the comparison off testifylint's float-compare radar.
func jsonInt(v any) int { return int(v.(float64)) }

// TestLoggingStoreCloseDelegates pins that the decorator's Close reaches the
// wrapped store and propagates its error: a decorator that swallows Close would
// leak the backend connection silently.
func TestLoggingStoreCloseDelegates(t *testing.T) {
	closeBoom := errors.New("close failed")

	tests := []struct {
		name     string
		closeErr error
	}{
		{name: "nil error", closeErr: nil},
		{name: "error propagates", closeErr: closeBoom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			st := &fakeStore{closeErr: tt.closeErr}
			w := newLoggingStore(st, captureRecords(&buf))
			require.ErrorIs(t, w.Close(), tt.closeErr)
			require.True(t, st.closed, "Close must delegate to the wrapped store")
		})
	}
}

func TestLoggingStorePropagatesErrors(t *testing.T) {
	boom := errors.New("backend down")
	fnBoom := errors.New("caller stop")

	t.Run("Get error propagates and is recorded", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLoggingStore(&fakeStore{err: boom}, captureRecords(&buf))
		_, err := w.Get(markedCtx(), []string{"k"})
		require.ErrorIs(t, err, boom)
		rec := findRecord(t, &buf, "store call", "op", "Get")
		require.Contains(t, rec["err"], "backend down")
	})

	t.Run("ScanBatches store error propagates", func(t *testing.T) {
		var buf bytes.Buffer
		w := newLoggingStore(&fakeStore{pages: []map[string]any{{"a": 1}}, scanErr: boom}, captureRecords(&buf))
		err := w.ScanBatches(markedCtx(), func(map[string]any) error { return nil })
		require.ErrorIs(t, err, boom, "the store's scan error must propagate")
		rec := findRecord(t, &buf, "store call", "op", "ScanBatches")
		require.Contains(t, rec["err"], "backend down")
	})

	t.Run("ScanBatches fn error propagates and stops the scan", func(t *testing.T) {
		base := &fakeStore{pages: []map[string]any{{"a": 1}, {"b": 2}}}
		w := newLoggingStore(base, captureRecords(&bytes.Buffer{}))
		calls := 0
		err := w.ScanBatches(markedCtx(), func(map[string]any) error {
			calls++
			return fnBoom
		})
		require.ErrorIs(t, err, fnBoom, "the fn's error must propagate out")
		require.Equal(t, 1, calls, "the scan must stop after the first fn error")
	})
}

// gateHandler enables a level only when the record's context is non-nil, so a
// test can prove that the caller's real context (not a substituted nil) reached
// the handler's Enabled. It delegates Handle to an inner handler.
type gateHandler struct{ inner slog.Handler }

func (h *gateHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return ctx != nil && h.inner.Enabled(ctx, l)
}

func (h *gateHandler) Handle(ctx context.Context, r slog.Record) error { return h.inner.Handle(ctx, r) }

func (h *gateHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &gateHandler{inner: h.inner.WithAttrs(a)}
}

func (h *gateHandler) WithGroup(n string) slog.Handler {
	return &gateHandler{inner: h.inner.WithGroup(n)}
}

// findRecord parses buf's JSON lines and returns the first record whose msg
// matches and whose keyAttr equals want.
func findRecord(t *testing.T, buf *bytes.Buffer, msg, keyAttr, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if m["msg"] == msg && m[keyAttr] == want {
			return m
		}
	}
	t.Fatalf("no %q record with %s=%q in:\n%s", msg, keyAttr, want, buf.String())
	return nil
}

// TestTraceTeeLogsBackendCmdIntegration pins that a --log run tees the driver
// command trace into the log (jq.go's traceSink wiring): a query against a real
// Redis backend with --log.file=stderr --log.format=json must emit "backend cmd"
// DEBUG records for the wire commands, even without --verbose. It needs a reachable
// Redis (the file driver has no wire trace to observe).
func TestTraceTeeLogsBackendCmdIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: needs a reachable Redis")
	}
	url := os.Getenv("IQ_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	c := newSeed()
	require.NoError(t, c.Add("live", url))
	require.NoError(t, c.SetActive("live"))
	seedConfig(t, c)

	root, _ := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--src", "live", "--timeout", "5s", "--log.file=stderr", "--log.format=json", ".[]"})
	require.NoError(t, root.Execute())

	// Every stderr line is a JSON record; a scan issues at least one wire command,
	// which the trace tee turns into a "backend cmd" record tagged with the driver.
	var sawBackendCmd bool
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &rec), "stderr line not JSON: %q", line)
		if rec["msg"] == "backend cmd" {
			require.Equal(t, "redis", rec["driver"], "the wire command must be tagged with its driver")
			require.NotEmpty(t, rec["cmd"], "the command text must be logged")
			sawBackendCmd = true
		}
	}
	require.True(t, sawBackendCmd, "the trace must be teed into the log as backend cmd records")
}

// parseLogRecords indexes newline-delimited JSON log records by their msg.
func parseLogRecords(t *testing.T, s string) map[string]map[string]any {
	t.Helper()
	byMsg := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &rec), "stderr line not JSON: %q", line)
		if msg, ok := rec["msg"].(string); ok {
			byMsg[msg] = rec
		}
	}
	return byMsg
}

// TestRunJQWiresRunOptionsFileSource pins that runJQ passes the Logger and OnPage
// fields of RunOptions to the engine, using the offline file driver so it runs in
// this package (no container). Each field's observable effect is asserted
// separately: the Logger surfaces a "scan strategy" record, OnPage drives the
// "query complete" scanned count.
func TestRunJQWiresRunOptionsFileSource(t *testing.T) {
	seedFileSource(t, `{"key":"a","type":"hash","value":{"total":150}}`+"\n"+
		`{"key":"b","type":"hash","value":{"total":10}}`+"\n")

	root, _ := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"--src", "snap", "--log.file=stderr", "--log.format=json", ".[] | select(.total > 99)"})
	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), "150")

	byMsg := parseLogRecords(t, errb.String())

	t.Run("query plan record is emitted on the run path", func(t *testing.T) {
		plan := byMsg["query plan"]
		require.NotNil(t, plan, "runJQ must log the query plan when logging is active")
		require.Equal(t, "file", plan["driver"], "the plan must name the resolved driver")
	})

	t.Run("Logger reaches the engine (scan strategy record present)", func(t *testing.T) {
		stratRec := byMsg["scan strategy"]
		require.NotNil(t, stratRec, "RunOptions.Logger must be wired: the engine emits scan strategy")
		require.Equal(t, true, stratRec["pushed"], "the file driver pushes a pushable predicate")
	})

	t.Run("OnPage reaches the engine (scanned count > 0)", func(t *testing.T) {
		done := byMsg["query complete"]
		require.NotNil(t, done, "a query complete record must be logged")
		require.GreaterOrEqual(t, int(done["scanned"].(float64)), 1, "OnPage must count scanned items")
	})
}

// TestRunJQWiresUnboundedFileSource pins that runJQ passes RunOptions.Unbounded to
// the engine: a holistic filter (keys) materializes only when Unbounded is set, so
// with --unbounded the run must succeed. Without the field wired, the engine refuses
// the holistic scan and Execute returns an error.
func TestRunJQWiresUnboundedFileSource(t *testing.T) {
	seedFileSource(t, `{"key":"a","type":"string","value":1}`+"\n"+
		`{"key":"b","type":"string","value":2}`+"\n")

	root, _ := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"--src", "snap", "--unbounded", "keys"})
	require.NoError(t, root.Execute(), "Unbounded must reach the engine so a holistic scan is allowed")
	require.Contains(t, out.String(), "a", "the keys must be emitted")
}

func TestSchemeOf(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"redis", "redis://localhost:6379/0", "redis"},
		{"redis tls", "rediss://host:6379", "rediss"},
		{"mongodb", "mongodb://localhost:27017/iq", "mongodb"},
		{"mongodb srv", "mongodb+srv://host/iq", "mongodb+srv"},
		{"uppercase normalized", "REDIS://host", "redis"},
		{"multi-host mongo", "mongodb://h1,h2:27017/iq", "mongodb"},
		{"no scheme", "localhost:6379", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, schemeOf(tt.url))
		})
	}
}
