package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
)

// mongoInspectCmds is the supported set of MongoDB diagnostic commands `inspect`
// runs. With no --only, it runs them all; --only narrows to the named ones.
var mongoInspectCmds = []string{"dbStats", "serverStatus", "listCollections", "collStats", "buildInfo", "hostInfo"}

// newInspectCmd builds `iq inspect [source]`: show a source's native
// server/database introspection. The positional names the source (sq-style
// `<source>.<collection>` addressing); with none it uses --src or the active
// source. --only narrows the output. Bounded by --timeout.
func newInspectCmd(cfg *config) *cobra.Command {
	var (
		jsonOut bool
		list    bool
		only    []string
	)
	long := "Show a source's native server/database introspection.\n\n" +
		"The positional argument names the source, like `iq inspect prod`; with none it\n" +
		"uses --src or the active source. MongoDB sources accept sq-style\n" +
		"`<source>.<collection>` addressing (`iq inspect prod.books`) to pick the\n" +
		"collection; --collection still overrides it, and Redis sources take no collection.\n\n" +
		"MongoDB — runs diagnostic database commands; no --only runs them all,\n" +
		"--only narrows to the named ones:\n" +
		"  " + strings.Join(mongoInspectCmds, "  ") + "\n" +
		"  (collStats needs a collection via -c or on the source)\n\n" +
		"Redis — runs INFO; --only narrows it to those sections\n" +
		"(`iq inspect prod --only memory,server`), and none runs the full INFO. Common sections:\n" +
		"  server  clients  memory  persistence  stats  replication  cpu  keyspace\n\n" +
		"Use --json for machine-readable output, or --list to print the subcommands/sections\n" +
		"available for the source. The location header is redacted by default: --reveal prints\n" +
		"an inline password verbatim, --expand resolves a keyring-backed one."
	c := &cobra.Command{
		Use:   "inspect [source]",
		Short: "Show a source's native server/database introspection",
		Long:  long,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			if err := resolveInspectSource(cmd, cfg, arg); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()
			st, err := openStore(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			out := cmd.OutOrStdout()
			if driverName(cfg.url) == "redis" {
				return inspectRedis(ctx, out, st, cfg, only, jsonOut, list)
			}
			return inspectMongo(ctx, out, st, cfg, only, jsonOut, list)
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	c.Flags().BoolVar(&list, "list", false, "list the subcommands/sections available for the source")
	c.Flags().StringSliceVar(&only, "only", nil, "narrow to these sections (Redis) / subcommands (MongoDB)")
	c.Flags().BoolVar(&cfg.reveal, "reveal", false, "print an inline-stored password verbatim in the location header instead of redacting it")
	c.Flags().BoolVar(&cfg.expand, "expand", false, "resolve a keyring-backed password and inline it in the location header")
	return c
}

// inspectRedis runs INFO (narrowed to the given sections) and renders it. With
// list, it prints the section names the reply exposes instead of the reply.
func inspectRedis(ctx context.Context, out io.Writer, st store, cfg *config, sections []string, jsonOut, list bool) error {
	res, err := query.NewRunner(st).Run(ctx, append([]string{"INFO"}, sections...))
	if err != nil {
		return redactErr(err, cfg.url)
	}
	info, _ := res.(string)
	if list {
		return writeInspectList(out, redisInfoSections(info), jsonOut)
	}
	if jsonOut {
		return newJSONEncoder(out, true).Encode(parseRedisInfo(info))
	}
	if err := inspectHeader(out, cfg); err != nil {
		return err
	}
	_, err = io.WriteString(out, info)
	return err
}

// redisInfoSections returns the section names present in an INFO reply, sorted.
func redisInfoSections(info string) []string {
	parsed := parseRedisInfo(info)
	names := make([]string, 0, len(parsed))
	for name := range parsed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseRedisInfo turns an INFO reply into section → key → value. Lines like
// "# Server" open a section; "key:value" lines populate it.
func parseRedisInfo(info string) map[string]map[string]string {
	sections := make(map[string]map[string]string)
	section := "default"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if sections[section] == nil {
			sections[section] = make(map[string]string)
		}
		sections[section][k] = v
	}
	return sections
}

// inspectMongo runs the requested MongoDB diagnostic commands (all supported when
// none are named) and renders each reply keyed by subcommand. With list, it prints
// the supported subcommand names instead, without touching the store.
func inspectMongo(ctx context.Context, out io.Writer, st store, cfg *config, subs []string, jsonOut, list bool) error {
	if list {
		return writeInspectList(out, mongoInspectCmds, jsonOut)
	}
	explicit := len(subs) > 0
	which := subs
	if !explicit {
		which = mongoInspectCmds
	}
	for _, sub := range which {
		if !isMongoInspectCmd(sub) {
			return fmt.Errorf("unknown inspect subcommand %q; want one of %s", sub, strings.Join(mongoInspectCmds, ", "))
		}
	}

	type result struct {
		sub   string
		value any
	}
	results := make([]result, 0, len(which))
	for _, sub := range which {
		if sub == "collStats" && cfg.collection == "" {
			if explicit {
				return fmt.Errorf("collStats needs a collection; set one with -c or on the source")
			}
			continue // skip in the run-all case
		}
		res, err := query.NewRunner(st).Run(ctx, []string{mongoInspectDoc(sub, cfg.collection)})
		if err != nil {
			res = map[string]any{"error": redactErr(err, cfg.url).Error()}
		}
		results = append(results, result{sub: sub, value: res})
	}

	if jsonOut {
		byName := make(map[string]any, len(results))
		for _, r := range results {
			byName[r.sub] = r.value
		}
		return newJSONEncoder(out, true).Encode(byName)
	}
	if err := inspectHeader(out, cfg); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := fmt.Fprintf(out, "%s\n%s\n\n", pal.header.Sprint("# "+r.sub), st.FormatRaw(r.value, colorOn())); err != nil {
			return err
		}
	}
	return nil
}

// mongoInspectDoc builds the JSON command document for a MongoDB diagnostic
// subcommand. collStats takes the collection name; the rest take 1.
func mongoInspectDoc(sub, collection string) string {
	if sub == "collStats" {
		return fmt.Sprintf(`{%q:%q}`, sub, collection)
	}
	return fmt.Sprintf(`{%q:1}`, sub)
}

// isMongoInspectCmd reports whether sub is a supported diagnostic command.
func isMongoInspectCmd(sub string) bool {
	for _, c := range mongoInspectCmds {
		if c == sub {
			return true
		}
	}
	return false
}

// writeInspectList renders the names --list emits: a JSON array with jsonOut, else
// one name per line.
func writeInspectList(out io.Writer, names []string, jsonOut bool) error {
	if jsonOut {
		return newJSONEncoder(out, true).Encode(names)
	}
	for _, name := range names {
		if _, err := fmt.Fprintln(out, name); err != nil {
			return err
		}
	}
	return nil
}

// inspectHeader writes a one-line source header: driver and location. The
// location is redacted unless --reveal (inline password) or --expand (keyring
// password) un-redacts it, rendered from the stored source like `iq ls`.
func inspectHeader(out io.Writer, cfg *config) error {
	loc := displayLocation(cfg.source, cfg.handle, cfg.reveal, cfg.expand)
	header := fmt.Sprintf("%s  %s", driverName(cfg.source.URL), loc)
	_, err := fmt.Fprintf(out, "%s\n\n", pal.header.Sprint(header))
	return err
}
