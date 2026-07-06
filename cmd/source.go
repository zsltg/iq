package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// newAddCmd builds `iq add <url>`: register a source from a connection URL,
// mirroring `sq add`. The URL is the sole positional; -n/--handle names the
// source, and when omitted a handle is derived from the URL. The backend is
// inferred from the URL scheme, so the scheme is validated here (the CLI owns
// backend dispatch). The source is pinged before it is saved unless
// --skip-verify is set, so a failed add leaves no trace. Note `add` shadows jq's
// built-in `add` filter at the top level; run the filter inside a larger
// expression (`[ .a, .b ] | add`) instead.
func newAddCmd(cfg *config) *cobra.Command {
	var (
		handle         string
		store          string
		driverFlag     string
		active         bool
		passwordPrompt bool
		skipVerify     bool
	)
	c := &cobra.Command{
		Use:   "add <url>",
		Short: "Register a source from a connection URL (sq-style)",
		Long: "Register a source from a connection URL, like `sq add`. The URL is the only\n" +
			"positional argument; -n/--handle names the source, and when omitted a handle is\n" +
			"derived from the URL (the MongoDB database name, else the driver). The backend is\n" +
			"inferred from the URL scheme: redis:// (rediss://) or mongodb:// (mongodb+srv://);\n" +
			"-d/--driver asserts the expected driver. For MongoDB, a default collection rides\n" +
			"in the URL as ?collection= (`mongodb://host/db?collection=orders`). Handles may be\n" +
			"grouped with '/' (`iq add -n prod/books\n" +
			"mongodb://...`). -p prompts for the URL password (or reads it from stdin); with\n" +
			"--store keyring the password is moved to the OS keyring and stripped from the\n" +
			"stored URL. -a makes the new source active. The source is pinged before it is\n" +
			"saved unless --skip-verify is set. Note: `iq add` is this command, which shadows\n" +
			"jq's built-in `add` filter — write the filter as `[ .a, .b ] | add`.",
		Example: "  # Register a Redis source named \"cache\".\n" +
			"  $ iq add -n cache redis://localhost:6379/0\n" +
			"\n" +
			"  # Register a Mongo source; ?collection= sets its default collection.\n" +
			"  $ iq add 'mongodb://localhost:27017/shop?collection=orders'\n" +
			"\n" +
			"  # A source needing auth, made active: prompt for the password, keep it in the keyring.\n" +
			"  $ iq add -a -p --store keyring 'mongodb://user@localhost:27017/shop?collection=orders'",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawURL := args[0]
			if driverFlag != "" {
				if _, ok := driverByName(driverFlag); !ok {
					return fmt.Errorf("unknown driver %q; known drivers: %s", driverFlag, driverNames())
				}
			}
			if !supportedScheme(rawURL) {
				return fmt.Errorf("unsupported url scheme %q; %s", schemeOf(rawURL), expectedSchemes())
			}
			if driverFlag != "" && driverName(rawURL) != driverFlag {
				return fmt.Errorf("--driver %q does not match url scheme %q:// (driver %q)", driverFlag, schemeOf(rawURL), driverName(rawURL))
			}
			if err := urlAddressUnsupported(rawURL); err != nil {
				return err
			}
			// A prompted password is spliced into the URL before storage, so the
			// keyring/inline path below handles it uniformly.
			if passwordPrompt {
				pw, err := readPassword(cmd)
				if err != nil {
					return err
				}
				rawURL, err = injectPassword(rawURL, pw)
				if err != nil {
					return err
				}
			}
			useKeyring, err := parseStore(store)
			if err != nil {
				return err
			}
			storedURL := rawURL
			password := ""
			if useKeyring {
				stripped, pw, ok, err := splitPassword(rawURL)
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("--store keyring: url has no password to store")
				}
				storedURL, password = stripped, pw
			}
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			name := handle
			if !cmd.Flags().Changed("handle") {
				name = suggestHandle(cf, rawURL)
			}
			if err := cf.Add(name, storedURL); err != nil {
				return err
			}
			// Verify reachability before persisting so a failed add leaves no
			// trace. rawURL still carries the password (stripped from storedURL for
			// a keyring source), so it is what we dial.
			if !skipVerify {
				if err := verifySource(cmd.Context(), rawURL, cfg.timeout); err != nil {
					return fmt.Errorf("verify %s: %w (use --skip-verify to add it anyway)", strings.TrimPrefix(name, "@"), err)
				}
			}
			if active {
				if err := cf.SetActive(name); err != nil {
					return err
				}
			}
			if useKeyring {
				if err := cf.UseKeyring(name); err != nil {
					return err
				}
				if err := keyringStore.Set(iqconfig.CleanHandle(name), password); err != nil {
					return err
				}
			}
			if err := cf.Save(); err != nil {
				if useKeyring {
					_ = keyringStore.Delete(iqconfig.CleanHandle(name))
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "added source %s\n", strings.TrimPrefix(name, "@"))
			return err
		},
	}
	c.Flags().StringVarP(&handle, "handle", "n", "", "handle for the source; derived from the url when omitted")
	c.Flags().StringVarP(&driverFlag, "driver", "d", "", "expected backend driver (mongo, redis); must match the url scheme")
	c.Flags().BoolVarP(&active, "active", "a", false, "make the new source the active source")
	c.Flags().BoolVarP(&passwordPrompt, "password", "p", false, "prompt for the url password (or read it from stdin)")
	c.Flags().BoolVar(&skipVerify, "skip-verify", false, "skip the post-add reachability check")
	c.Flags().StringVar(&store, "store", "inline", "where the url's password is kept: inline (in the config file) or keyring (the OS keyring)")
	return c
}

// suggestHandle derives a source handle from rawURL when -n is omitted, mirroring
// sq: the MongoDB database name when the URL names one, otherwise the driver name
// (redis, mongo). The candidate is sanitized to the handle alphabet and made
// unique against existing sources by appending 2, 3, … on collision.
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

// handleBase picks the raw handle stem for a URL: the first path segment when it
// is a non-numeric name (a MongoDB database), else the driver name. A multi-host
// Mongo URI that net/url cannot parse falls back to the driver name too.
func handleBase(rawURL string) string {
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
// go to the OS keyring. inline (the default) keeps it in the stored URL.
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
		Use:   "ls [group]",
		Short: "List saved sources (the active one marked *), or groups with -g",
		Long: "List saved sources, the active one marked with '*'. An optional [group] limits\n" +
			"the listing to sources in that group. -v adds each source's driver; -g lists\n" +
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
				return listGroups(out, cf, cfg.verbose, jsonOut, yamlOut)
			}
			filter := ""
			if len(args) == 1 {
				filter = iqconfig.CleanHandle(args[0])
			}
			return listSources(out, cf, filter, cfg.verbose, cfg.reveal, jsonOut, yamlOut, cfg.expand)
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
		Use:   "rm <name>...",
		Short: "Remove one or more saved sources or groups",
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
		Use:   "mv <old> <new>",
		Short: "Rename or move a source or a whole group",
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
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			moved, err := cf.Move(args[0], args[1])
			if err != nil {
				return err
			}
			if len(moved) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "nothing to move")
				return err
			}
			// Stage each keyring credential under its new handle (leaving the old
			// entry in place) so a failed Save rolls back without data loss.
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
					return sErr
				}
				staged = append(staged, r)
			}
			if err := cf.Save(); err != nil {
				for _, r := range staged {
					_ = keyringStore.Delete(r.New)
				}
				return err
			}
			for _, r := range staged {
				_ = keyringStore.Delete(r.Old)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "moved %s to %s\n",
				strings.TrimPrefix(args[0], "@"), strings.TrimPrefix(args[1], "@"))
			return err
		},
	}
}

// newSrcCmd builds `iq src [name]`: show the active source, or set it.
func newSrcCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "src [name]",
		Short: "Show or set the active source",
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
				if cf.Active == "" {
					_, err := fmt.Fprintln(out, "no active source")
					return err
				}
				_, err := fmt.Fprintln(out, cf.Active)
				return err
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

// newGroupCmd builds `iq group [name]`: show, set, or (with --clear) clear the
// active group. An active group lets an unqualified source name resolve within
// it, so `iq src books` finds `prod/books` when the group is `prod`.
func newGroupCmd() *cobra.Command {
	var clear bool
	c := &cobra.Command{
		Use:   "group [name]",
		Short: "Show, set, or clear the active group",
		Example: "  $ iq group         # show the active group\n" +
			"  $ iq group prod    # set it (handles resolve within it)\n" +
			"  $ iq group --clear # back to top-level handles",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case clear:
				if err := cf.SetGroup(""); err != nil {
					return err
				}
				if err := cf.Save(); err != nil {
					return err
				}
				_, err = fmt.Fprintln(out, "cleared active group")
				return err
			case len(args) == 0:
				if cf.Group == "" {
					_, err := fmt.Fprintln(out, "no active group")
					return err
				}
				_, err := fmt.Fprintln(out, cf.Group)
				return err
			default:
				if err := cf.SetGroup(args[0]); err != nil {
					return err
				}
				if err := cf.Save(); err != nil {
					return err
				}
				_, err = fmt.Fprintf(out, "active group: %s\n", cf.Group)
				return err
			}
		},
	}
	c.Flags().BoolVar(&clear, "clear", false, "clear the active group")
	return c
}

// redactURL returns raw with any password in its userinfo replaced by "xxxxx",
// so a stored credential is never printed. url.Parse handles multi-host Mongo
// URIs (mongodb://h1,h2/db); an unparseable string yields a safe placeholder
// rather than risk leaking a credential we could not locate.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable url)"
	}
	return u.Redacted()
}
