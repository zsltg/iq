package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// pingTarget is one source to check: its full handle and stored source.
type pingTarget struct {
	handle string
	source iqconfig.Source
}

// newPingCmd builds `iq ping [name...]`: check that sources are reachable. With
// no arguments it pings the active source. Otherwise each argument names a source
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
			return runPing(cmd, cfg, args, all)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "ping every saved source")
	return c
}

// runPing checks each target and prints one table row per target. It returns an
// error after the table when any check failed.
func runPing(cmd *cobra.Command, cfg *config, args []string, all bool) error {
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
		d, perr := pingOne(cmd.Context(), keyringFor(cf), t, cfg.timeout)
		anyFail = anyFail || perr != nil
		rows = append(rows, pingRow(t, d, perr))
	}
	if err := renderTable(cmd.OutOrStdout(), rows); err != nil {
		return err
	}
	if anyFail {
		return errors.New("one or more sources unreachable")
	}
	return nil
}

// pingRow builds the table row of one check: the handle, the driver, and a
// result. The result is either "ok" with the elapsed time or "error" with the
// message on one line.
func pingRow(t pingTarget, elapsed time.Duration, err error) []tableCell {
	driver := driverName(t.source.URL)
	if err != nil {
		return []tableCell{cell(t.handle), cell(driver), coloredCell("error", pal.fail), cell(oneLine(err))}
	}
	return []tableCell{cell(t.handle), cell(driver), coloredCell("ok", pal.ok), cell(elapsed.Round(time.Millisecond).String())}
}

// pingTargets resolves the ping arguments to sources. When all is set, the
// targets are every saved source. When no arguments are given, the target is the
// active source. Otherwise each argument is a source or a group (expanded to its
// members). It errors if --all is combined with arguments or finds no saved
// sources. It also errors if there is no active source or an argument is
// unknown.
func pingTargets(cf *iqconfig.Config, args []string, all bool) ([]pingTarget, error) {
	switch {
	case all:
		return allPingTargets(cf, args)
	case len(args) == 0:
		return activePingTarget(cf)
	default:
		return namedPingTargets(cf, args)
	}
}

// allPingTargets returns every saved source. It refuses source arguments and an
// empty registry, in that order.
func allPingTargets(cf *iqconfig.Config, args []string) ([]pingTarget, error) {
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

// activePingTarget returns the active source as the one target.
func activePingTarget(cf *iqconfig.Config) ([]pingTarget, error) {
	if cf.Active == "" {
		return nil, errors.New("no active source; name one or run `iq src <name>`")
	}
	s, full, ok := cf.Resolve(cf.Active)
	if !ok {
		return nil, fmt.Errorf("active source %q not found; run `iq ls`", cf.Active)
	}
	return []pingTarget{{handle: full, source: s}}, nil
}

// namedPingTargets resolves each argument as a source or a group. It collects the
// unknown names and reports them together.
func namedPingTargets(cf *iqconfig.Config, args []string) ([]pingTarget, error) {
	set := pingSet{seen: make(map[string]bool)}
	var unknown []string
	for _, arg := range args {
		if s, full, ok := cf.Resolve(arg); ok {
			set.add(full, s)
			continue
		}
		if !set.addGroup(cf, arg) {
			unknown = append(unknown, iqconfig.CleanHandle(arg))
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown source or group: %s", strings.Join(unknown, ", "))
	}
	return set.targets, nil
}

// pingSet collects targets and keeps the first one of each handle.
type pingSet struct {
	targets []pingTarget
	seen    map[string]bool
}

// add appends the target unless its handle is already in the set.
func (p *pingSet) add(handle string, s iqconfig.Source) {
	if p.seen[handle] {
		return
	}
	p.targets = append(p.targets, pingTarget{handle: handle, source: s})
	p.seen[handle] = true
}

// addGroup adds every source under the group that arg names. It reports whether
// the group has a member.
func (p *pingSet) addGroup(cf *iqconfig.Config, arg string) bool {
	prefix := iqconfig.CleanHandle(arg) + "/"
	found := false
	for _, h := range cf.List() {
		if strings.HasPrefix(h.Name, prefix) {
			p.add(h.Name, h.Source)
			found = true
		}
	}
	return found
}

// pingOne opens the target and round-trips one cheap command, returning the
// elapsed time. Any error has the connection URL redacted so a stored password
// never surfaces in a diagnostic message.
func pingOne(ctx context.Context, kr secret.Keyring, t pingTarget, timeout time.Duration) (time.Duration, error) {
	u, err := effectiveURL(kr, t.source, t.handle)
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
	if opensAreEnough(rawURL) {
		return nil
	}
	if _, err := st.Query(cctx, healthArgs(rawURL)); err != nil {
		return redactErr(err, rawURL)
	}
	return nil
}

// opensAreEnough reports whether opening the backend already proves that it is
// reachable. A read-only local source (a dump file) has no server to round-trip a
// command against. A verifiesOnOpen backend (DynamoDB) already round-tripped a
// reachability probe at open.
func opensAreEnough(rawURL string) bool {
	d, ok := driverForScheme(schemeOf(rawURL))
	if !ok {
		return false
	}
	return d.readOnly || d.verifiesOnOpen
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

// passwordMask is the text that url.URL.Redacted puts in place of a password.
// redactURL and maskPassword both use it.
const passwordMask = "xxxxx"

// minMaskedPassword is the shortest password that maskPassword removes when it
// stands alone in a message. A shorter password can match unrelated text. A
// password of any length is still removed when it sits between ":" and "@".
const minMaskedPassword = 4

// maskPassword replaces the password of rawURL in msg with passwordMask. It
// matches the raw password and its escaped forms. It applies two rules. The
// first rule masks every copy of the password between ":" and "@", for a
// password of any length, because that shape is a piece of a URI. The second
// rule masks every bare copy, but only when the password has at least
// minMaskedPassword bytes. It does nothing when rawURL has no password or does
// not parse. This catches a driver error that quotes a piece of the URI without
// a scheme, which the pattern step of newRedactor cannot find.
func maskPassword(msg, rawURL string) string {
	pw, ok := sourcePassword(rawURL)
	if !ok {
		return msg
	}
	// The raw form goes last, so a longer escaped form wins at the same position.
	// url.UserPassword escapes a little differently from the two functions.
	forms := []string{
		url.PathEscape(pw),
		url.QueryEscape(pw),
		strings.TrimPrefix(url.UserPassword("", pw).String(), ":"),
		pw,
	}
	pairs := make([]string, 0, 4*len(forms))
	// A credential pair starts at ":", so it wins over a bare pair for the same text.
	for _, f := range forms {
		pairs = append(pairs, ":"+f+"@", ":"+passwordMask+"@")
	}
	if len(pw) >= minMaskedPassword {
		for _, f := range forms {
			pairs = append(pairs, f, passwordMask)
		}
	}
	return strings.NewReplacer(pairs...).Replace(msg)
}

// sourcePassword returns the password in the user info of rawURL. It reports
// false when rawURL does not parse, has no user info, or has an empty password.
func sourcePassword(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	// Password reports false for a nil *url.Userinfo, so no user info needs no check.
	pw, ok := u.User.Password()
	return pw, ok && pw != ""
}

// redactErr returns err with the connection password removed from its message,
// so a driver error cannot leak it. It first replaces each occurrence of the raw
// connection URI with its redacted form, then applies newRedactor, then masks
// each literal copy of the password with maskPassword. rawURL can be
// empty. It returns err itself when the message does not change, so errors.Is
// and errors.As still see the chain.
func redactErr(err error, rawURL string) error {
	msg := err.Error()
	out := msg
	if rawURL != "" {
		out = strings.ReplaceAll(out, rawURL, redactURL(rawURL))
	}
	out = maskPassword(newRedactor(err)(out), rawURL)
	if out == msg {
		return err
	}
	return errors.New(out)
}

// oneLine collapses an error message to a single line for tabular output.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
