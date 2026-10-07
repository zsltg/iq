package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqconfig "github.com/zsltg/iq/internal/config"
)

// addLong is the long help of `iq add`.
const addLong = "Register a source from a connection URI, like `sq add`. The URI is the only\n" +
	"positional argument; -n/--handle names the source, and when omitted a handle\n" +
	"is derived from the URI: the keyspace it pins (?collection=, ?table=,\n" +
	"?index=, …), else the MongoDB database or Cassandra keyspace name (its schema\n" +
	"container), else the dump file's stem for a file:// source, else the driver.\n" +
	"The backend is inferred from the URI scheme: redis:// (rediss://), mongodb://\n" +
	"(mongodb+srv://), cassandra://, dynamodb://, hbase://, couchdb://\n" +
	"(couchdbs://), couchbase:// (couchbases://), neo4j:// (neo4j+s://, bolt://),\n" +
	"elasticsearch:// (elasticsearch+s://), or opensearch:// (opensearch+s://);\n" +
	"-d/--driver asserts the expected driver.\n" +
	"\n" +
	"Each driver reads its own URI options. MongoDB: a default collection as\n" +
	"?collection= (`mongodb://host/db?collection=orders`). Cassandra: a default\n" +
	"table as ?table= (`cassandra://host/keyspace?table=orders`). DynamoDB: the\n" +
	"region is the host, a default table as ?table=\n" +
	"(`dynamodb://us-east-1/?table=orders`, credentials from the AWS default\n" +
	"chain). HBase: the host is the ZooKeeper quorum, a default table as ?table=\n" +
	"(`hbase://host:2181/?table=books`, cell encodings declared with\n" +
	"?types=cf:age=long). CouchDB: the host is the server, a default database as\n" +
	"?database= (`couchdb://host:5984/?database=orders`). Couchbase: the host is\n" +
	"the cluster, a bucket as ?bucket= with an optional scope.collection as\n" +
	"?collection= (`couchbase://host/?bucket=iq&collection=sales.orders`). Neo4j:\n" +
	"the host is the bolt server, a default node label as ?label= (or a\n" +
	"relationship type as ?rel=). Elasticsearch and OpenSearch: a default index as\n" +
	"?index= (`elasticsearch://host:9200/?index=books`).\n" +
	"\n" +
	"Handles may be grouped with '/' (`iq add -n prod/books mongodb://…`). -p\n" +
	"prompts for the URI password (or reads it from stdin). The password goes to the\n" +
	"OS keyring and is stripped from the stored URI. When no keyring is available,\n" +
	"iq stores the password in the config file and prints a warning. --store inline\n" +
	"keeps it in the config file, and --store keyring makes a missing keyring an\n" +
	"error. -a makes the new source active. The source is pinged before it is saved unless\n" +
	"--skip-verify is set. Note: `iq add` is this command, which shadows jq's\n" +
	"built-in `add` filter: write the filter as `[ .a, .b ] | add`."

// addExample is the example block of `iq add`.
const addExample = "  # Register a Redis source named \"cache\".\n" +
	"  $ iq add -n cache redis://localhost:6379/0\n" +
	"\n" +
	"  # Register a Mongo source; ?collection= sets its default collection and names it \"orders\".\n" +
	"  $ iq add 'mongodb://localhost:27017/shop?collection=orders'\n" +
	"\n" +
	"  # A source needing auth, made active: prompt for the password, keep it in the keyring.\n" +
	"  $ iq add -a -p 'mongodb://user@localhost:27017/shop?collection=orders'"

// addOptions holds the flag values of `iq add`.
type addOptions struct {
	handle, store, driver              string
	active, passwordPrompt, skipVerify bool
}

// newAddCmd builds `iq add <url>`: register a source from a connection URL,
// mirroring `sq add`. The URL is the sole positional; -n/--handle names the
// source, and when omitted a handle is derived from the URL. The backend is
// inferred from the URL scheme, so the scheme is validated here (the CLI owns
// backend dispatch). The source is pinged before it is saved unless
// --skip-verify is set, so a failed add leaves no trace. Note `add` shadows jq's
// built-in `add` filter at the top level; run the filter inside a larger
// expression (`[ .a, .b ] | add`) instead.
func newAddCmd(cfg *config) *cobra.Command {
	var o addOptions
	c := &cobra.Command{
		Use:               "add <uri>",
		ValidArgsFunction: cobra.NoFileCompletions,
		Short:             "Register a source from a connection URI (sq-style)",
		Long:              addLong,
		Example:           addExample,
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cmd, cfg, o, args[0])
		},
	}
	c.Flags().StringVarP(&o.handle, "handle", "n", "", "handle for the source; derived from the keyspace the URI names when omitted")
	c.Flags().StringVarP(&o.driver, "driver", "d", "", "expected backend driver (mongo, redis, cassandra, dynamodb, hbase, couchdb, couchbase, neo4j, elasticsearch, opensearch); must match the URI scheme")
	c.Flags().BoolVarP(&o.active, "active", "a", false, "make the new source the active source")
	c.Flags().BoolVarP(&o.passwordPrompt, "password", "p", false, "prompt for the URI password (or read it from stdin)")
	c.Flags().BoolVar(&o.skipVerify, "skip-verify", false, "skip the post-add reachability check")
	c.Flags().StringVar(&o.store, "store", "keyring", "where the URI's password is kept: keyring (the OS keyring) or inline (in the config file); without this flag, inline when no keyring is available")
	// Both flags take a closed set; --driver's comes from the registry, so a new
	// backend completes without a second edit.
	_ = c.RegisterFlagCompletionFunc("driver", fixedValues(driverNameList()...))
	_ = c.RegisterFlagCompletionFunc("store", fixedValues(passwordStoreNames...))
	return c
}

// runAdd registers the source rawURL. It checks the URI, plans the password,
// verifies the source, and saves it.
func runAdd(cmd *cobra.Command, cfg *config, o addOptions, rawURL string) error {
	rawURL, err := prepareAddURI(cmd, o, rawURL)
	if err != nil {
		return err
	}
	plan, err := planAddPassword(cmd, rawURL, o.store)
	if err != nil {
		return err
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	name := addHandle(cmd, cf, o, rawURL)
	if err := cf.Add(name, plan.stored); err != nil {
		return err
	}
	// Verify reachability before persisting so a failed add leaves no
	// trace. rawURL still carries the password (stripped from plan.stored
	// for a keyring source), so it is what we dial.
	if !o.skipVerify {
		if err := verifyAdd(cmd.Context(), name, rawURL, cfg.timeout); err != nil {
			return err
		}
	}
	return finishAdd(cmd, cf, addedSource{name: name, active: o.active, plan: plan})
}

// prepareAddURI checks rawURL against the driver flag and the registry. It then
// returns rawURL with the prompted password inserted, when the user asked to be
// prompted.
func prepareAddURI(cmd *cobra.Command, o addOptions, rawURL string) (string, error) {
	if err := validateAddURI(rawURL, o.driver); err != nil {
		return "", err
	}
	// A prompted password is spliced into the URL before storage, so the
	// keyring/inline path below handles it uniformly.
	if !o.passwordPrompt {
		return rawURL, nil
	}
	pw, err := readPassword(cmd)
	if err != nil {
		return "", err
	}
	return injectPassword(rawURL, pw)
}

// validateAddURI makes sure that rawURL names a known driver scheme that matches
// driverFlag, and that its query holds no keyspace parameter the driver cannot
// use.
func validateAddURI(rawURL, driverFlag string) error {
	if driverFlag != "" {
		if _, ok := driverByName(driverFlag); !ok {
			return fmt.Errorf("unknown driver %q; known drivers: %s", driverFlag, driverNames())
		}
	}
	if !supportedScheme(rawURL) {
		return fmt.Errorf("unsupported URI scheme %q; %s", schemeOf(rawURL), expectedSchemes())
	}
	if driverFlag != "" && driverName(rawURL) != driverFlag {
		return fmt.Errorf("--driver %q does not match URI scheme %q:// (driver %q)", driverFlag, schemeOf(rawURL), driverName(rawURL))
	}
	return urlAddressUnsupported(rawURL)
}

// planAddPassword reads the --store value and decides where the password goes.
func planAddPassword(cmd *cobra.Command, rawURL, store string) (passwordPlan, error) {
	useKeyring, err := parseStore(store)
	if err != nil {
		return passwordPlan{}, err
	}
	return planPassword(rawURL, useKeyring, cmd.Flags().Changed("store"))
}

// addHandle returns the handle for the new source: the -n value when the user
// gave one, else a handle derived from rawURL.
func addHandle(cmd *cobra.Command, cf *iqconfig.Config, o addOptions, rawURL string) string {
	if cmd.Flags().Changed("handle") {
		return o.handle
	}
	return suggestHandle(cf, rawURL)
}

// verifyAdd opens the new source and pings it. It wraps a failure with the handle
// and the --skip-verify hint.
func verifyAdd(ctx context.Context, name, rawURL string, timeout time.Duration) error {
	if err := verifySource(ctx, rawURL, timeout); err != nil {
		return fmt.Errorf("verify %s: %w (use --skip-verify to add it anyway)", strings.TrimPrefix(name, "@"), err)
	}
	return nil
}

// addedSource is a source that `iq add` has put in the config in memory: its
// handle, whether to make it active, and where its password goes.
type addedSource struct {
	name   string
	active bool
	plan   passwordPlan
}

// finishAdd makes the source active when asked, saves it, and prints the report.
func finishAdd(cmd *cobra.Command, cf *iqconfig.Config, src addedSource) error {
	if src.active {
		if err := cf.SetActive(src.name); err != nil {
			return err
		}
	}
	if err := saveSource(cmd.ErrOrStderr(), cf, src.name, src.plan); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "added source %s\n", strings.TrimPrefix(src.name, "@"))
	return err
}

// suggestHandle derives a source handle from rawURL when -n is omitted, mirroring
// sq: the most specific container the URL names (see handleBase), falling back to
// the driver name (redis, mongo). The candidate is sanitized to the handle
// alphabet and made unique against existing sources by appending 2, 3, … on
// collision.
func suggestHandle(cf *iqconfig.Config, rawURL string) string {
	base := sanitizeHandle(handleBase(rawURL))
	if base == "" {
		base = "source"
	}
	name := base
	for i := 2; ; i++ {
		if _, exists := cf.Sources[iqconfig.CleanHandle(name)]; !exists {
			return name
		}
		name = base + strconv.Itoa(i)
	}
}

// handleBase picks the raw handle stem for a URL: the most specific container the
// URL names, in order — the keyspace a driver-owned query param pins
// (?collection=, ?table=, ?index=, …), the dump file's stem for a file source,
// the first path segment when it is a non-numeric name (a MongoDB database or
// Cassandra keyspace), else the driver name. A multi-host Mongo URI that net/url
// cannot parse falls back to the driver name too.
func handleBase(rawURL string) string {
	if a := urlAddressName(rawURL); a != "" {
		return lastSegment(a)
	}
	if stem, ok := fileStem(rawURL); ok {
		return stem
	}
	if u, err := url.Parse(rawURL); err == nil {
		seg := strings.TrimLeft(u.Path, "/")
		if i := strings.IndexByte(seg, '/'); i >= 0 {
			seg = seg[:i]
		}
		if seg != "" && !isAllDigits(seg) {
			return seg
		}
	}
	return driverName(rawURL)
}

// lastSegment reduces a dotted keyspace spec to its final part (Couchbase's
// ?collection=sales.orders names collection "orders"). A dotted handle is a legal
// name but shadows `handle.address` addressing, so a derived one never carries a
// dot. LastIndexByte reports -1 for an undotted spec, so the slice then starts at
// 0 and the whole spec is the last segment.
func lastSegment(s string) string {
	return s[strings.LastIndexByte(s, '.')+1:]
}

// fileStem returns the dump file's base name without its extension for a local
// dump source (file:///dumps/books.json names "books") — the container such a
// source reads, since its path names a file rather than a database. DumpPath
// rejects every non-file:// URL, so a connected backend reports false here, as
// does a file URL naming no usable stem (file:/// is the bare root,
// file:///dumps/.json is all extension); the caller then falls through to the
// path and driver-name rules.
func fileStem(rawURL string) (string, bool) {
	path, err := iqfile.DumpPath(rawURL)
	if err != nil {
		return "", false
	}
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" || stem == string(filepath.Separator) {
		return "", false
	}
	return stem, true
}

// isAllDigits reports whether s is non-empty and every rune is a decimal digit,
// so a Redis "/0" database index is not mistaken for a handle stem.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeHandle drops every rune outside the single-segment handle alphabet
// (letters, digits, '.', '_', '-'), so a derived stem is a valid handle.
func sanitizeHandle(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// readPassword obtains a password for -p: read a line from stdin when it is piped
// (so scripts and tests can feed one in), otherwise prompt without echo on the
// terminal. The prompt and trailing newline go to stderr so redirected stdout
// stays clean.
func readPassword(cmd *cobra.Command) (string, error) {
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		errOut := cmd.ErrOrStderr()
		_, _ = fmt.Fprint(errOut, "Password: ")
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(errOut)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// parseStore validates the --store value, returning whether the password should
// go to the OS keyring (the default). inline keeps it in the stored URL.
func parseStore(store string) (keyring bool, err error) {
	switch store {
	case "inline":
		return false, nil
	case "keyring":
		return true, nil
	default:
		return false, fmt.Errorf("unknown --store %q: want inline or keyring", store)
	}
}

// newLsCmd builds `iq ls [group]`: list saved sources (or, with -g, groups). URLs
// are redacted so a stored password is never printed unless --reveal (inline
// passwords) or --expand (keyring passwords) is set.
func newLsCmd(cfg *config) *cobra.Command {
	var groups, jsonOut, yamlOut bool
	c := &cobra.Command{
		Use:               "ls [group]",
		ValidArgsFunction: completeGroups,
		Short:             "List saved sources (the active one marked *), or groups with -g",
		Long: "List saved sources, the active one marked with '*'. An optional [group] limits\n" +
			"the listing to sources in that group. -v adds a header row, FORMAT and OPTIONS\n" +
			"columns, and a [keyring] tag on keyring-backed sources; -g lists\n" +
			"groups instead of sources; -j/--json or -y/--yaml emit machine-readable output.\n" +
			"Passwords are redacted by default: --reveal prints a password stored inline in\n" +
			"the config verbatim, and --expand resolves a keyring-backed source's stored\n" +
			"password and inlines it (combine both to print a keyring password verbatim).",
		Example: "  $ iq ls               # list saved sources (active marked *)\n" +
			"  $ iq ls -v            # add the driver column\n" +
			"  $ iq ls -g            # list groups instead of sources\n" +
			"  $ iq ls -j            # machine-readable JSON\n" +
			"  $ iq ls prod --reveal # one group, inline passwords shown",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if groups {
				return listGroups(out, cf, lsOptions{verbose: cfg.verbose, jsonOut: jsonOut, yamlOut: yamlOut})
			}
			filter := ""
			if len(args) == 1 {
				filter = iqconfig.CleanHandle(args[0])
			}
			return listSources(out, cf, lsOptions{filter: filter, verbose: cfg.verbose, reveal: cfg.reveal, expand: cfg.expand, jsonOut: jsonOut, yamlOut: yamlOut})
		},
	}
	// -v is the global --verbose (registered on the root); `iq ls -v` reuses it
	// for the driver column, so no local -v is declared here.
	c.Flags().BoolVarP(&groups, "group", "g", false, "list groups instead of sources")
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.MarkFlagsMutuallyExclusive("json", "yaml")
	c.Flags().BoolVar(&cfg.reveal, "reveal", false, "print inline-stored passwords verbatim instead of redacting them")
	c.Flags().BoolVar(&cfg.expand, "expand", false, "resolve keyring-backed passwords and inline them into printed URLs")
	return c
}

// newRmCmd builds `iq rm <name>...`: remove one or more saved sources or groups.
func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "rm <name>...",
		ValidArgsFunction: completeHandlesAndGroups,
		Short:             "Remove one or more saved sources or groups",
		Long: "Remove saved sources or whole groups. Each argument is a source handle or a\n" +
			"group name (which removes every source under it). The removal is atomic: if any\n" +
			"argument names neither a source nor a group, nothing is removed. A keyring-backed\n" +
			"source's stored credential is deleted too.",
		Example: "  $ iq rm cache               # remove one source\n" +
			"  $ iq rm cache shop prod/old # several at once\n" +
			"  $ iq rm prod                # a whole group",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			removed, err := cf.RemoveAll(args)
			if err != nil {
				return err
			}
			if err := cf.Save(); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, r := range removed {
				if r.Source.Keyring {
					_ = keyringStore.Delete(r.Handle)
				}
				if _, err := fmt.Fprintf(out, "removed source %s\n", r.Handle); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// newMvCmd builds `iq mv <old> <new>`: rename or move a source or a whole group.
func newMvCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "mv <old> <new>",
		ValidArgsFunction: firstArgOnly(completeHandlesAndGroups),
		Short:             "Rename or move a source or a whole group",
		Long: "Rename a source to a new full handle, or move it into a group by giving the\n" +
			"group-qualified target (`iq mv books prod/books`). When <old> names a group, every\n" +
			"source under it is re-prefixed (`iq mv prod staging` renames prod/* to staging/*).\n" +
			"The active source and group follow the move. A keyring-backed source's stored\n" +
			"credential moves with it.",
		Example: "  $ iq mv shop catalog   # rename a source\n" +
			"  $ iq mv shop prod/shop # move into a group\n" +
			"  $ iq mv prod staging   # rename a whole group (prod/* -> staging/*)",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMv(cmd, args[0], args[1])
		},
	}
}

// runMv renames or moves a source or group, and moves the keyring entries with it.
func runMv(cmd *cobra.Command, oldName, newName string) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	moved, err := cf.Move(oldName, newName)
	if err != nil {
		return err
	}
	if len(moved) == 0 {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "nothing to move")
		return err
	}
	staged, err := stageKeyringMoves(cf, moved)
	if err != nil {
		return err
	}
	if err := commitMove(cf, staged); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "moved %s to %s\n",
		strings.TrimPrefix(oldName, "@"), strings.TrimPrefix(newName, "@"))
	return err
}

// stageKeyringMoves copies each keyring secret of a moved source to its new
// handle. It leaves the old entry in place, so that a failed Save rolls back
// without data loss. It returns the renames that it staged.
func stageKeyringMoves(cf *iqconfig.Config, moved []iqconfig.Rename) ([]iqconfig.Rename, error) {
	staged := make([]iqconfig.Rename, 0, len(moved))
	for _, r := range moved {
		if !cf.Sources[r.New].Keyring {
			continue
		}
		pw, gErr := keyringStore.Get(r.Old)
		if gErr != nil {
			continue // no stored credential to migrate
		}
		if sErr := keyringStore.Set(r.New, pw); sErr != nil {
			return nil, sErr
		}
		staged = append(staged, r)
	}
	return staged, nil
}

// commitMove saves the config. If the save fails, it deletes the staged new
// entries. Otherwise it deletes the old entries.
func commitMove(cf *iqconfig.Config, staged []iqconfig.Rename) error {
	if err := cf.Save(); err != nil {
		for _, r := range staged {
			_ = keyringStore.Delete(r.New)
		}
		return err
	}
	for _, r := range staged {
		_ = keyringStore.Delete(r.Old)
	}
	return nil
}

// newSrcCmd builds `iq src [name]`: show the active source, or set it.
func newSrcCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "src [name]",
		ValidArgsFunction: completeSourceHandles,
		Short:             "Show or set the active source",
		Example: "  $ iq src      # show the active source\n" +
			"  $ iq src shop # set it",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				return showCurrent(out, cf.Active, "no active source")
			}
			if err := cf.SetActive(args[0]); err != nil {
				return err
			}
			if err := cf.Save(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "active source: %s\n", cf.Active)
			return err
		},
	}
}

// showCurrent prints current, or none when current is empty.
func showCurrent(out io.Writer, current, none string) error {
	if current == "" {
		current = none
	}
	_, err := fmt.Fprintln(out, current)
	return err
}

// newGroupCmd builds `iq group [name]`: show, set, or (with --clear) clear the
// active group. An active group lets an unqualified source name resolve within
// it, so `iq src books` finds `prod/books` when the group is `prod`.
func newGroupCmd() *cobra.Command {
	var clear bool
	c := &cobra.Command{
		Use:               "group [name]",
		ValidArgsFunction: completeGroups,
		Short:             "Show, set, or clear the active group",
		Example: "  $ iq group         # show the active group\n" +
			"  $ iq group prod    # set it (handles resolve within it)\n" +
			"  $ iq group --clear # back to top-level handles",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGroup(cmd, args, clear)
		},
	}
	c.Flags().BoolVar(&clear, "clear", false, "clear the active group")
	return c
}

// runGroup shows, sets, or clears the active group. --clear wins over a
// positional name.
func runGroup(cmd *cobra.Command, args []string, clear bool) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	switch {
	case clear:
		if err := saveGroup(cf, ""); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "cleared active group")
		return err
	case len(args) == 0:
		return showCurrent(out, cf.Group, "no active group")
	default:
		if err := saveGroup(cf, args[0]); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "active group: %s\n", cf.Group)
		return err
	}
}

// saveGroup sets the active group to name and saves the config.
func saveGroup(cf *iqconfig.Config, name string) error {
	if err := cf.SetGroup(name); err != nil {
		return err
	}
	return cf.Save()
}

// redactURL returns raw with any password in its userinfo replaced by "xxxxx",
// so a stored credential is never printed. url.Parse handles multi-host Mongo
// URIs (mongodb://h1,h2/db); an unparseable string yields a safe placeholder
// rather than risk leaking a credential we could not locate.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URI)"
	}
	return u.Redacted()
}
