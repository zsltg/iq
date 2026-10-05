package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// errHandler is a stub slog.Handler that always accepts a record and returns a
// fixed error from Handle, so the fanout's error joining can be exercised.
type errHandler struct{ err error }

func (h errHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h errHandler) Handle(context.Context, slog.Record) error { return h.err }
func (h errHandler) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h errHandler) WithGroup(string) slog.Handler             { return h }

// TestFanoutHandleJoinsChildErrors pins that a child handler's error propagates
// out of the fanout (and a nil-returning child contributes nothing).
func TestFanoutHandleJoinsChildErrors(t *testing.T) {
	boom := errors.New("boom")
	h := newFanout(errHandler{err: boom}, errHandler{err: nil})

	err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "m", 0))
	require.ErrorIs(t, err, boom)
}

// TestFanoutRoutesByChildLevel pins the core fanout contract: each child gates on
// its own level, so a DEBUG record reaches the DEBUG child only while an INFO
// record reaches both.
func TestFanoutRoutesByChildLevel(t *testing.T) {
	var info, debug bytes.Buffer
	logger := slog.New(newFanout(
		slog.NewTextHandler(&info, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&debug, &slog.HandlerOptions{Level: slog.LevelDebug}),
	))

	logger.Debug("dbg")
	require.Empty(t, info.String(), "INFO sink must skip a DEBUG record")
	require.Contains(t, debug.String(), "dbg", "DEBUG sink must receive it")

	info.Reset()
	debug.Reset()
	logger.Info("nfo")
	require.Contains(t, info.String(), "nfo")
	require.Contains(t, debug.String(), "nfo")
}

// TestFanoutWithAttrsAndGroup checks that WithAttrs and WithGroup propagate to
// every child, so attributes and groups appear in both sinks.
func TestFanoutWithAttrsAndGroup(t *testing.T) {
	var a, b bytes.Buffer
	logger := slog.New(newFanout(
		slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelDebug}),
		slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)).With("k", "v").WithGroup("g")

	logger.Info("m", "x", 1)

	for _, out := range []string{a.String(), b.String()} {
		require.Contains(t, out, "k")
		require.Contains(t, out, "g")
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"DEBUG", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{" warn ", slog.LevelWarn, false},
		{"WARNING", slog.LevelWarn, false},
		{"Error", slog.LevelError, false},
		{"loud", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseLogLevel(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// resolveWith parses args and applies env, then resolves the logging options,
// mirroring how PersistentPreRunE resolves them at runtime.
func resolveWith(t *testing.T, args []string, env map[string]string) (logOptions, error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	root, cfg := newRootCmd()
	// Pin stderr to a non-terminal writer so the color decision is deterministic
	// regardless of whether the test binary's stderr is attached to a terminal.
	root.SetErr(io.Discard)
	require.NoError(t, root.ParseFlags(args))
	return resolveLogOptions(root, cfg)
}

func TestResolveLogOptions(t *testing.T) {
	// Isolate from an ambient IQ_LOG* that a developer may have exported.
	for _, k := range []string{envLog, envLogFile, envLogLevel, envLogFormat} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantErr    bool
		wantEnable bool
		wantFile   string
		wantActive bool
		wantLevel  slog.Level
		wantFormat string
		wantColor  bool
	}{
		{
			name:       "defaults: disabled, default path, debug/text",
			wantEnable: false, wantFile: defaultLogFile(), wantActive: false,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "--log enables, default path active",
			args:       []string{"--log"},
			wantEnable: true, wantFile: defaultLogFile(), wantActive: true,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "explicit empty --log.file disables even with --log",
			args:       []string{"--log", "--log.file="},
			wantEnable: true, wantFile: "", wantActive: false,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "explicit --log.file path",
			args:       []string{"--log", "--log.file=/tmp/x.log", "--log.level=INFO", "--log.format=json"},
			wantEnable: true, wantFile: "/tmp/x.log", wantActive: true,
			wantLevel: slog.LevelInfo, wantFormat: "json",
		},
		{
			name:       "IQ_LOG env activates",
			env:        map[string]string{envLog: "true", envLogFile: "/tmp/y.log"},
			wantEnable: true, wantFile: "/tmp/y.log", wantActive: true,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "IQ_LOG_FILE set empty disables",
			args:       []string{"--log"},
			env:        map[string]string{envLogFile: ""},
			wantEnable: true, wantFile: "", wantActive: false,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			// An explicit --log.level flag wins over the env level and, per
			// imply-enable, turns logging on without --log.
			name:       "flag beats env for level, implies enable",
			args:       []string{"--log.level=WARN"},
			env:        map[string]string{envLogLevel: "INFO"},
			wantEnable: true, wantFile: defaultLogFile(), wantActive: true,
			wantLevel: slog.LevelWarn, wantFormat: "text",
		},
		{
			name:       "explicit --log.file implies enable",
			args:       []string{"--log.file=/tmp/x.log"},
			wantEnable: true, wantFile: "/tmp/x.log", wantActive: true,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "explicit --log.format implies enable",
			args:       []string{"--log.format=json"},
			wantEnable: true, wantFile: defaultLogFile(), wantActive: true,
			wantLevel: slog.LevelDebug, wantFormat: "json",
		},
		{
			// A lingering exported IQ_LOG_FILE must not silently start logging;
			// env vars alone still require IQ_LOG.
			name:       "IQ_LOG_FILE alone does not enable",
			env:        map[string]string{envLogFile: "/tmp/y.log"},
			wantEnable: false, wantFile: "/tmp/y.log", wantActive: false,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			// An explicit --log is authoritative and is not overridden by an
			// implying --log.* flag.
			name:       "explicit --log=false beats implying flag",
			args:       []string{"--log=false", "--log.file=/tmp/x.log"},
			wantEnable: false, wantFile: "/tmp/x.log", wantActive: false,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "stderr stream target, implied enable",
			args:       []string{"--log.file=stderr"},
			wantEnable: true, wantFile: "stderr", wantActive: true,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "stdout stream target",
			args:       []string{"--log", "--log.file=stdout"},
			wantEnable: true, wantFile: "stdout", wantActive: true,
			wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:       "env supplies level and format when flag unset",
			env:        map[string]string{envLogLevel: "error", envLogFormat: "json"},
			wantEnable: false, wantFile: defaultLogFile(), wantActive: false,
			wantLevel: slog.LevelError, wantFormat: "json",
		},
		{
			name:      "--color forces the verbose sink tinted",
			args:      []string{"--color"},
			wantColor: true,
			wantFile:  defaultLogFile(), wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:      "--monochrome keeps the verbose sink plain",
			args:      []string{"--monochrome"},
			wantColor: false,
			wantFile:  defaultLogFile(), wantLevel: slog.LevelDebug, wantFormat: "text",
		},
		{
			name:    "invalid IQ_LOG boolean errors",
			env:     map[string]string{envLog: "maybe"},
			wantErr: true,
		},
		{
			name:    "invalid --log.level errors",
			args:    []string{"--log.level=loud"},
			wantErr: true,
		},
		{
			name:    "invalid --log.format errors",
			args:    []string{"--log.format=xml"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := resolveWith(t, tt.args, tt.env)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantEnable, o.enable)
			require.Equal(t, tt.wantFile, o.file)
			require.Equal(t, tt.wantActive, o.fileActive())
			require.Equal(t, tt.wantLevel, o.level)
			require.Equal(t, tt.wantFormat, o.format)
			require.Equal(t, tt.wantColor, o.color)
		})
	}
}

// TestBuildVerboseOnly checks the verbose sink: INFO reaches stderr, DEBUG does
// not, no file is opened (nil closer), the timestamp is stripped, and the color
// flag drives whether the line carries ANSI escapes.
func TestBuildVerboseOnly(t *testing.T) {
	const esc = "\x1b[" // ANSI CSI introducer emitted by tint when tinting.
	tests := []struct {
		name      string
		color     bool
		wantEscHi bool
	}{
		{name: "color off stays plain", color: false, wantEscHi: false},
		{name: "color on tints the line", color: true, wantEscHi: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			logger, closer, err := logOptions{verbose: true, color: tt.color}.build(&stderr, io.Discard)
			require.NoError(t, err)
			require.Nil(t, closer)

			logger.Debug("d")
			require.Empty(t, stderr.String())

			logger.Info("i")
			require.Contains(t, stderr.String(), "i")
			require.NotContains(t, stderr.String(), "time=")
			if tt.wantEscHi {
				require.Contains(t, stderr.String(), esc)
			} else {
				require.NotContains(t, stderr.String(), esc)
			}
		})
	}
}

// nonEmptyLines counts the non-blank lines in s, one per rendered log record.
func nonEmptyLines(s string) int {
	n := 0
	for line := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// TestBuildSameStreamSuppression pins FIX 2 by observable output: with --verbose
// and the structured sink both targeting the literal "stderr" stream, one Info
// record must render exactly ONCE on stderr (the structured JSON wins; the tinted
// duplicate is suppressed). "stdout" is a different stream, so both sinks stay and
// the record renders on each. Verbose alone tints stderr; the structured sink alone
// renders structured only. It asserts renderings, never handler internals, so it
// kills the o.file == "stderr" comparison mutant and the suppression-branch mutant.
func TestBuildSameStreamSuppression(t *testing.T) {
	tests := []struct {
		name         string
		verbose      bool
		file         string // "" means no structured sink
		disabled     bool   // file is set but the structured sink is off (--log=false)
		wantStderr   int
		wantStdout   int
		stderrIsJSON bool // the single stderr line is the structured JSON, not tinted text
	}{
		{
			name:    "verbose+stderr suppresses the tinted duplicate, structured wins",
			verbose: true, file: "stderr", wantStderr: 1, wantStdout: 0, stderrIsJSON: true,
		},
		{
			// Suppression requires the structured sink to be ACTIVE, not merely a
			// "stderr" spelling: with --log=false the tinted sink must survive, or
			// -v --log=false --log.file=stderr would silently log nothing.
			name:    "verbose with disabled stderr sink keeps the tinted rendering",
			verbose: true, file: "stderr", disabled: true, wantStderr: 1, wantStdout: 0,
		},
		{
			name:    "verbose+stdout keeps both: different streams, one rendering each",
			verbose: true, file: "stdout", wantStderr: 1, wantStdout: 1,
		},
		{
			name:    "verbose alone tints stderr, no structured sink",
			verbose: true, file: "", wantStderr: 1, wantStdout: 0,
		},
		{
			name:    "structured stderr alone (no verbose) renders once",
			verbose: false, file: "stderr", wantStderr: 1, wantStdout: 0, stderrIsJSON: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := logOptions{verbose: tt.verbose}
			if tt.file != "" {
				o.enable = !tt.disabled
				o.file = tt.file
				o.level = slog.LevelDebug
				o.format = "json"
			}
			var stderr, stdout bytes.Buffer
			logger, closer, err := o.build(&stderr, &stdout)
			require.NoError(t, err)
			require.Nil(t, closer, "a stream/verbose target opens no file")

			logger.Info("m")

			require.Equal(t, tt.wantStderr, nonEmptyLines(stderr.String()),
				"stderr renderings; a lost suppression would double this")
			require.Equal(t, tt.wantStdout, nonEmptyLines(stdout.String()), "stdout renderings")

			if tt.stderrIsJSON {
				// The surviving stderr rendering is the structured JSON, not the tinted
				// text line: it parses as JSON with the record's msg.
				var rec map[string]any
				require.NoError(t, json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &rec),
					"the single stderr line must be the structured JSON")
				require.Equal(t, "m", rec["msg"])
			}
			if tt.verbose && tt.file == "stdout" {
				// The comparison must match "stderr" only: the verbose (tinted) sink
				// stays on stderr as text (no JSON braces) and the structured JSON goes
				// to stdout. A mutated comparison would suppress the tint here.
				require.NotContains(t, stderr.String(), "{", "stderr carries the tinted text, not JSON")
				require.Contains(t, stdout.String(), `"msg":"m"`, "the structured JSON goes to stdout")
			}
		})
	}
}

// TestBuildFileSink checks the file sink honors level and format, writes 0600,
// and returns a working closer.
func TestBuildFileSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "iq.log")
	o := logOptions{enable: true, file: path, level: slog.LevelDebug, format: "json"}

	logger, closer, err := o.build(io.Discard, io.Discard)
	require.NoError(t, err)
	require.NotNil(t, closer)
	logger.Debug("hello")
	require.NoError(t, closer())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"msg":"hello"`)

	if runtime.GOOS != "windows" { // Windows keeps no Unix mode bits to assert on.
		fi, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
}

// TestBuildStreamTarget checks that a literal stderr/stdout file target writes to
// the given stream, opens no file, and returns a nil closer (the stream must never
// be closed).
func TestBuildStreamTarget(t *testing.T) {
	tests := []struct {
		name string
		file string
		hit  string // which of stderr/stdout receives the record
	}{
		{name: "stderr stream", file: "stderr", hit: "stderr"},
		{name: "stdout stream", file: "stdout", hit: "stdout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr, stdout bytes.Buffer
			o := logOptions{enable: true, file: tt.file, level: slog.LevelDebug, format: "json"}
			logger, closer, err := o.build(&stderr, &stdout)
			require.NoError(t, err)
			require.Nil(t, closer, "a stream target must not be closed")

			logger.Info("streamed")
			if tt.hit == "stderr" {
				require.Contains(t, stderr.String(), `"msg":"streamed"`)
				require.Empty(t, stdout.String())
			} else {
				require.Contains(t, stdout.String(), `"msg":"streamed"`)
				require.Empty(t, stderr.String())
			}
		})
	}
}

// TestTraceLogWriter checks the trace-bridge splits its input into one DEBUG
// "backend cmd" record per complete line, splitting the driver prefix from the
// command, buffering a partial line until its newline arrives, and dropping blank
// lines.
func TestTraceLogWriter(t *testing.T) {
	var buf bytes.Buffer
	var seen []string
	lg := slog.New(cappedHandler{
		Handler: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		recs:    &seen,
		max:     100,
	})
	w := &traceLogWriter{lg: lg}

	// Write reports the full byte count it consumed, even for a partial line.
	whole := "mongo> find {\"a\":1}\n"
	n, err := io.WriteString(w, whole)
	require.NoError(t, err)
	require.Equal(t, len(whole), n, "Write must report every byte consumed")

	// A line delivered in two writes logs only once its newline arrives.
	partial := "redis> GET "
	n, err = io.WriteString(w, partial)
	require.NoError(t, err)
	require.Equal(t, len(partial), n)
	require.Equal(t, 1, strings.Count(buf.String(), "\n"), "partial line must not log yet")

	rest := "foo\n\nbare line\n"
	n, err = io.WriteString(w, rest)
	require.NoError(t, err)
	require.Equal(t, len(rest), n)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3, "one record per complete non-blank line")

	var recs []map[string]any
	for _, l := range lines {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &m))
		require.Equal(t, "backend cmd", m["msg"])
		recs = append(recs, m)
	}
	require.Equal(t, "mongo", recs[0]["driver"])
	require.Equal(t, `find {"a":1}`, recs[0]["cmd"])
	require.Equal(t, "redis", recs[1]["driver"])
	require.Equal(t, "GET foo", recs[1]["cmd"], "the split line is reassembled across writes")
	require.Equal(t, "bare line", recs[2]["cmd"])
	require.NotContains(t, recs[2], "driver")
}

// lockProbeHandler records whether traceLogWriter's mutex was free at the instant a
// record was handled. traceLogWriter emits its records from inside the drain loop,
// which runs while the write lock is held, so on correct code the probe never finds
// the mutex free. A mutant that releases the lock early (before the drain) leaves it
// free during Handle, which the probe observes deterministically on a single
// goroutine — no -race, no timing, no flakiness.
type lockProbeHandler struct {
	slog.Handler
	w        *traceLogWriter
	freeSeen *bool
}

func (h lockProbeHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.w.mu.TryLock() { // succeeds only if the write lock is NOT held.
		*h.freeSeen = true
		h.w.mu.Unlock()
	}
	return h.Handler.Handle(ctx, r)
}

// TestTraceLogWriterHoldsLockWhileEmitting pins that the writer emits its records
// under the buffer lock, so a concurrent write can never observe (or corrupt) a
// half-drained buffer. It kills the mutex defer-remove mutant deterministically: a
// single Write drives one record through the probe, which sees the lock held on
// correct code and free on the mutant.
func TestTraceLogWriterHoldsLockWhileEmitting(t *testing.T) {
	var freeSeen bool
	w := &traceLogWriter{}
	w.lg = slog.New(lockProbeHandler{
		Handler:  slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}),
		w:        w,
		freeSeen: &freeSeen,
	})

	_, err := io.WriteString(w, "redis> PING\n")
	require.NoError(t, err)
	require.False(t, freeSeen, "the buffer lock must be held while a record is emitted")
}

// cappedHandler collects the records it receives and panics once their count passes
// max. A Write loop that fails to consume its buffer logs one record per pass, so
// the cap turns an endless loop into a fast failure instead of a full disk.
type cappedHandler struct {
	slog.Handler // optional, receives every record the cap lets through.
	recs         *[]string
	max          int
}

func (cappedHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h cappedHandler) Handle(_ context.Context, r slog.Record) error {
	var driver, cmd string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "driver":
			driver = a.Value.String()
		case "cmd":
			cmd = a.Value.String()
		}
		return true
	})
	*h.recs = append(*h.recs, driver+"|"+cmd)
	if len(*h.recs) > h.max {
		panic("traceLogWriter.Write logged more records than its input holds")
	}
	if h.Handler != nil {
		return h.Handler.Handle(context.Background(), r)
	}
	return nil
}

func (h cappedHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h cappedHandler) WithGroup(string) slog.Handler      { return h }

// TestTraceLogWriterDrainsBuffer checks that one Write consumes every complete
// line exactly once, in order, and keeps the trailing partial line. The handler
// caps the record count, so a loop that does not advance past a line fails in
// milliseconds instead of running without end.
func TestTraceLogWriterDrainsBuffer(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
		rest  string
	}{
		{
			name:  "three lines",
			input: "redis> GET a\nmongo> find {}\nplain\n",
			want:  []string{"redis|GET a", "mongo|find {}", "|plain"},
		},
		{
			name:  "crlf line ends",
			input: "a> one\r\ntwo\n",
			want:  []string{"a|one", "|two"},
		},
		{
			name:  "partial tail kept",
			input: "x> done\nx> half",
			want:  []string{"x|done"},
			rest:  "x> half",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var recs []string
			w := &traceLogWriter{lg: slog.New(cappedHandler{recs: &recs, max: 20})}

			require.NotPanics(t, func() {
				n, err := io.WriteString(w, tt.input)
				require.NoError(t, err)
				require.Equal(t, len(tt.input), n)
			})

			require.Equal(t, tt.want, recs)
			require.Equal(t, tt.rest, string(w.buf))
		})
	}
}

// TestTraceSinkGating checks traceSink returns the input unchanged when no DEBUG
// sink is listening, and, when one is, a working tee that carries the caller's
// context to the Enabled gate. It writes through each returned writer so a broken
// bridge (nil logger, a nil MultiWriter member, a nil return) is caught.
func TestTraceSinkGating(t *testing.T) {
	// gateHandler enables only for a non-nil context, so a substituted nil ctx in
	// traceSink's Enabled check would fall to the "off" path.
	debug := slog.New(&gateHandler{inner: slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})})
	off := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))

	t.Run("off: existing writer returned unchanged", func(t *testing.T) {
		existing := &bytes.Buffer{}
		require.Equal(t, io.Writer(existing), traceSink(existing, off))
	})

	t.Run("off with no existing trace stays nil", func(t *testing.T) {
		require.Nil(t, traceSink(nil, off))
	})

	t.Run("on, no existing: bridge alone logs one record per line", func(t *testing.T) {
		var recs bytes.Buffer
		lg := slog.New(&gateHandler{inner: slog.NewJSONHandler(&recs, &slog.HandlerOptions{Level: slog.LevelDebug})})
		w := traceSink(nil, lg)
		require.NotNil(t, w, "on: a bridge even with no existing trace")
		n, err := io.WriteString(w, "redis> PING\n")
		require.NoError(t, err)
		require.Equal(t, len("redis> PING\n"), n)
		require.Contains(t, recs.String(), `"cmd":"PING"`)
	})

	t.Run("on, existing: writes reach both the existing sink and the log", func(t *testing.T) {
		var existing, recs bytes.Buffer
		lg := slog.New(&gateHandler{inner: slog.NewJSONHandler(&recs, &slog.HandlerOptions{Level: slog.LevelDebug})})
		w := traceSink(&existing, lg)
		require.NotEqual(t, io.Writer(&existing), w, "on: teed, not the bare writer")
		n, err := io.WriteString(w, "mongo> find\n")
		require.NoError(t, err)
		require.Equal(t, len("mongo> find\n"), n, "the tee must report the full byte count")
		require.Contains(t, existing.String(), "mongo> find", "the existing trace still receives the raw line")
		require.Contains(t, recs.String(), `"cmd":"find"`, "the log bridge also receives it")
	})

	t.Run("gate carries the caller context (nil ctx falls to off)", func(t *testing.T) {
		// With the gate handler, DEBUG is enabled only when a non-nil ctx reaches
		// Enabled; traceSink must pass a real context, so it returns a bridge.
		require.NotNil(t, traceSink(nil, debug), "a real context must enable the bridge")
	})
}

// TestBuildNoSinkDiscards checks that with nothing enabled the logger discards
// (never nil, no closer) so log points are cheap no-ops.
func TestBuildNoSinkDiscards(t *testing.T) {
	logger, closer, err := logOptions{}.build(io.Discard, io.Discard)
	require.NoError(t, err)
	require.Nil(t, closer)
	require.NotNil(t, logger)
	logger.Info("x") // must not panic
}

// TestBuildFileOpenError surfaces a file-open failure as an error, so PreRun
// fails fast rather than silently dropping logs.
func TestBuildFileOpenError(t *testing.T) {
	notdir := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(notdir, []byte("x"), 0o600))
	o := logOptions{enable: true, file: filepath.Join(notdir, "iq.log"), level: slog.LevelDebug, format: "text"}

	_, _, err := o.build(io.Discard, io.Discard)
	require.Error(t, err)
}

// ctxProbe is a slog handler that records whether the contexts it received carry
// the test marker, so a test can prove a wrapper forwards the caller's context.
// enabled is the answer that Enabled gives.
type ctxProbe struct {
	enabled                      bool
	enabledMarked, handledMarked bool
}

func (p *ctxProbe) Enabled(ctx context.Context, _ slog.Level) bool {
	p.enabledMarked = ctx != nil && ctx.Value(ctxKey{}) != nil
	return p.enabled
}

func (p *ctxProbe) Handle(ctx context.Context, _ slog.Record) error {
	p.handledMarked = ctx != nil && ctx.Value(ctxKey{}) != nil
	return nil
}

func (p *ctxProbe) WithAttrs([]slog.Attr) slog.Handler { return p }
func (p *ctxProbe) WithGroup(string) slog.Handler      { return p }

// TestFanoutForwardsTheContext checks Enabled and Handle pass the caller's
// context on to each child, not only to the first one.
func TestFanoutForwardsTheContext(t *testing.T) {
	t.Run("Enabled asks a later child with the context", func(t *testing.T) {
		first, second := &ctxProbe{enabled: false}, &ctxProbe{enabled: true}
		h := newFanout(first, second)

		require.True(t, h.Enabled(markedCtx(), slog.LevelInfo))
		require.True(t, first.enabledMarked, "the first child gets the context")
		require.True(t, second.enabledMarked, "the second child gets the context")
	})

	t.Run("Handle passes the context to every child", func(t *testing.T) {
		first, second := &ctxProbe{enabled: true}, &ctxProbe{enabled: true}
		h := newFanout(first, second)

		rec := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
		require.NoError(t, h.Handle(markedCtx(), rec))
		for i, p := range []*ctxProbe{first, second} {
			require.True(t, p.enabledMarked, "child %d checks its level with the context", i)
			require.True(t, p.handledMarked, "child %d handles the record with the context", i)
		}
	})
}

// TestDropTimeAttr checks only the top-level time attribute is removed.
func TestDropTimeAttr(t *testing.T) {
	timeAttr := slog.Time(slog.TimeKey, time.Unix(0, 0))
	tests := []struct {
		name   string
		groups []string
		attr   slog.Attr
		want   slog.Attr
	}{
		{name: "top-level time is dropped", attr: timeAttr, want: slog.Attr{}},
		{name: "grouped time is kept", groups: []string{"g"}, attr: timeAttr, want: timeAttr},
		{name: "other key is kept", attr: slog.String("msg", "x"), want: slog.String("msg", "x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dropTimeAttr(tt.groups, tt.attr)
			require.True(t, got.Equal(tt.want), "got %v, want %v", got, tt.want)
		})
	}
}

// TestVerboseSinkOmitsTheTimestamp checks the stderr sink prints no wall clock.
func TestVerboseSinkOmitsTheTimestamp(t *testing.T) {
	var stderr bytes.Buffer
	o := logOptions{verbose: true}
	logger, _, err := o.build(&stderr, io.Discard)
	require.NoError(t, err)
	logger.Info("hello")

	line := strings.TrimSpace(stderr.String())
	require.True(t, strings.HasPrefix(line, "INF hello"), "line %q starts with the level", line)
}

// TestBuildReportsAnUnusableLogFile checks a log path whose parent is a file
// fails with an error and still returns a usable logger and no closer.
func TestBuildReportsAnUnusableLogFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))

	o := logOptions{enable: true, file: filepath.Join(blocker, "iq.log"), level: slog.LevelInfo, format: "text"}
	logger, closer, err := o.build(io.Discard, io.Discard)
	require.ErrorContains(t, err, "create log dir")
	require.NotNil(t, logger)
	require.Nil(t, closer)
	require.NotPanics(t, func() { logger.Info("still safe") })
}

// TestOpenLogFileErrors checks both failure wraps keep their cause, and that a
// new directory is private.
func TestOpenLogFileErrors(t *testing.T) {
	t.Run("parent is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))
		_, err := openLogFile(filepath.Join(blocker, "iq.log"))
		require.ErrorContains(t, err, "create log dir")
		var pe *fs.PathError
		require.ErrorAs(t, err, &pe)
	})

	t.Run("target is a directory", func(t *testing.T) {
		_, err := openLogFile(t.TempDir())
		require.ErrorContains(t, err, "open log file")
		var pe *fs.PathError
		require.ErrorAs(t, err, &pe)
	})

	t.Run("a new directory is private", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("windows has no unix permission bits")
		}
		dir := filepath.Join(t.TempDir(), "logs")
		f, err := openLogFile(filepath.Join(dir, "iq.log"))
		require.NoError(t, err)
		require.NoError(t, f.Close())
		info, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})
}

// TestParseLogLevelErrorValue checks a rejected level comes back as the zero
// level beside the error.
func TestParseLogLevelErrorValue(t *testing.T) {
	level, err := parseLogLevel("loud")
	require.ErrorContains(t, err, `invalid --log.level "loud"`)
	require.Zero(t, level)
}
