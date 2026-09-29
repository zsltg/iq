package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/secret"
)

// keyringRow is the JSON/YAML shape of one entry in `iq config keyring ls`:
// the source handle and whether its secret is present in the OS keyring.
type keyringRow struct {
	Handle string `json:"handle"`
	Status string `json:"status"` // present | missing
}

// keyringStaged records a keyring entry a migrate run wrote, so a failure before
// Save can roll each one back.
type keyringStaged struct{ full, clean string }

// newConfigKeyringCmd builds `iq config keyring`: manage the OS-keyring secrets
// that back keyring-stored sources (`iq add` stores a password there by default). The
// underlying keyring library cannot enumerate entries, so every listing and
// pruning path is driven off the config's source list, never the keyring itself.
func newConfigKeyringCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:   "keyring",
		Short: "Manage the OS-keyring secrets backing keyring-stored sources",
		Long: "Manage the OS-keyring secrets that back keyring-stored sources (see `iq add`).\n" +
			"`ls` lists keyring-backed sources and whether their secret is\n" +
			"present; `get`/`set`/`rm` read, write, and delete one secret; `migrate` moves an\n" +
			"inline password into the keyring; `prune` deletes stale entries. The keyring\n" +
			"library cannot enumerate entries, so these commands reason only about handles the\n" +
			"config knows, a secret whose source was deleted is undetectable here.",
		Example: "  $ iq config keyring ls              # keyring-backed sources & secret presence\n" +
			"  $ iq config keyring migrate shop    # move an inline password into the keyring\n" +
			"  $ iq config keyring get shop --reveal # read a secret\n" +
			"  $ iq config keyring set shop        # write/update a secret\n" +
			"  $ iq config keyring rm shop         # delete a secret",
	}
	c.AddCommand(
		newConfigKeyringLsCmd(),
		newConfigKeyringGetCmd(cfg),
		newConfigKeyringSetCmd(),
		newConfigKeyringRmCmd(),
		newConfigKeyringMigrateCmd(),
		newConfigKeyringPruneCmd(),
	)
	return c
}

// newConfigKeyringLsCmd builds `iq config keyring ls`: list keyring-backed
// sources, each marked present or missing by probing the keyring for its secret.
func newConfigKeyringLsCmd() *cobra.Command {
	var jsonOut, yamlOut bool
	c := &cobra.Command{
		Use:     "ls",
		Short:   "List keyring-backed sources and whether each secret is present",
		Example: "  $ iq config keyring ls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			rows := make([]keyringRow, 0)
			for _, h := range cf.List() {
				if !h.Source.Keyring {
					continue
				}
				status := "present"
				if _, gErr := keyringStore.Get(iqconfig.CleanHandle(h.Name)); gErr != nil {
					if !errors.Is(gErr, secret.ErrNotFound) {
						return gErr
					}
					status = "missing"
				}
				rows = append(rows, keyringRow{Handle: h.Name, Status: status})
			}
			out := cmd.OutOrStdout()
			if jsonOut || yamlOut {
				return writeStructured(out, rows, yamlOut)
			}
			if len(rows) == 0 {
				_, err := fmt.Fprintln(out, "no keyring-backed sources")
				return err
			}
			trows := make([][]tableCell, 0, len(rows))
			for _, r := range rows {
				trows = append(trows, []tableCell{cell(r.Handle), keyringStatusCell(r.Status)})
			}
			return renderTable(out, trows)
		},
	}
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.MarkFlagsMutuallyExclusive("json", "yaml")
	return c
}

// keyringStatusCell colors a keyring entry's status: green when present, red when
// the source is marked keyring-backed but its secret is missing.
func keyringStatusCell(status string) tableCell {
	if status == "missing" {
		return coloredCell(status, pal.remove)
	}
	return coloredCell(status, pal.add)
}

// newConfigKeyringGetCmd builds `iq config keyring get <handle>`: print a
// keyring-backed source's secret, redacted unless --reveal is set.
func newConfigKeyringGetCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:               "get <handle>",
		ValidArgsFunction: firstArgOnly(completeSourceHandles),
		Short:             "Print a keyring-backed source's secret (redacted unless --reveal)",
		Example:           "  $ iq config keyring get shop --reveal",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			src, full, ok := cf.Resolve(args[0])
			if !ok {
				return fmt.Errorf("unknown source %q; run `iq ls`", args[0])
			}
			if !src.Keyring {
				return fmt.Errorf("source %q is not keyring-backed", full)
			}
			pw, err := keyringStore.Get(iqconfig.CleanHandle(full))
			if err != nil {
				return err
			}
			if !cfg.reveal {
				pw = "xxxxx"
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), pw)
			return err
		},
	}
	c.Flags().BoolVar(&cfg.reveal, "reveal", false, "print the secret verbatim instead of redacting it")
	return c
}

// newConfigKeyringSetCmd builds `iq config keyring set <handle> [value]`: write
// or update a source's keyring secret. The value is taken from the positional or,
// when omitted, read from stdin/prompt like `iq add -p`. A source that carries an
// inline password is left untouched (use `migrate`); a password-less source is
// marked keyring-backed so the new secret takes effect.
func newConfigKeyringSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "set <handle> [value]",
		ValidArgsFunction: firstArgOnly(completeSourceHandles),
		Short:             "Write or update a source's keyring secret",
		Example:           "  $ iq config keyring set shop",
		Args:              cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			src, full, ok := cf.Resolve(args[0])
			if !ok {
				return fmt.Errorf("unknown source %q; run `iq ls`", args[0])
			}
			if !src.Keyring {
				_, _, hasPw, err := splitPassword(src.URL)
				if err != nil {
					return err
				}
				if hasPw {
					return fmt.Errorf("source %q has an inline password; run `iq config keyring migrate %s` to move it to the keyring", full, full)
				}
			}
			value := ""
			if len(args) == 2 {
				value = args[1]
			} else {
				value, err = readPassword(cmd)
				if err != nil {
					return err
				}
			}
			clean := iqconfig.CleanHandle(full)
			if err := keyringStore.Set(clean, value); err != nil {
				return err
			}
			// Flip a password-less source to keyring-backed and persist. On a Save
			// failure the just-written entry is new, so deleting it fully rolls back.
			if !src.Keyring {
				if err := cf.UseKeyring(full); err != nil {
					_ = keyringStore.Delete(clean)
					return err
				}
				if err := cf.Save(); err != nil {
					_ = keyringStore.Delete(clean)
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "set keyring secret for %s\n", full)
			return err
		},
	}
}

// newConfigKeyringRmCmd builds `iq config keyring rm <handle>`: delete a source's
// keyring secret and mark the source inline again. The config is updated first so
// the source is never left pointing at a secret that no longer exists.
func newConfigKeyringRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "rm <handle>",
		ValidArgsFunction: firstArgOnly(completeSourceHandles),
		Short:             "Delete a source's keyring secret (leaving the source without a credential)",
		Example:           "  $ iq config keyring rm shop",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			src, full, ok := cf.Resolve(args[0])
			if !ok {
				return fmt.Errorf("unknown source %q; run `iq ls`", args[0])
			}
			if !src.Keyring {
				return fmt.Errorf("source %q is not keyring-backed", full)
			}
			// Clear the flag and persist before deleting the secret: at worst this
			// leaves an orphaned entry (which `prune` removes), never a source that
			// claims a keyring secret it no longer has.
			if err := cf.ClearKeyring(full); err != nil {
				return err
			}
			if err := cf.Save(); err != nil {
				return err
			}
			_ = keyringStore.Delete(iqconfig.CleanHandle(full))
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "deleted keyring secret for %s; its stored URI now has no password\n", full)
			return err
		},
	}
}

// newConfigKeyringMigrateCmd builds `iq config keyring migrate [handle]`: move a
// source's inline password into the keyring, rewriting its stored URL to the
// password-less form. --all migrates every inline source that has a password;
// --dry-run reports what it would do without writing.
func newConfigKeyringMigrateCmd() *cobra.Command {
	var all, dryRun bool
	c := &cobra.Command{
		Use:               "migrate [handle]",
		ValidArgsFunction: firstArgOnly(completeSourceHandles),
		Short:             "Move an inline password into the keyring (--all for every inline source)",
		Example: "  $ iq config keyring migrate shop # one source\n" +
			"  $ iq config keyring migrate --all # every inline source",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return errors.New("give a source handle or --all, not both or neither")
			}
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			targets, err := migrateTargets(cf, args, all)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			done := make([]keyringStaged, 0, len(targets))
			for _, full := range targets {
				src := cf.Sources[full]
				stripped, pw, ok, err := splitPassword(src.URL)
				if err != nil {
					return err
				}
				if !ok {
					// Only reachable for an explicit handle; --all filters these out.
					return fmt.Errorf("source %q has no inline password to migrate", full)
				}
				if dryRun {
					if _, err := fmt.Fprintf(out, "would migrate %s\n", full); err != nil {
						return err
					}
					continue
				}
				clean := iqconfig.CleanHandle(full)
				if err := keyringStore.Set(clean, pw); err != nil {
					migrateRollback(done)
					return err
				}
				if err := cf.UseKeyring(full); err != nil {
					migrateRollback(append(done, keyringStaged{full, clean}))
					return err
				}
				if err := cf.SetSourceURL(full, stripped); err != nil {
					migrateRollback(append(done, keyringStaged{full, clean}))
					return err
				}
				done = append(done, keyringStaged{full, clean})
			}
			if dryRun {
				return nil
			}
			if len(done) == 0 {
				_, err := fmt.Fprintln(out, "nothing to migrate")
				return err
			}
			if err := cf.Save(); err != nil {
				migrateRollback(done)
				return err
			}
			for _, d := range done {
				if _, err := fmt.Fprintf(out, "migrated %s to the keyring\n", d.full); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "migrate every inline source that has a password")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be migrated without writing anything")
	return c
}

// migrateTargets resolves the source handles a migrate run should act on: the one
// explicit handle (which must exist and be inline), or, with --all, every inline
// source that carries a password. It errors on an unknown or already-keyring
// handle.
func migrateTargets(cf *iqconfig.Config, args []string, all bool) ([]string, error) {
	if all {
		targets := make([]string, 0)
		for _, h := range cf.List() {
			if h.Source.Keyring {
				continue
			}
			if _, _, ok, err := splitPassword(h.Source.URL); err != nil {
				return nil, err
			} else if ok {
				targets = append(targets, h.Name)
			}
		}
		return targets, nil
	}
	src, full, ok := cf.Resolve(args[0])
	if !ok {
		return nil, fmt.Errorf("unknown source %q; run `iq ls`", args[0])
	}
	if src.Keyring {
		return nil, fmt.Errorf("source %q is already keyring-backed", full)
	}
	return []string{full}, nil
}

// migrateRollback deletes the keyring entries a partial migrate wrote, so a
// failure before Save leaves no orphaned secrets.
func migrateRollback(done []keyringStaged) {
	for _, d := range done {
		_ = keyringStore.Delete(d.clean)
	}
}

// newConfigKeyringPruneCmd builds `iq config keyring prune`: delete keyring
// entries that no live source uses. Because the keyring cannot be enumerated,
// "stale" is only detectable for sources still in the config that are not
// keyring-backed yet still have an entry (left by a crash, a failed rm, or a
// migrate); entries for deleted sources cannot be found.
func newConfigKeyringPruneCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:     "prune",
		Short:   "Delete stale keyring entries for non-keyring sources",
		Example: "  $ iq config keyring prune",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			pruned := false
			for _, h := range cf.List() {
				if h.Source.Keyring {
					continue
				}
				clean := iqconfig.CleanHandle(h.Name)
				if _, err := keyringStore.Get(clean); err != nil {
					if errors.Is(err, secret.ErrNotFound) {
						continue
					}
					return err
				}
				verb := "deleted"
				if dryRun {
					verb = "would delete"
				} else if err := keyringStore.Delete(clean); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out, "%s stale keyring entry for %s\n", verb, h.Name); err != nil {
					return err
				}
				pruned = true
			}
			if !pruned {
				if _, err := fmt.Fprintln(out, "no stale keyring entries"); err != nil {
					return err
				}
			}
			// The keyring has no enumeration API, so entries whose source was deleted
			// are undetectable; say so rather than imply the store is fully clean.
			_, err = fmt.Fprintln(cmd.ErrOrStderr(), "note: keyring entries for deleted sources cannot be detected and are not pruned")
			return err
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be pruned without deleting anything")
	return c
}
