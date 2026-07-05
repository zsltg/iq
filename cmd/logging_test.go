package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
			name:       "flag beats env for level",
			args:       []string{"--log.level=WARN"},
			env:        map[string]string{envLogLevel: "INFO"},
			wantEnable: false, wantFile: defaultLogFile(), wantActive: false,
			wantLevel: slog.LevelWarn, wantFormat: "text",
		},
		{
			name:       "env supplies level and format when flag unset",
			env:        map[string]string{envLogLevel: "error", envLogFormat: "json"},
			wantEnable: false, wantFile: defaultLogFile(), wantActive: false,
			wantLevel: slog.LevelError, wantFormat: "json",
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
		})
	}
}

// TestBuildVerboseOnly checks the verbose sink: INFO reaches stderr, DEBUG does
// not, no file is opened (nil closer), and the timestamp is stripped.
func TestBuildVerboseOnly(t *testing.T) {
	var stderr bytes.Buffer
	logger, closer, err := logOptions{verbose: true}.build(&stderr)
	require.NoError(t, err)
	require.Nil(t, closer)

	logger.Debug("d")
	require.Empty(t, stderr.String())

	logger.Info("i")
	require.Contains(t, stderr.String(), "i")
	require.NotContains(t, stderr.String(), "time=")
}

// TestBuildFileSink checks the file sink honors level and format, writes 0600,
// and returns a working closer.
func TestBuildFileSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "iq.log")
	o := logOptions{enable: true, file: path, level: slog.LevelDebug, format: "json"}

	logger, closer, err := o.build(io.Discard)
	require.NoError(t, err)
	require.NotNil(t, closer)
	logger.Debug("hello")
	require.NoError(t, closer())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"msg":"hello"`)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

// TestBuildNoSinkDiscards checks that with nothing enabled the logger discards
// (never nil, no closer) so log points are cheap no-ops.
func TestBuildNoSinkDiscards(t *testing.T) {
	logger, closer, err := logOptions{}.build(io.Discard)
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

	_, _, err := o.build(io.Discard)
	require.Error(t, err)
}
