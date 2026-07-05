package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Environment overrides for the logging flags, read at bootstrap when the
// matching flag was not set on the command line. They mirror sq's SQ_LOG family
// so the muscle memory carries over; precedence is explicit flag > env > default.
const (
	envLog       = "IQ_LOG"
	envLogFile   = "IQ_LOG_FILE"
	envLogLevel  = "IQ_LOG_LEVEL"
	envLogFormat = "IQ_LOG_FORMAT"
)

// logOptions is the resolved, validated logging configuration for one
// invocation: the verbose stderr sink and the file sink are independent, each
// with its own level, so a DEBUG record can reach the file while the INFO stderr
// sink skips it. file is empty when file logging is off.
type logOptions struct {
	verbose bool
	enable  bool
	file    string
	level   slog.Level
	format  string
}

// resolveLogOptions computes the logging configuration from the flags and the
// IQ_LOG* environment, validating the level and format so a bad value fails fast
// before any file is opened. It never opens a file itself.
func resolveLogOptions(cmd *cobra.Command, cfg *config) (logOptions, error) {
	o := logOptions{verbose: cfg.verbose}

	o.enable = cfg.logEnable
	if !cmd.Flags().Changed("log") {
		if v, ok := os.LookupEnv(envLog); ok {
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return o, fmt.Errorf("invalid %s %q: want a boolean", envLog, v)
			}
			o.enable = b
		}
	}

	// An explicit --log.file wins (empty explicitly disables); otherwise
	// IQ_LOG_FILE, then the default cache path (empty when it cannot be located).
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

// build assembles the invocation logger and, when file logging is on, opens the
// log file and returns its Close as the second result. The logger is never nil:
// with no sink it discards, so every log point is a cheap no-op. The verbose
// sink writes to stderr at INFO; the file sink honors the resolved level and
// format.
func (o logOptions) build(stderr io.Writer) (*slog.Logger, func() error, error) {
	var handlers []slog.Handler
	if o.verbose {
		handlers = append(handlers, slog.NewTextHandler(stderr, &slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: dropTimeAttr,
		}))
	}
	var closer func() error
	if o.fileActive() {
		f, err := openLogFile(o.file)
		if err != nil {
			return slog.New(slog.DiscardHandler), nil, err
		}
		closer = f.Close
		opts := &slog.HandlerOptions{Level: o.level}
		if o.format == "json" {
			handlers = append(handlers, slog.NewJSONHandler(f, opts))
		} else {
			handlers = append(handlers, slog.NewTextHandler(f, opts))
		}
	}
	if len(handlers) == 0 {
		return slog.New(slog.DiscardHandler), nil, nil
	}
	return slog.New(newFanout(handlers...)), closer, nil
}

// dropTimeAttr removes the top-level time attribute so the verbose stderr sink
// stays terse (the wall clock is noise on an interactive stream); the file sink
// keeps its timestamp.
func dropTimeAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
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

// openLogFile opens (creating and appending to) the log file, creating its
// parent directory. Both are 0600/0700 because a log line may carry a redacted
// but still sensitive location, matching the config file's secret-aware perms.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

// discardLogger is the shared no-op logger returned when an invocation has no
// logger set (a command built directly in a test, bypassing the root PreRun).
var discardLogger = slog.New(slog.DiscardHandler)

// log returns the invocation logger, or the shared discard logger when none was
// set, so every log point is nil-safe without a guard at the call site.
func (c *config) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return discardLogger
}

// fanoutHandler dispatches each record to several slog handlers, so one logger
// drives both the verbose stderr sink and the file sink from a single set of log
// points. Each child gates on its own Enabled, keeping the two thresholds
// independent.
type fanoutHandler struct {
	handlers []slog.Handler
}

// newFanout builds a fanoutHandler over the given child handlers.
func newFanout(handlers ...slog.Handler) *fanoutHandler {
	return &fanoutHandler{handlers: handlers}
}

// Enabled reports whether any child handles this level, so a record is dropped
// only when every sink would ignore it.
func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, c := range h.handlers {
		if c.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle forwards the record to each child that accepts its level, cloning per
// child because a Record shares backing storage its handler may append to.
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
