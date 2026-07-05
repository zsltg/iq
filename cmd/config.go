package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	iqconfig "github.com/zsltg/iq/internal/config"
	"github.com/zsltg/iq/internal/numfmt"
)

// persistableOptions is the allowlist of flag names that may be stored as config
// defaults, keyed by the flag's own name so an option is a 1:1 alias for its
// flag (same name, type, help, and validation — one source of truth). Excluded
// on purpose: per-invocation targeting (src, collection, from, combine,
// dry-run), display-only listing toggles (reveal, expand), diagnostics that only
// make sense live (debug.pprof), the config path itself, the -j/-J/-A/-y/-r
// aliases (the canonical selector is format), and the query safety gates
// (unbounded, no-compile) whose intent is one run, not a stored default.
var persistableOptions = []string{
	"format", "format.decimal", "compact",
	"timeout",
	"monochrome", "color", "no-progress",
	"verbose", "log", "log.file", "log.level", "log.format",
	"error.format", "error.stack", "error.format.text.verbose",
}

// isPersistableOption reports whether key names a storable option.
func isPersistableOption(key string) bool {
	for _, k := range persistableOptions {
		if k == key {
			return true
		}
	}
	return false
}

// applyStoredOptions merges stored option defaults into cmd's flags before the
// rest of PersistentPreRunE validates and consumes them. Precedence per key:
// an explicit flag wins; else the selected source's option; else the base
// option; else the flag's built-in default (left untouched). The source is
// resolved read-only — no keyring, no connection — so per-source defaults reach
// even the logger/color setup that runs later in PreRun. Values were validated
// at `config set` time; a hand-edited bad value is caught here and fails fast.
func applyStoredOptions(cmd *cobra.Command, cfg *config) error {
	cf, err := iqconfig.Load()
	if err != nil {
		return err
	}
	selected := cfg.src
	if selected == "" {
		selected = cf.Active
	}
	var srcOpts map[string]string
	if selected != "" {
		if s, _, ok := cf.Resolve(selected); ok {
			srcOpts = s.Options
		}
	}
	for _, key := range persistableOptions {
		f := cmd.Flag(key)
		if f == nil || f.Changed {
			continue
		}
		val, ok := srcOpts[key]
		if !ok {
			val, ok = cf.Options[key]
		}
		if !ok {
			continue
		}
		if err := f.Value.Set(val); err != nil {
			return fmt.Errorf("stored option %q: %w", key, err)
		}
	}
	return nil
}

// newConfigCmd builds `iq config`: inspect and edit the config file, and get,
// set, or unset the stored option defaults. With --src an option is scoped to
// one source, overriding the base for a query against it; without --src it is a
// base default. The precedence at query time is explicit flag > source option >
// base option > built-in default.
func newConfigCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Show the config file and manage stored option defaults",
		Long: "Inspect the config file and manage stored option defaults. `set`/`unset` a\n" +
			"persistable flag by name to persist its value so you need not retype it; scope\n" +
			"it to one source with --src (`iq config set --src @prod format yaml`). A stored\n" +
			"default is overridden per query by an explicit flag, and a source option is\n" +
			"overridden only by an explicit flag; the base option applies to every other\n" +
			"source. `location` prints the file path, `edit` opens it, `view` dumps it.",
		Args: cobra.NoArgs,
	}
	c.AddCommand(
		newConfigLocationCmd(),
		newConfigGetCmd(cfg),
		newConfigSetCmd(cfg),
		newConfigUnsetCmd(cfg),
		newConfigLsCmd(cfg),
		newConfigEditCmd(),
		newConfigViewCmd(cfg),
	)
	return c
}

// newConfigLocationCmd builds `iq config location`: print the resolved config
// file path (honoring --config and $IQ_CONFIG).
func newConfigLocationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "location",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := iqconfig.Path()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
			return err
		},
	}
}

// newConfigGetCmd builds `iq config get [--src @x] <key>`: print the effective
// value of one option at that scope — the stored value if set, else the flag's
// built-in default (for a source, the source value falls back to the base value
// then the default).
func newConfigGetCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "get <option>",
		Short: "Print an option's effective value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if !isPersistableOption(key) {
				return unknownOptionErr(key)
			}
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			handle := iqconfig.CleanHandle(cfg.src)
			if handle != "" {
				if _, _, ok := cf.Resolve(handle); !ok {
					return fmt.Errorf("unknown source %q; run `iq ls`", handle)
				}
			}
			val, _, err := effectiveOption(cf, handle, key)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), val)
			return err
		},
	}
}

// newConfigSetCmd builds `iq config set [--src @x] <key> <value>`: validate and
// store an option's value, base or per-source.
func newConfigSetCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "set <option> <value>",
		Short: "Store an option's value (base, or per-source with --src)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			canonical, err := canonicalOptionValue(key, value)
			if err != nil {
				return err
			}
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			handle := iqconfig.CleanHandle(cfg.src)
			if err := cf.SetOption(handle, key, canonical); err != nil {
				return err
			}
			if err := cf.Save(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "set %s%s = %s\n", scopePrefix(handle), key, canonical)
			return err
		},
	}
}

// newConfigUnsetCmd builds `iq config unset [--src @x] <key>`: remove a stored
// option, base or per-source.
func newConfigUnsetCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <option>",
		Short: "Remove a stored option (base, or per-source with --src)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if !isPersistableOption(key) {
				return unknownOptionErr(key)
			}
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			handle := iqconfig.CleanHandle(cfg.src)
			existed, err := cf.UnsetOption(handle, key)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !existed {
				_, err = fmt.Fprintf(out, "%s%s was not set\n", scopePrefix(handle), key)
				return err
			}
			if err := cf.Save(); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "unset %s%s\n", scopePrefix(handle), key)
			return err
		},
	}
}

// newConfigLsCmd builds `iq config ls [--src @x]`: list the options set at that
// scope with their values. -v (the global --verbose) lists every persistable
// option with its effective value, built-in default, and help text.
func newConfigLsCmd(cfg *config) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List stored options (all with -v), base or per-source",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			handle := iqconfig.CleanHandle(cfg.src)
			if handle != "" {
				if _, _, ok := cf.Resolve(handle); !ok {
					return fmt.Errorf("unknown source %q; run `iq ls`", handle)
				}
			}
			out := cmd.OutOrStdout()
			if cfg.verbose {
				return listAllOptions(out, cf, handle)
			}
			opts, err := cf.OptionList(handle)
			if err != nil {
				return err
			}
			for _, o := range opts {
				if _, err := fmt.Fprintf(out, "%s = %s\n", o.Key, o.Value); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// listAllOptions prints every persistable option with its effective value at the
// scope, its built-in default, and the flag's usage text.
func listAllOptions(out io.Writer, cf *iqconfig.Config, handle string) error {
	probe, _ := newRootCmd()
	for _, key := range persistableOptions {
		val, set, err := effectiveOption(cf, handle, key)
		if err != nil {
			return err
		}
		f := probe.Flag(key)
		marker := " "
		if set {
			marker = "*"
		}
		if _, err := fmt.Fprintf(out, "%s %s = %s (default %s)\n    %s\n", marker, key, val, f.DefValue, f.Usage); err != nil {
			return err
		}
	}
	return nil
}

// newConfigEditCmd builds `iq config edit`: open the config file in the user's
// editor. The file is created empty first if missing so the editor has a target.
func newConfigEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in your editor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := iqconfig.Path()
			if err != nil {
				return err
			}
			// Materialize an empty config through Save so the parent dir and 0600
			// perms match a normally-written file, then hand the path to the editor.
			if _, err := os.Stat(p); os.IsNotExist(err) {
				cf, lerr := iqconfig.Load()
				if lerr != nil {
					return lerr
				}
				if serr := cf.Save(); serr != nil {
					return serr
				}
			}
			editor, args := resolveEditor()
			args = append(args, p)
			ed := exec.CommandContext(cmd.Context(), editor, args...)
			ed.Stdin, ed.Stdout, ed.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := ed.Run(); err != nil {
				return fmt.Errorf("run editor %q: %w", editor, err)
			}
			return nil
		},
	}
}

// newConfigViewCmd builds `iq config view`: dump the whole config as TOML with
// source URLs redacted (--reveal/--expand unredact, as for `iq ls`).
func newConfigViewCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use:   "view",
		Short: "Print the whole config as TOML, URLs redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cf, err := iqconfig.Load()
			if err != nil {
				return err
			}
			data, err := toml.Marshal(redactedConfig(cf, cfg.reveal, cfg.expand))
			if err != nil {
				return fmt.Errorf("encode config: %w", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	c.Flags().BoolVar(&cfg.reveal, "reveal", false, "print inline-stored passwords verbatim instead of redacting them")
	c.Flags().BoolVar(&cfg.expand, "expand", false, "resolve keyring-backed passwords and inline them into printed URLs")
	return c
}

// redactedConfig returns a copy of cf with every source URL replaced by its
// display location, so `view` never prints a stored credential unless --reveal
// or --expand asks for it. Option maps are shared by reference (they hold no
// secrets), which is safe because the copy is only marshaled, never mutated.
func redactedConfig(cf *iqconfig.Config, reveal, expand bool) *iqconfig.Config {
	out := &iqconfig.Config{Active: cf.Active, Group: cf.Group, Options: cf.Options}
	if len(cf.Sources) > 0 {
		out.Sources = make(map[string]iqconfig.Source, len(cf.Sources))
		for h, s := range cf.Sources {
			s.URL = displayLocation(s, h, reveal, expand)
			out.Sources[h] = s
		}
	}
	return out
}

// effectiveOption returns the value that would apply for key at the scope and
// whether it was explicitly set at that scope. For a source: its own value if
// set, else the base value, else the flag default. For the base: its value if
// set, else the flag default.
func effectiveOption(cf *iqconfig.Config, handle, key string) (value string, set bool, err error) {
	if v, ok := cf.GetOption(handle, key); ok {
		return v, true, nil
	}
	if handle != "" {
		if v, ok := cf.GetOption("", key); ok {
			return v, false, nil
		}
	}
	probe, _ := newRootCmd()
	f := probe.Flag(key)
	if f == nil {
		return "", false, unknownOptionErr(key)
	}
	return f.DefValue, false, nil
}

// canonicalOptionValue validates value for key and returns its canonical stored
// form. It runs the value through the real flag (catching bad booleans and
// durations and normalizing) and then the same semantic validators PreRunE uses
// for the plain-string flags, so a stored value can never be one a live run
// would reject.
func canonicalOptionValue(key, value string) (string, error) {
	if !isPersistableOption(key) {
		return "", unknownOptionErr(key)
	}
	probe, _ := newRootCmd()
	f := probe.Flag(key)
	if f == nil {
		return "", unknownOptionErr(key)
	}
	if err := f.Value.Set(value); err != nil {
		return "", fmt.Errorf("invalid value for %q: %w", key, err)
	}
	canonical := f.Value.String()
	if err := validateOptionSemantics(key, canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

// validateOptionSemantics applies the extra validation that PreRunE performs for
// the plain-string flags (which accept any string at parse time). Keys not
// listed here are fully validated by the flag's own type.
func validateOptionSemantics(key, value string) error {
	switch key {
	case "format":
		return validateFormat(value)
	case "format.decimal":
		_, err := numfmt.ParseDecimalMode(value)
		return err
	case "error.format":
		return validateErrorFormat(value)
	case "log.level":
		_, err := parseLogLevel(value)
		return err
	case "log.format":
		v := strings.ToLower(strings.TrimSpace(value))
		if v != "text" && v != "json" {
			return fmt.Errorf("invalid log.format %q: want text or json", value)
		}
		return nil
	default:
		return nil
	}
}

// resolveEditor picks the editor command and its leading arguments, honoring
// IQ_EDITOR, then VISUAL, then EDITOR (each may carry arguments, e.g. "code
// --wait"), falling back to vi.
func resolveEditor() (string, []string) {
	for _, env := range []string{"IQ_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			fields := strings.Fields(v)
			return fields[0], fields[1:]
		}
	}
	return "vi", nil
}

// scopePrefix renders the leading "<handle> " label for a per-source message, or
// "" for the base scope.
func scopePrefix(handle string) string {
	if handle == "" {
		return ""
	}
	return handle + " "
}

// unknownOptionErr reports a key that is not a persistable option, listing the
// valid keys.
func unknownOptionErr(key string) error {
	return fmt.Errorf("unknown option %q; valid options: %s", key, strings.Join(persistableOptions, ", "))
}
