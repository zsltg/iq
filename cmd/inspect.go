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
// runs. With no positional argument it runs them all; positionals narrow to the
// named ones.
var mongoInspectCmds = []string{"dbStats", "serverStatus", "listCollections", "collStats", "buildInfo", "hostInfo"}

// newInspectCmd builds `iq inspect [section...]`: show a source's native
// server/database introspection. The source is the active one or --src; the
// positional arguments narrow the output. Bounded by --timeout.
func newInspectCmd(cfg *config) *cobra.Command {
	var (
		jsonOut bool
		list    bool
	)
	long := "Show a source's native server/database introspection.\n\n" +
		"MongoDB — runs diagnostic database commands; no arguments runs them all,\n" +
		"positional arguments narrow to the named ones:\n" +
		"  " + strings.Join(mongoInspectCmds, "  ") + "\n" +
		"  (collStats needs a collection via -c or on the source)\n\n" +
		"Redis — runs INFO; positional arguments narrow it to those sections\n" +
		"(`iq inspect memory server`), and none runs the full INFO. Common sections:\n" +
		"  server  clients  memory  persistence  stats  replication  cpu  keyspace\n\n" +
		"Select the source with --src or the active source. Use --json for machine-readable\n" +
		"output, or --list to print the subcommands/sections available for the active source."
	c := &cobra.Command{
		Use:   "inspect [section...]",
		Short: "Show a source's native server/database introspection",
		Long:  long,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSource(cmd, cfg); err != nil {
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
			if strings.HasPrefix(schemeOf(cfg.url), "redis") {
				return inspectRedis(ctx, out, st, cfg, args, jsonOut, list)
			}
			return inspectMongo(ctx, out, st, cfg, args, jsonOut, list)
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	c.Flags().BoolVar(&list, "list", false, "list the subcommands/sections available for the active source")
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
		if _, err := fmt.Fprintf(out, "# %s\n%s\n\n", r.sub, st.FormatRaw(r.value)); err != nil {
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

// inspectHeader writes a one-line source header: driver and redacted location.
func inspectHeader(out io.Writer, cfg *config) error {
	_, err := fmt.Fprintf(out, "%s  %s\n\n", schemeOf(cfg.url), redactURL(cfg.url))
	return err
}
