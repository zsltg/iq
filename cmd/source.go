package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
)

// newAddCmd builds `iq add <name> <url>`: register a named source. The backend
// is inferred from the URL scheme, so the scheme is validated here (the CLI owns
// backend dispatch). Note `add` shadows jq's built-in `add` filter at the top
// level; run the filter inside a larger expression (`[ .a, .b ] | add`) instead.
func newAddCmd() *cobra.Command {
	var collection string
	var store string
	c := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Register a named source (a connection URL, optionally a MongoDB collection)",
		Long: "Register a named source. The backend is inferred from the URL scheme:\n" +
			"redis:// (rediss://) or mongodb:// (mongodb+srv://). For MongoDB, -c names the\n" +
			"collection stored with the source. Names may be grouped with '/' (`iq add\n" +
			"prod/books mongodb://...`). With --store keyring the URL's password is moved to\n" +
			"the OS keyring and stripped from the stored URL. Note: `iq add` is this command,\n" +
			"which shadows jq's built-in `add` filter — write the filter as `[ .a, .b ] | add`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rawURL := args[0], args[1]
			if !supportedScheme(rawURL) {
				return fmt.Errorf("unsupported url scheme %q; expected redis:// or mongodb://", schemeOf(rawURL))
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
			if err := cf.Add(name, storedURL, collection); err != nil {
				return err
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
	c.Flags().StringVarP(&collection, "collection", "c", "", "MongoDB collection stored with this source (ignored for Redis)")
	c.Flags().StringVar(&store, "store", "inline", "where the url's password is kept: inline (in the config file) or keyring (the OS keyring)")
	return c
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
// are redacted so a stored password is never printed unless --reveal is set.
func newLsCmd() *cobra.Command {
	var verbose, groups, jsonOut bool
	c := &cobra.Command{
		Use:   "ls [group]",
		Short: "List saved sources (the active one marked *), or groups with -g",
		Long: "List saved sources, the active one marked with '*'. An optional [group] limits\n" +
			"the listing to sources in that group. -v adds each source's driver; -g lists\n" +
			"groups instead of sources; --json emits machine-readable output. Passwords are\n" +
			"redacted unless --reveal is given.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if groups {
				return listGroups(out, cf, verbose, jsonOut)
			}
			filter := ""
			if len(args) == 1 {
				filter = iqconfig.CleanHandle(args[0])
			}
			return listSources(out, cf, filter, verbose, jsonOut, false)
		},
	}
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "show each source's driver alongside its location")
	c.Flags().BoolVarP(&groups, "group", "g", false, "list groups instead of sources")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
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
		Args:  cobra.MaximumNArgs(1),
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
		Args:  cobra.MaximumNArgs(1),
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
