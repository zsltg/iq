package cmd

import (
	"errors"
	"fmt"
	"io"

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
			rows, err := keyringRows(cf)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut || yamlOut {
				return writeStructured(out, rows, yamlOut)
			}
			return writeKeyringRows(out, rows)
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

// keyringRows lists the keyring-backed sources and the state of each secret.
func keyringRows(cf *iqconfig.Config) ([]keyringRow, error) {
	rows := make([]keyringRow, 0)
	for _, h := range cf.List() {
		if !h.Source.Keyring {
			continue
		}
		has, err := keyringHas(iqconfig.CleanHandle(h.Name))
		if err != nil {
			return nil, err
		}
		status := "present"
		if !has {
			status = "missing"
		}
		rows = append(rows, keyringRow{Handle: h.Name, Status: status})
	}
	return rows, nil
}

// writeKeyringRows prints the rows as a table, or the none line when there are
// no rows.
func writeKeyringRows(out io.Writer, rows []keyringRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no keyring-backed sources")
		return err
	}
	trows := make([][]tableCell, 0, len(rows))
	for _, r := range rows {
		trows = append(trows, []tableCell{cell(r.Handle), keyringStatusCell(r.Status)})
	}
	return renderTable(out, trows)
}

// keyringHas reports whether the keyring holds a secret for the clean handle.
// A missing secret is not an error.
func keyringHas(clean string) (bool, error) {
	if _, err := keyringStore.Get(clean); err != nil {
		if errors.Is(err, secret.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// resolveHandle finds the source that handle names. It fails with the "unknown
// source" message when the config has no such source.
func resolveHandle(cf *iqconfig.Config, handle string) (iqconfig.Source, string, error) {
	src, full, ok := cf.Resolve(handle)
	if !ok {
		return iqconfig.Source{}, "", fmt.Errorf("unknown source %q; run `iq ls`", handle)
	}
	return src, full, nil
}

// requireKeyring makes sure that the source is keyring-backed.
func requireKeyring(src iqconfig.Source, full string) error {
	if !src.Keyring {
		return fmt.Errorf("source %q is not keyring-backed", full)
	}
	return nil
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
			src, full, err := resolveHandle(cf, args[0])
			if err != nil {
				return err
			}
			if err := requireKeyring(src, full); err != nil {
				return err
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
		RunE:              runKeyringSet,
	}
}

// runKeyringSet writes the secret of one source.
func runKeyringSet(cmd *cobra.Command, args []string) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	src, full, err := resolveHandle(cf, args[0])
	if err != nil {
		return err
	}
	if err := refuseInlinePassword(src, full); err != nil {
		return err
	}
	value, err := secretValue(cmd, args)
	if err != nil {
		return err
	}
	clean := iqconfig.CleanHandle(full)
	if err := keyringStore.Set(clean, value); err != nil {
		return err
	}
	if err := adoptKeyring(cf, src, full); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "set keyring secret for %s\n", full)
	return err
}

// refuseInlinePassword fails when the source is not keyring-backed and its URL
// holds a password. The user must run migrate for such a source.
func refuseInlinePassword(src iqconfig.Source, full string) error {
	if src.Keyring {
		return nil
	}
	_, _, hasPw, err := splitPassword(src.URL)
	if err != nil {
		return err
	}
	if hasPw {
		return fmt.Errorf("source %q has an inline password; run `iq config keyring migrate %s` to move it to the keyring", full, full)
	}
	return nil
}

// secretValue returns the value from the second argument, or reads it from stdin
// or the prompt when there is none.
func secretValue(cmd *cobra.Command, args []string) (string, error) {
	if len(args) == 2 {
		return args[1], nil
	}
	return readPassword(cmd)
}

// adoptKeyring flips a password-less source to keyring-backed and persists the
// config. On a failure the just-written entry is new, so deleting it fully rolls
// back. A source that is already keyring-backed needs no change.
func adoptKeyring(cf *iqconfig.Config, src iqconfig.Source, full string) error {
	if src.Keyring {
		return nil
	}
	clean := iqconfig.CleanHandle(full)
	if err := cf.UseKeyring(full); err != nil {
		_ = keyringStore.Delete(clean)
		return err
	}
	if err := cf.Save(); err != nil {
		_ = keyringStore.Delete(clean)
		return err
	}
	return nil
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
		RunE:              runKeyringRm,
	}
}

// runKeyringRm deletes the secret of one source and marks the source inline.
func runKeyringRm(cmd *cobra.Command, args []string) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	src, full, err := resolveHandle(cf, args[0])
	if err != nil {
		return err
	}
	if err := requireKeyring(src, full); err != nil {
		return err
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
}

// newConfigKeyringMigrateCmd builds `iq config keyring migrate [handle]`: move a
// source's inline password into the keyring, rewriting its stored URL to the
// password-less form. --all migrates every inline source that has a password;
// --dry-run reports what it would do without writing.
func newConfigKeyringMigrateCmd() *cobra.Command {
	var o migrateOptions
	c := &cobra.Command{
		Use:               "migrate [handle]",
		ValidArgsFunction: firstArgOnly(completeSourceHandles),
		Short:             "Move an inline password into the keyring (--all for every inline source)",
		Example: "  $ iq config keyring migrate shop # one source\n" +
			"  $ iq config keyring migrate --all # every inline source",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKeyringMigrate(cmd, args, o)
		},
	}
	c.Flags().BoolVar(&o.all, "all", false, "migrate every inline source that has a password")
	c.Flags().BoolVar(&o.dryRun, "dry-run", false, "report what would be migrated without writing anything")
	return c
}

// runKeyringMigrate moves inline passwords into the keyring.
func runKeyringMigrate(cmd *cobra.Command, args []string, o migrateOptions) error {
	if o.all == (len(args) == 1) {
		return errors.New("give a source handle or --all, not both or neither")
	}
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	items, err := migrationItems(cf, args, o.all)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if o.dryRun {
		for _, it := range items {
			if _, err := fmt.Fprintf(out, "would migrate %s\n", it.full); err != nil {
				return err
			}
		}
		return nil
	}
	return applyMigration(out, cf, items)
}

// migrationItems resolves the targets of a migrate run and makes sure that each
// one can move. It checks every source before the first keyring write, so that a
// source that cannot move stops the run with nothing written.
func migrationItems(cf *iqconfig.Config, args []string, all bool) ([]migrateItem, error) {
	targets, err := migrateTargets(cf, args, all)
	if err != nil {
		return nil, err
	}
	items := make([]migrateItem, 0, len(targets))
	for _, full := range targets {
		it, err := migrateSecret(cf.Sources[full], full)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

// applyMigration writes the secrets, saves the config, and prints one line for
// each migrated source. It prints "nothing to migrate" and saves nothing when
// there are no items.
func applyMigration(out io.Writer, cf *iqconfig.Config, items []migrateItem) error {
	done, err := stageMigration(cf, items)
	if err != nil {
		return err
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
}

// stageMigration writes each secret to the keyring and updates the config in
// memory. On a failure it deletes every secret that it wrote, including the one
// of the failing item when that one was written.
func stageMigration(cf *iqconfig.Config, items []migrateItem) ([]keyringStaged, error) {
	done := make([]keyringStaged, 0, len(items))
	for _, it := range items {
		if err := keyringStore.Set(it.clean, it.password); err != nil {
			migrateRollback(done)
			return nil, err
		}
		if err := cf.UseKeyring(it.full); err != nil {
			migrateRollback(append(done, it.keyringStaged))
			return nil, err
		}
		if err := cf.SetSourceURL(it.full, it.stripped); err != nil {
			migrateRollback(append(done, it.keyringStaged))
			return nil, err
		}
		done = append(done, it.keyringStaged)
	}
	return done, nil
}

// migrateOptions holds the flag values of `iq config keyring migrate`.
type migrateOptions struct{ all, dryRun bool }

// migrateItem is one source that a migration moves to the keyring: its handle,
// its stored URL without the password, and the password.
type migrateItem struct {
	keyringStaged
	stripped, password string
}

// migrateSecret returns the migrateItem of the source full when the source can
// move to the keyring. It fails when the URL has no password (only for an
// explicit handle, --all filters these out) or when the keyring already holds a
// password for the handle. That entry can belong to a source in another config
// file, so a migration does not replace it and tells the user to rename the
// source.
func migrateSecret(src iqconfig.Source, full string) (migrateItem, error) {
	stripped, password, ok, err := splitPassword(src.URL)
	if err != nil {
		return migrateItem{}, err
	}
	if !ok {
		return migrateItem{}, fmt.Errorf("source %q has no inline password to migrate", full)
	}
	clean := iqconfig.CleanHandle(full)
	err = keyringFree(clean)
	if errors.Is(err, errKeyringTaken) {
		return migrateItem{}, fmt.Errorf("%w; rename the source with iq mv, then migrate it", err)
	}
	if err != nil {
		return migrateItem{}, err
	}
	return migrateItem{keyringStaged{full, clean}, stripped, password}, nil
}

// migrateTargets resolves the source handles a migrate run should act on: the one
// explicit handle (which must exist and be inline), or, with --all, every inline
// source that carries a password. It errors on an unknown or already-keyring
// handle.
func migrateTargets(cf *iqconfig.Config, args []string, all bool) ([]string, error) {
	if all {
		return inlinePasswordSources(cf)
	}
	src, full, err := resolveHandle(cf, args[0])
	if err != nil {
		return nil, err
	}
	if src.Keyring {
		return nil, fmt.Errorf("source %q is already keyring-backed", full)
	}
	return []string{full}, nil
}

// inlinePasswordSources lists the handles of the sources that keep a password in
// their URL and are not keyring-backed.
func inlinePasswordSources(cf *iqconfig.Config) ([]string, error) {
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
			return runKeyringPrune(cmd, dryRun)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be pruned without deleting anything")
	return c
}

// runKeyringPrune deletes the stale keyring entries, or only reports them in a
// dry run.
func runKeyringPrune(cmd *cobra.Command, dryRun bool) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	pruned, err := pruneStale(out, cf, dryRun)
	if err != nil {
		return err
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
}

// pruneStale visits every non-keyring source and prunes its entry. It reports
// whether it found any entry.
func pruneStale(out io.Writer, cf *iqconfig.Config, dryRun bool) (bool, error) {
	pruned := false
	for _, h := range cf.List() {
		if h.Source.Keyring {
			continue
		}
		found, err := pruneEntry(out, h.Name, dryRun)
		if err != nil {
			return false, err
		}
		pruned = pruned || found
	}
	return pruned, nil
}

// pruneEntry deletes the keyring entry of one source, or only reports it in a
// dry run. It reports whether the source had an entry.
func pruneEntry(out io.Writer, name string, dryRun bool) (bool, error) {
	clean := iqconfig.CleanHandle(name)
	has, err := keyringHas(clean)
	if err != nil || !has {
		return false, err
	}
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	} else if err := keyringStore.Delete(clean); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(out, "%s stale keyring entry for %s\n", verb, name); err != nil {
		return false, err
	}
	return true, nil
}
