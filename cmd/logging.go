package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lmittmann/tint"
	"github.com/spf13/cobra"
)

// Environment overrides for the logging flags, read at bootstrap when the
// matching flag was not set on the command line. They mirror the SQ_LOG family of
// sq, so the muscle memory carries over. Precedence is explicit flag > env >
// default.
const (
	envLog       = "IQ_LOG"
	envLogFile   = "IQ_LOG_FILE"
	envLogLevel  = "IQ_LOG_LEVEL"
	envLogFormat = "IQ_LOG_FORMAT"
)

// logOptions is the resolved, validated logging configuration for one
// invocation. The verbose stderr sink and the file sink are independent, and
// each has its own level. So a DEBUG record can reach the file while the INFO
// stderr sink skips it. The file field is empty when file logging is off. The
// color field tints the verbose stderr sink (never the file sink) when stderr
// is a color-eligible terminal.
type logOptions struct {
	verbose bool
	color   bool
	enable  bool
	file    string
	level   slog.Level
	format  string
}

// resolveLogOptions computes the logging configuration from the flags and the
// IQ_LOG* environment. It validates the level and format, so a bad value fails
// fast before any file is opened. It never opens a file itself.
func resolveLogOptions(cmd *cobra.Command, cfg *config) (logOptions, error) {
	o := logOptions{verbose: cfg.verbose}

	// The verbose sink writes to stderr, so its color eligibility is decided
	// against the own TTY-ness of stderr. It is independent of the global mode
	// that resolveColor decides from stdout (the two writers can differ).
	// -M/-C/NO_COLOR still apply.
	o.color = wantColor(cfg.monochrome, cfg.forceColor, cmd.ErrOrStderr())

	o.enable = cfg.logEnable
	if !cmd.Flags().Changed("log") {
		if v, ok := os.LookupEnv(envLog); ok {
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return o, fmt.Errorf("invalid %s %q: want a boolean", envLog, v)
			}
			o.enable = b
		}
		// An explicitly set --log.file/--log.level/--log.format implies enable even
		// without --log, because configuring the logger is intent to use it. This
		// runs only when --log was not given (an explicit --log, true or false, is
		// authoritative). It also runs only for a command-line flag. A lingering
		// exported IQ_LOG_FILE must never silently start logging, so env vars still
		// require IQ_LOG. An explicit flag thus beats env here, which keeps flag >
		// env > default.
		if !o.enable {
			o.enable = cmd.Flags().Changed("log.file") ||
				cmd.Flags().Changed("log.level") ||
				cmd.Flags().Changed("log.format")
		}
	}

	// An explicit --log.file wins (empty explicitly disables). Otherwise the
	// order is IQ_LOG_FILE, then the default cache path (empty when it cannot be
	// located).
	switch {
	case cmd.Flags().Changed("log.file"):
		o.file = cfg.logFile
	default:
		if v, ok := os.LookupEnv(envLogFile); ok {
			o.file = v
		} else {
			o.file = defaultLogFile()
		}
	}

	levelStr := cfg.logLevel
	if !cmd.Flags().Changed("log.level") {
		if v, ok := os.LookupEnv(envLogLevel); ok {
			levelStr = v
		}
	}
	level, err := parseLogLevel(levelStr)
	if err != nil {
		return o, err
	}
	o.level = level

	format := cfg.logFormat
	if !cmd.Flags().Changed("log.format") {
		if v, ok := os.LookupEnv(envLogFormat); ok {
			format = v
		}
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "text" && format != "json" {
		return o, fmt.Errorf("invalid --log.format %q: want text or json", format)
	}
	o.format = format

	return o, nil
}

// fileActive reports whether file logging is on: enabled and pointed at a
// non-empty path. An empty path disables logging, matching sq.
func (o logOptions) fileActive() bool { return o.enable && o.file != "" }

// build assembles the invocation logger. If file logging is on, it opens the
// sink and returns its Close as the second result (nil for a stream target).
// The logger is never nil: with no sink it discards, so every log point is a
// cheap no-op. The verbose sink writes to stderr at INFO, tinted when o.color
// is set. The file sink honors the resolved level and format and is never
// tinted. A literal "stderr" or "stdout" file target resolves to the stderr and
// stdout writers.
//
// If the verbose sink and the structured sink both target the stderr stream,
// each record renders twice on it. The user configured the structured sink
// explicitly, so build suppresses the tinted duplicate. This matches only the
// literal "stderr" keyword. A "/dev/stderr" file path is a distinct target that
// build does not detect. "stdout" is a different stream, so it never suppresses
// the verbose sink.
func (o logOptions) build(stderr, stdout io.Writer) (*slog.Logger, func() error, error) {
	var handlers []slog.Handler
	structuredActive := o.fileActive()
	suppressVerbose := structuredActive && o.file == "stderr"
	if o.verbose && !suppressVerbose {
		// tint.Options.Level is nil, so tint uses its default level, INFO.
		handlers = append(handlers, tint.NewTextHandler(stderr, &tint.Options{
			NoColor: !o.color,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				return redactAttr(groups, dropTimeAttr(groups, a))
			},
		}))
	}
	var closer func() error
	if structuredActive {
		sink, c, err := o.logSink(stderr, stdout)
		if err != nil {
			return slog.New(slog.DiscardHandler), nil, err
		}
		closer = c
		opts := &slog.HandlerOptions{Level: o.level, ReplaceAttr: redactAttr}
		if o.format == "json" {
			handlers = append(handlers, slog.NewJSONHandler(sink, opts))
		} else {
			handlers = append(handlers, slog.NewTextHandler(sink, opts))
		}
	}
	if len(handlers) == 0 {
		return slog.New(slog.DiscardHandler), nil, nil
	}
	return slog.New(newFanout(handlers...)), closer, nil
}

// logSink resolves the file target to its writer and a closer. A literal "stderr"
// or "stdout" is a portable stream target (unlike the "/dev/stderr" path trap). It
// writes to the provided stream and creates no directory. It is never closed,
// because closing os.Stderr or os.Stdout breaks the process. Any other value is a
// filesystem path, opened (and its parent created) by openLogFile. The Close of
// that file is returned, so the caller releases it.
func (o logOptions) logSink(stderr, stdout io.Writer) (io.Writer, func() error, error) {
	switch o.file {
	case "stderr":
		return stderr, nil, nil
	case "stdout":
		return stdout, nil, nil
	default:
		f, err := openLogFile(o.file)
		if err != nil {
			return nil, nil, err
		}
		return f, f.Close, nil
	}
}

// dropTimeAttr removes the top-level time attribute, so the verbose stderr sink
// stays terse (the wall clock is noise on an interactive stream). The file sink
// keeps its timestamp.
func dropTimeAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

// redactAttr is the log hook that removes the connection password from a log
// record. It applies newRedactor to an attribute with an error value and to a
// string attribute with the key err or error. It also applies newRedactor to the
// top-level message, as defense in depth: log messages in cmd are fixed
// literals. Other string attributes do not change, because they carry records,
// filters and commands that a diagnostic needs. A changed error stays an error
// value, so the tint handler still shows it as an error.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindAny:
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		msg := err.Error()
		if out := newRedactor(err)(msg); out != msg {
			return slog.Any(a.Key, errors.New(out))
		}
	case slog.KindString:
		if redactsString(groups, a.Key) {
			return slog.String(a.Key, newRedactor(nil)(a.Value.String()))
		}
	}
	return a
}

// redactsString reports whether redactAttr redacts a string attribute. It
// redacts the err and error keys in any group, and the message only at the top
// level.
func redactsString(groups []string, key string) bool {
	switch key {
	case "err", "error":
		return true
	case slog.MessageKey:
		return len(groups) == 0
	}
	return false
}

// parseLogLevel maps a case-insensitive level name to an slog.Level, erroring on
// anything outside the accepted set so an invalid --log.level fails fast.
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN", "WARNING":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid --log.level %q: want DEBUG, INFO, WARN, or ERROR", s)
	}
}

// defaultLogFile is the log path used when logging is enabled without an
// explicit file: <user cache dir>/iq/iq.log. It returns "" when the cache dir
// cannot be located, so an otherwise-valid run is not failed by the fallback.
func defaultLogFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "iq", "iq.log")
}

// openLogFile opens (creating and appending to) the log file, and creates its
// parent directory. Both use 0600/0700, because a log line can carry a
// redacted but still sensitive location. This matches the secret-aware perms
// of the config file.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: opens the user's configured log file, by design.
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

// discardLogger is the shared no-op logger returned when an invocation has no
// logger set (a command built directly in a test, bypassing the root PreRun).
var discardLogger = slog.New(slog.DiscardHandler)

// log returns the invocation logger, or the shared discard logger when none was
// set. So every log point is nil-safe without a guard at the call site.
func (c *config) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return discardLogger
}

// traceSink returns the writer for the live command trace of a driver. When a
// DEBUG sink is listening, it tees the existing trace (the --verbose stderr
// sink, or nil) with a DEBUG "backend cmd" log bridge. So a plain --log run
// captures the wire trace without --verbose. Drivers redact credentials before
// they reach the trace, so the bridge logs exactly what --verbose shows. With
// no DEBUG sink it returns the input unchanged.
func traceSink(existing io.Writer, lg *slog.Logger) io.Writer {
	if !lg.Enabled(context.Background(), slog.LevelDebug) {
		return existing
	}
	bridge := &traceLogWriter{lg: lg}
	if existing == nil {
		return bridge
	}
	return io.MultiWriter(existing, bridge)
}

// traceLogWriter adapts the line-oriented command trace of the drivers to
// structured logging. It buffers partial writes and emits one DEBUG "backend cmd"
// record per complete line. A line is split on the "> " prefix of the driver into
// a driver attr and a cmd attr. A line without that shape logs the whole text as
// cmd. Writes are serialized, so a trace produced from several goroutines never
// interleaves the parsing of a record.
type traceLogWriter struct {
	lg  *slog.Logger
	mu  sync.Mutex
	buf []byte
}

// Write appends p, then drains every complete newline-terminated line into a log
// record. A trailing partial line is retained for the next Write. It never
// reports an error, so tracing can never break the query it describes.
func (w *traceLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if line == "" {
			continue
		}
		if driver, cmd, ok := strings.Cut(line, "> "); ok {
			w.lg.Debug("backend cmd", "driver", driver, "cmd", cmd)
		} else {
			w.lg.Debug("backend cmd", "cmd", line)
		}
	}
	return len(p), nil
}

// fanoutHandler dispatches each record to several slog handlers. So one
// logger drives both the verbose stderr sink and the file sink from a
// single set of log points. Each child gates on its own Enabled. This
// keeps the two thresholds independent.
type fanoutHandler struct {
	handlers []slog.Handler
}

// newFanout builds a fanoutHandler over the given child handlers.
func newFanout(handlers ...slog.Handler) *fanoutHandler {
	return &fanoutHandler{handlers: handlers}
}

// Enabled reports whether any child handles this level, so a record is dropped
// only when every sink ignores it.
func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, c := range h.handlers {
		if c.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle forwards the record to each child that accepts its level. It clones
// per child, because a Record shares backing storage that its handler can
// append to.
func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, c := range h.handlers {
		if !c.Enabled(ctx, r.Level) {
			continue
		}
		if err := c.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs returns a fanout whose children each carry the added attributes.
func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, c := range h.handlers {
		next[i] = c.WithAttrs(attrs)
	}
	return &fanoutHandler{handlers: next}
}

// WithGroup returns a fanout whose children each open the named group.
func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, c := range h.handlers {
		next[i] = c.WithGroup(name)
	}
	return &fanoutHandler{handlers: next}
}
