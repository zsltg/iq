package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// pingTarget is one source to check: its full handle and stored source.
type pingTarget struct {
	handle string
	source iqconfig.Source
}

// newPingCmd builds `iq ping [name...]`: check that sources are reachable. With
// no arguments it pings the active source; otherwise each argument names a source
// or a group (which pings every member), and --all pings every saved source. Each
// check is bounded by --timeout.
func newPingCmd(cfg *config) *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:               "ping [name...]",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Check that sources are reachable",
		Long: "Open each source and round-trip a cheap backend-specific command (Redis PING,\n" +
			"Cassandra a version read, {ping:1} and its kin elsewhere; a dump file and\n" +
			"DynamoDB verify at open), reporting its driver and the round-trip time, or the\n" +
			"error. With no arguments the\n" +
			"active source is pinged; otherwise each argument is a source handle or a group\n" +
			"(pinging every member), and --all pings every saved source. Each check is bounded\n" +
			"by --timeout. Exits non-zero if any source is unreachable.",
		Example: "  $ iq ping            # the active source\n" +
			"  $ iq ping cache shop # specific sources\n" +
			"  $ iq ping prod       # a whole group\n" +
			"  $ iq ping --all      # every saved source",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			targets, err := pingTargets(cf, args, all)
			if err != nil {
				return err
			}
			rows := make([][]tableCell, 0, len(targets))
			anyFail := false
			for _, t := range targets {
				driver := driverName(t.source.URL)
				if d, perr := pingOne(cmd.Context(), t, cfg.timeout); perr != nil {
					anyFail = true
					rows = append(rows, []tableCell{cell(t.handle), cell(driver), coloredCell("error", pal.fail), cell(oneLine(perr))})
				} else {
					rows = append(rows, []tableCell{cell(t.handle), cell(driver), coloredCell("ok", pal.ok), cell(d.Round(time.Millisecond).String())})
				}
			}
			if err := renderTable(cmd.OutOrStdout(), rows); err != nil {
				return err
			}
			if anyFail {
				return errors.New("one or more sources unreachable")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "ping every saved source")
	return c
}

// pingTargets resolves the ping arguments to sources: every saved source when all
// is set, the active source when no arguments are given, otherwise each argument
// as a source or a group (expanded to its members). It errors if --all is
// combined with arguments, --all finds no saved sources, there is no active
// source, or an argument is unknown.
func pingTargets(cf *iqconfig.Config, args []string, all bool) ([]pingTarget, error) {
	if all {
		if len(args) > 0 {
			return nil, errors.New("--all pings every source; drop the source arguments")
		}
		handles := cf.List()
		if len(handles) == 0 {
			return nil, errors.New("no sources; add one with `iq add <uri>`")
		}
		targets := make([]pingTarget, 0, len(handles))
		for _, h := range handles {
			targets = append(targets, pingTarget{handle: h.Name, source: h.Source})
		}
		return targets, nil
	}
	if len(args) == 0 {
		if cf.Active == "" {
			return nil, errors.New("no active source; name one or run `iq src <name>`")
		}
		s, full, ok := cf.Resolve(cf.Active)
		if !ok {
			return nil, fmt.Errorf("active source %q not found; run `iq ls`", cf.Active)
		}
		return []pingTarget{{handle: full, source: s}}, nil
	}
	var targets []pingTarget
	var unknown []string
	seen := make(map[string]bool)
	add := func(handle string, s iqconfig.Source) {
		if !seen[handle] {
			targets = append(targets, pingTarget{handle: handle, source: s})
			seen[handle] = true
		}
	}
	for _, arg := range args {
		if s, full, ok := cf.Resolve(arg); ok {
			add(full, s)
			continue
		}
		prefix := iqconfig.CleanHandle(arg) + "/"
		found := false
		for _, h := range cf.List() {
			if strings.HasPrefix(h.Name, prefix) {
				add(h.Name, h.Source)
				found = true
			}
		}
		if !found {
			unknown = append(unknown, iqconfig.CleanHandle(arg))
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown source or group: %s", strings.Join(unknown, ", "))
	}
	return targets, nil
}

// pingOne opens the target and round-trips one cheap command, returning the
// elapsed time. Any error has the connection URL redacted so a stored password
// never surfaces in a diagnostic message.
func pingOne(ctx context.Context, t pingTarget, timeout time.Duration) (time.Duration, error) {
	u, err := effectiveURL(t.source, t.handle)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	if err := verifySource(ctx, u, timeout); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// verifySource opens rawURL and round-trips one cheap command, bounded by
// timeout, returning nil when the backend is reachable. It backs both `iq ping`
// and the post-add reachability check in `iq add`. The check is
// connection-level, so it needs no collection (the URL carries any default). Any
// error has the connection URL redacted so a stored password never surfaces in a
// diagnostic message.
func verifySource(ctx context.Context, rawURL string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st, err := openStore(cctx, &config{url: rawURL})
	if err != nil {
		return redactErr(err, rawURL)
	}
	defer func() { _ = st.Close() }()
	// A read-only local source (a dump file) has no server to round-trip a command
	// against, and a verifiesOnOpen backend (DynamoDB) already round-tripped a
	// reachability probe at open — for both, opening it is the reachability check.
	if d, ok := driverForScheme(schemeOf(rawURL)); ok && (d.readOnly || d.verifiesOnOpen) {
		return nil
	}
	if _, err := st.Query(cctx, healthArgs(rawURL)); err != nil {
		return redactErr(err, rawURL)
	}
	return nil
}

// healthArgs returns the cheapest round-trip command for the URL's backend.
func healthArgs(rawURL string) []string {
	switch driverName(rawURL) {
	case "redis":
		return []string{"PING"}
	case "cassandra":
		return []string{"SELECT release_version FROM system.local"}
	default:
		return []string{`{"ping":1}`}
	}
}

// redactErr returns err with the connection password removed from its message,
// so a driver error cannot leak it. It first replaces each occurrence of the raw
// connection URI with its redacted form, then applies newRedactor. rawURL can be
// empty. It returns err itself when the message does not change, so errors.Is
// and errors.As still see the chain.
func redactErr(err error, rawURL string) error {
	msg := err.Error()
	out := msg
	if rawURL != "" {
		out = strings.ReplaceAll(out, rawURL, redactURL(rawURL))
	}
	out = newRedactor(err)(out)
	if out == msg {
		return err
	}
	return errors.New(out)
}

// oneLine collapses an error message to a single line for tabular output.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
