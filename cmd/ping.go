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
// or a group (which pings every member). Each check is bounded by --timeout.
func newPingCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "ping [name...]",
		Short: "Check that sources are reachable",
		Long: "Open each source and round-trip a cheap command (Redis PING, MongoDB {ping:1}),\n" +
			"reporting its driver and the round-trip time, or the error. With no arguments the\n" +
			"active source is pinged; otherwise each argument is a source handle or a group\n" +
			"(pinging every member). Each check is bounded by --timeout. Exits non-zero if any\n" +
			"source is unreachable.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			targets, err := pingTargets(cf, args)
			if err != nil {
				return err
			}
			rows := make([][]tableCell, 0, len(targets))
			anyFail := false
			for _, t := range targets {
				driver := schemeOf(t.source.URL)
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
}

// pingTargets resolves the ping arguments to sources: the active source when
// none are given, otherwise each argument as a source or a group (expanded to its
// members). It errors if there is no active source or an argument is unknown.
func pingTargets(cf *iqconfig.Config, args []string) ([]pingTarget, error) {
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
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	st, err := openStore(cctx, &config{url: u, collection: t.source.Collection})
	if err != nil {
		return 0, redactErr(err, u)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Query(cctx, healthArgs(u)); err != nil {
		return 0, redactErr(err, u)
	}
	return time.Since(start), nil
}

// healthArgs returns the cheapest round-trip command for the URL's backend.
func healthArgs(rawURL string) []string {
	if strings.HasPrefix(schemeOf(rawURL), "redis") {
		return []string{"PING"}
	}
	return []string{`{"ping":1}`}
}

// redactErr returns err with any occurrence of the raw connection URL replaced by
// its redacted form, so a driver error that echoes the URL cannot leak a password.
func redactErr(err error, rawURL string) error {
	msg := err.Error()
	if strings.Contains(msg, rawURL) {
		return errors.New(strings.ReplaceAll(msg, rawURL, redactURL(rawURL)))
	}
	return err
}

// oneLine collapses an error message to a single line for tabular output.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
