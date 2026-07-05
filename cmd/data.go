package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/query"
)

// dataFlags holds the two previews shared by every `iq data` subcommand as parent
// persistent flags, so copy/clear/drop expose them identically. They are mutually
// exclusive: --explain describes the plan without connecting, --dry-run connects
// and reports the real effect without mutating.
type dataFlags struct {
	explain bool
	dryRun  bool
}

// validate rejects the one illegal combination, mirroring the root's
// mutually-exclusive output flags.
func (f *dataFlags) validate() error {
	if f.explain && f.dryRun {
		return errors.New("--explain and --dry-run are mutually exclusive")
	}
	return nil
}

// newDataCmd builds the `iq data` command group: structured data movement (copy)
// and container lifecycle (clear, drop). It is the iq-native counterpart to sq's
// `tbl` group, named for the backend-neutral thing every store holds rather than a
// SQL table. The group owns the shared --explain/--dry-run previews.
func newDataCmd(cfg *config) *cobra.Command {
	df := &dataFlags{}
	c := &cobra.Command{
		Use:   "data",
		Short: "Move data between sources, and manage containers",
		Long: "Structured, driver-agnostic data movement and lifecycle. `copy` moves items\n" +
			"between sources (a `file://` source reads a dump; -o writes one); `clear` empties\n" +
			"a container; `drop` removes one.\n\n" +
			"--explain shows the plan without connecting; --dry-run reports the real effect\n" +
			"without changing anything.",
	}
	c.PersistentFlags().BoolVar(&df.explain, "explain", false, "describe the plan without connecting or changing anything")
	c.PersistentFlags().BoolVar(&df.dryRun, "dry-run", false, "report the real effect without changing anything")
	c.AddCommand(
		newDataCopyCmd(cfg, df),
		newDataClearCmd(cfg, df),
		newDataDropCmd(cfg, df),
	)
	return c
}

// endpoint is one side of a copy: either a registered source (a connectable
// keyspace) or a file path (a typed JSONL dump, "-" or "" meaning stdin/stdout).
type endpoint struct {
	isFile     bool
	path       string // for a file endpoint; "" or "-" is stdin (source) / stdout (dest)
	url        string
	collection string
	driver     string
	handle     string
}

// resolveEndpoint turns one positional (or an empty dst) into an endpoint. A
// positional is always a saved source handle — never a bare file path, so a handle
// can never be shadowed by, or confused with, a like-named file. A dump file is
// used by registering it as a `file://` source; file output goes through -o and
// stdio through "-". An empty argument is only valid for a destination, where it
// means stdout.
func resolveEndpoint(cf *iqconfig.Config, arg string, isDst bool) (endpoint, error) {
	if arg == "" {
		if !isDst {
			return endpoint{}, errors.New("a source is required")
		}
		return endpoint{isFile: true, path: ""}, nil // omitted destination is stdout.
	}
	if arg == "-" {
		return endpoint{isFile: true, path: "-"}, nil
	}
	name, coll, hasColl := splitSourceArg(cf, arg)
	src, full, ok := cf.Resolve(name)
	if !ok {
		return endpoint{}, fmt.Errorf("unknown source %q; register it with `iq add` "+
			"(a dump file too, via a file:// url), write a file with -o, or use - for stdin/stdout", arg)
	}
	u, err := effectiveURL(src, full)
	if err != nil {
		return endpoint{}, fmt.Errorf("source %q: %w", full, err)
	}
	collection := src.Collection
	if hasColl {
		if strings.HasPrefix(schemeOf(u), "redis") {
			return endpoint{}, fmt.Errorf("redis sources have no collections; drop the %q suffix", coll)
		}
		collection = coll
	}
	return endpoint{url: u, collection: collection, driver: driverName(u), handle: full}, nil
}

// label names an endpoint for a message: its handle (with collection) for a
// source, or a friendly file label for a file endpoint.
func (e endpoint) label() string {
	if !e.isFile {
		if e.collection != "" {
			return fmt.Sprintf("%s.%s", e.handle, e.collection)
		}
		return e.handle
	}
	switch e.path {
	case "", "-":
		return "stdout/stdin"
	default:
		return e.path
	}
}

// jsonlPutter is the destination for a file/stdout endpoint: it encodes each batch
// as typed JSONL. The write mode is irrelevant — a stream sink always writes — so a
// file destination rejects the write-mode flags at the command layer.
type jsonlPutter struct {
	w io.Writer
}

func (p jsonlPutter) Put(_ context.Context, batch []query.Record, _ query.WriteMode) (query.WriteStat, error) {
	if err := query.WriteJSONL(p.w, batch); err != nil {
		return query.WriteStat{}, err
	}
	return query.WriteStat{Written: len(batch)}, nil
}

// openFileInput opens a file endpoint for reading: stdin for "-"/"" else the named
// file. The returned closer is a no-op for stdin.
func openFileInput(cmd *cobra.Command, path string) (io.Reader, func() error, error) {
	if path == "" || path == "-" {
		return cmd.InOrStdin(), func() error { return nil }, nil
	}
	f, err := os.Open(path) //nolint:gosec // the path is a user-supplied input file, by design.
	if err != nil {
		return nil, nil, fmt.Errorf("open %q: %w", path, err)
	}
	return f, f.Close, nil
}

// openFileOutput opens a file endpoint for writing: stdout for "-"/"" else a
// truncated named file. The returned closer is a no-op for stdout.
func openFileOutput(cmd *cobra.Command, path string) (io.Writer, func() error, error) {
	if path == "" || path == "-" {
		return cmd.OutOrStdout(), func() error { return nil }, nil
	}
	f, err := os.Create(path) //nolint:gosec // the path is a user-supplied output file, by design.
	if err != nil {
		return nil, nil, fmt.Errorf("create %q: %w", path, err)
	}
	return f, f.Close, nil
}

// confirmDestruction asks the user to confirm a destructive operation, unless
// --force is set. In a non-interactive context (stdin is not a terminal) it
// refuses rather than block, requiring --force, so a script never hangs on a
// prompt or destroys data silently.
func confirmDestruction(cmd *cobra.Command, action string, force bool) error {
	if force {
		return nil
	}
	if !stdinIsTerminal() {
		return fmt.Errorf("%s: needs confirmation; re-run with --force", action)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s? [y/N] ", action)
	var resp string
	_, _ = fmt.Fscanln(cmd.InOrStdin(), &resp)
	if !strings.EqualFold(resp, "y") && !strings.EqualFold(resp, "yes") {
		return errors.New("aborted")
	}
	return nil
}

// stdinIsTerminal reports whether stdin is an interactive terminal, so a
// confirmation prompt only appears where a human can answer it. It avoids a
// terminal dependency by checking the character-device bit of stdin's mode.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
