package cmd

import (
	"context"
	"io"
	"strings"

	"github.com/spf13/cobra"

	iqfile "github.com/zsltg/iq/drivers/file"
	iqmongo "github.com/zsltg/iq/drivers/mongo"
	iqredis "github.com/zsltg/iq/drivers/redis"
	"github.com/zsltg/iq/internal/predicate"
	"github.com/zsltg/iq/internal/query"
	"github.com/zsltg/iq/internal/selector"
)

// driver describes one backend iq can talk to: a stable name, a human
// description, the URL schemes that select it, its upstream docs, and the opener
// that connects. The drivers registry below is the single source of truth —
// openStore, supportedScheme, driverForScheme, driverName, and `iq driver ls`
// all derive from it, so adding a backend is one entry, not edits scattered
// across a switch. It lives in cmd, the composition root, because open returns
// the cmd-local store port and takes the per-invocation config.
type driver struct {
	name    string
	desc    string
	schemes []string
	doc     string
	// versions is the range of backend server versions the bundled client
	// library supports, shown by `iq driver ls`.
	versions string
	// readOnly marks a local, connection-less backend (a dump file) that iq can
	// only read: it has no write-side describers, is never a copy destination, and
	// has no upstream server to suggest — so doc/versions may be empty for it and
	// it is left out of the connection-scheme hint in error messages.
	readOnly bool
	open     func(ctx context.Context, cfg *config) (store, error)
	// explainPlan describes, without connecting, the backend calls this driver
	// would make for a classified query and pushed predicate — the data the query
	// plan (--explain/--verbose) shows.
	explainPlan func(keys selector.KeySet, pred predicate.Node, unbounded bool) query.AccessPlan
	// explainWrite, explainClear, and explainDrop are the write-side counterparts:
	// each describes an `iq data` operation without connecting, so `--explain`
	// stays connection-free. explainDrop's bool reports whether the backend can
	// drop its container at all (false for Redis, whose DB index is not removable).
	explainWrite func(mode query.WriteMode) query.AccessPlan
	explainClear func() query.AccessPlan
	explainDrop  func() (query.AccessPlan, bool)
}

// drivers is the registry of every backend the CLI can dispatch to. Order is the
// listing order of `iq driver ls` and the enumeration order of expectedSchemes.
var drivers = []driver{
	{
		name:     "mongo",
		desc:     "MongoDB document store",
		schemes:  []string{"mongodb", "mongodb+srv"},
		doc:      "https://www.mongodb.com/docs/",
		versions: "4.2+",
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqmongo.Open(ctx, cfg.url, cfg.collection, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqmongo.ExplainPlan,
		explainWrite: iqmongo.ExplainWrite,
		explainClear: iqmongo.ExplainClear,
		explainDrop:  iqmongo.ExplainDrop,
	},
	{
		name:     "redis",
		desc:     "Redis key-value store",
		schemes:  []string{"redis", "rediss"},
		doc:      "https://redis.io/docs/",
		versions: "7.0+",
		open: func(ctx context.Context, cfg *config) (store, error) {
			return iqredis.Open(ctx, cfg.url, cfg.trace, cfg.decimalMode)
		},
		explainPlan:  iqredis.ExplainPlan,
		explainWrite: iqredis.ExplainWrite,
		explainClear: iqredis.ExplainClear,
		explainDrop:  iqredis.ExplainDrop,
	},
	{
		name:     "file",
		desc:     "Local dump file, read-only (JSONL, Redis RDB, Mongo BSON/JSON)",
		schemes:  []string{"file"},
		readOnly: true,
		open: func(_ context.Context, cfg *config) (store, error) {
			return iqfile.Open(cfg.url, cfg.decimalMode, cfg.fileCacheConfig())
		},
		explainPlan: iqfile.ExplainPlan,
	},
}

// driverForScheme returns the driver a URL scheme selects, dispatching over the
// registry. It is the one place that maps a scheme to a backend.
func driverForScheme(scheme string) (driver, bool) {
	for _, d := range drivers {
		for _, s := range d.schemes {
			if s == scheme {
				return d, true
			}
		}
	}
	return driver{}, false
}

// driverName returns the canonical driver name for a connection URL — the stable
// identity shown by `iq driver ls`, `iq ls`, `ping`, `inspect`, and `diff`, so a
// rediss:// and a redis:// source read as the same "redis" driver. An unknown or
// schemeless URL falls back to its raw scheme rather than an empty label.
func driverName(url string) string {
	scheme := schemeOf(url)
	if d, ok := driverForScheme(scheme); ok {
		return d.name
	}
	return scheme
}

// driverByName returns the driver with the given canonical name, dispatching
// over the registry. It backs `iq add -d/--driver`, which names a driver rather
// than a scheme.
func driverByName(name string) (driver, bool) {
	for _, d := range drivers {
		if d.name == name {
			return d, true
		}
	}
	return driver{}, false
}

// driverNames renders the registry's driver names as a comma-separated list for
// an error fragment, so the message stays in sync with the drivers that exist.
func driverNames() string {
	names := make([]string, len(drivers))
	for i, d := range drivers {
		names[i] = d.name
	}
	return strings.Join(names, ", ")
}

// expectedSchemes renders the registry's primary schemes as an error fragment
// ("expected mongodb:// or redis://"), so the message an unsupported URL yields
// stays in sync with the drivers that actually exist.
func expectedSchemes() string {
	// Only connectable backends are suggested for a bad connection URL; a local
	// read-only driver (file://) is discoverable via `iq driver ls`, not here.
	primaries := make([]string, 0, len(drivers))
	for _, d := range drivers {
		if d.readOnly {
			continue
		}
		primaries = append(primaries, d.schemes[0]+"://")
	}
	switch len(primaries) {
	case 1:
		return "expected " + primaries[0]
	case 2:
		return "expected " + primaries[0] + " or " + primaries[1]
	default:
		return "expected one of " + strings.Join(primaries, ", ")
	}
}

// driverRow is the JSON shape of one driver in `iq driver ls --json`.
type driverRow struct {
	Driver      string   `json:"driver"`
	Description string   `json:"description"`
	Schemes     []string `json:"schemes"`
	Versions    string   `json:"versions"`
	Doc         string   `json:"doc"`
}

// newDriverCmd builds `iq driver`: the backend-registry command group. Its `ls`
// subcommand lists the drivers iq can dispatch to, mirroring sq's `driver ls`.
func newDriverCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "driver",
		Short: "Inspect the backends iq can talk to",
	}
	c.AddCommand(newDriverLsCmd())
	return c
}

// newDriverLsCmd builds `iq driver ls`: list the registered backend drivers, each
// with its description, the URL schemes that select it, and its upstream docs.
func newDriverLsCmd() *cobra.Command {
	var jsonOut, yamlOut bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List the backend drivers iq can dispatch to",
		Long: "List the backend drivers iq can dispatch to. Each row shows the driver's stable\n" +
			"name (as `iq ls` reports it), a description, the URL schemes that select it, the\n" +
			"backend server versions the bundled client library supports, and a link to its\n" +
			"upstream documentation. -j/--json or -y/--yaml emit machine-readable output.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listDrivers(cmd.OutOrStdout(), jsonOut, yamlOut)
		},
	}
	c.Flags().BoolVarP(&jsonOut, "json", "j", false, "emit machine-readable JSON")
	c.Flags().BoolVarP(&yamlOut, "yaml", "y", false, "emit machine-readable YAML")
	c.MarkFlagsMutuallyExclusive("json", "yaml")
	return c
}

// listDrivers renders the driver registry as an aligned table (with a header
// row) or, with jsonOut/yamlOut set, as machine-readable output.
func listDrivers(out io.Writer, jsonOut, yamlOut bool) error {
	if jsonOut || yamlOut {
		rows := make([]driverRow, 0, len(drivers))
		for _, d := range drivers {
			rows = append(rows, driverRow{
				Driver:      d.name,
				Description: d.desc,
				Schemes:     d.schemes,
				Versions:    d.versions,
				Doc:         d.doc,
			})
		}
		return writeStructured(out, rows, yamlOut)
	}
	rows := [][]tableCell{{
		coloredCell("DRIVER", pal.header),
		coloredCell("DESCRIPTION", pal.header),
		coloredCell("SCHEMES", pal.header),
		coloredCell("VERSIONS", pal.header),
		coloredCell("DOC", pal.header),
	}}
	for _, d := range drivers {
		rows = append(rows, []tableCell{
			cell(d.name),
			cell(d.desc),
			cell(strings.Join(d.schemes, ", ")),
			cell(d.versions),
			cell(d.doc),
		})
	}
	return renderTable(out, rows)
}
