package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zsltg/iq/internal/query"
)

// newExecCmd builds the `iq exec` subcommand: an escape hatch that forwards a
// command verbatim to the database and prints the reply. It exists for the
// writes, administration, and server-side queries the jq read path does not
// cover. What it accepts depends on the backend: for Redis a command and its
// operands (`iq exec HGETALL book:2`); for MongoDB a single JSON command document
// run with runCommand (`iq exec '{"find":"books","filter":{...}}'`).
func newExecCmd(cfg *config) *cobra.Command {
	c := &cobra.Command{
		Use: "exec <command> [args...]",
		// The positionals are a backend verb and its operands, never a file, so
		// suppress the shell's filename fallback. What each backend accepts is
		// driver-defined and not enumerable offline, so nothing is offered.
		ValidArgsFunction: cobra.NoFileCompletions,
		Short:             "Forward a command to the database verbatim and print the reply",
		Long: "Forward a command to the backend verbatim, in the backend's own language.\n" +
			"Redis: a command and operands (`iq --src cache exec SET greeting hello`).\n" +
			"MongoDB: one JSON command document run with runCommand. Cassandra: a CQL\n" +
			"statement. DynamoDB: a PartiQL statement. CouchDB: a Mango _find request or a\n" +
			"bare selector. Couchbase: a SQL++ statement, optionally followed by a JSON\n" +
			"object of named parameters. Neo4j: a Cypher statement, optionally followed by\n" +
			"a JSON object of parameters. Elasticsearch and OpenSearch: a _search body or\n" +
			"a bare query object. HBase has no query language, so it takes a shell-style\n" +
			"verb naming its own table: get, scan, count (reads), put, delete (writes).\n" +
			"\n" +
			"Every iq flag must come before `exec`: everything after it is forwarded to\n" +
			"the backend untouched. The documentation site's Drivers page details each\n" +
			"backend's shape.",
		Example: "  # Redis: a command and its operands.\n" +
			"  $ iq --src cache exec SET greeting hello\n" +
			"  $ iq --src cache exec HGETALL user:2\n" +
			"\n" +
			"  # MongoDB: one runCommand document (database-scoped; no collection needed).\n" +
			"  $ iq --src shop exec '{\"find\":\"orders\",\"filter\":{}}'\n" +
			"\n" +
			"  # Cassandra: a CQL statement (keyspace-scoped; no table needed).\n" +
			"  $ iq --src cluster exec 'SELECT release_version FROM system.local'\n" +
			"\n" +
			"  # Neo4j: parameterized Cypher (parameters ride as a JSON object).\n" +
			"  $ iq --src graph exec 'MATCH (n:Person) WHERE n.age > $min RETURN n.name' '{\"min\": 40}'\n" +
			"\n" +
			"  # HBase: a verb naming its own table (get/scan/count/put/delete).\n" +
			"  $ iq --src cluster exec get books 42\n" +
			"  $ iq --src cluster exec put books 42 cf:title Dune",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Log only the command verb: Redis operands (SET greeting hello) may
			// carry values, so they are never logged.
			cfg.log().Info("exec forward", "verb", args[0])
			if err := resolveSource(cfg); err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.timeout)
			defer cancel()

			store, err := openStore(ctx, cfg)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			result, err := query.NewRunner(store).Run(ctx, args)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), store.FormatRaw(result, colorOn())); err != nil {
				return err
			}
			return nil
		},
	}
	// Redis operands may start with a dash (negative indices such as `-1`), so
	// stop flag parsing at the first positional and forward the rest verbatim.
	c.Flags().SetInterspersed(false)
	return c
}
