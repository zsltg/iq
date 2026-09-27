---
icon: material/database-outline
---

# Sources

`iq` connects only through **saved sources**, a named connection you register
once, then select by name or as the default.

!!! note "Configuration"

    Sources live in a TOML file at `<os user config dir>/iq/iq.toml` (for example
    `~/.config/iq/iq.toml`), written `0600` because a URI can carry a password.
    Override the path with `IQ_CONFIG`, or per run with the global
    `--config <path>` flag (which wins over `IQ_CONFIG`).

    A source added with `--store keyring` keeps no password in this file, it
    lives in the OS keyring (Secret Service on Linux, Keychain on macOS,
    Credential Manager on Windows) and is spliced back into the URI only when
    connecting.

## Add `add`

`iq add <uri> [flags]`

Register a source from a connection URI. The URI is the only positional
argument.

Each driver supports a different set of URI parameters, see
[Drivers](drivers.md).

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| `-a` | `--active` | ✗ | make the new source the active source |
| `-d <string>` | `--driver <string>` | auto-detect | expected backend driver, must match the URI scheme |
| `-n <string>` | `--handle <string>` | the keyspace the URI names | handle for the source, derived from the URI when omitted |
| `-p` | `--password` | ✗ | prompt for the URI password or read it from stdin |
| | `--skip-verify` | ✗ | skip the post-add reachability check |
| | `--store <string>` | `inline` | where the URI's password is kept, `inline` (in the config file) or `keyring` (OS keyring) |

```sh { title='Add an inactive MongoDB source, defaults to handle "books"' }
iq add mongodb://localhost:27017/books
```
```sh { title='Add an inactive Redis source, sets the handle to "cache"' }
iq add -n cache redis://localhost:6379/0
```
```sh { title='Add a Cassandra source and make it active, defaults to handle "orders"' }
iq add -a 'cassandra://localhost:9042/shop?table=orders'
```
```sh { title='Add an inactive MongoDB source, prompts for the password for a user named "iq" in the "iq" database, defaults to handle "books"' }
iq add -p 'mongodb://iq@localhost:27018/iq?collection=books'
```
```sh { title='Add an inactive MongoDB source, prompts for the password for a user named "root" in the "admin" database, defaults to handle "books"' }
iq add -p 'mongodb://root@localhost:27018/iq?authSource=admin&collection=books'
```

!!! tip "Escaping the source URI"

    If the URI contains `?` (and `&`) your shell can interpret it as a
    wildcard, operator or separator, so you must escape it (for example `iq add
    'mongodb://localhost:27017/iq?collection=books'`).

!!! tip "&quot;add&quot; shadowing"

    `iq add` shadows `jq`'s built-in `add` filter at the top level. To sum with
    `jq`, write it inside a larger expression, for example `iq '[ .a, .b ] | add'`.

## Group `group`

`iq group [name] [flags]`

Show, set, or clear the active group.

A `/` in a name groups sources (`prod/books`, `dev/books`). Set an active group with
`iq group prod`, and an unqualified name resolves inside it, `iq src books` then selects
`prod/books`, falling back to a top-level `books` if the group has none.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--clear` | ✗ | clear the active group |

```sh { title='Show the active group' }
iq group
```
```sh { title='Set the active group to "dev"' }
iq group dev
```
```sh { title='Clear the active group' }
iq group --clear
```

!!! tip "Listing groups"

    You can list the already created groups with `iq ls -g`.

## List `ls`

`iq ls [group] [flags]`

List saved sources, the active one marked with `*`.

An optional `[group]` limits the listing to sources in that group.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| `-v` | `--verbose` | ✗ | show a header and a `FORMAT` (a file source's detected dump format) and an `OPTIONS` (the source's stored option defaults) column :material-earth:{ title="Global flag" } |
| `-g` | `--group` | ✗ | lists groups instead of sources |
| `-j` | `--json` | ✗ | emit machine-readable JSON output |
| `-y` | `--yaml` | ✗ | emit machine-readable YAML output |
| | `--reveal` | ✗ | prints a password stored inline in the config verbatim |
| | `--expand` | ✗ | resolves a keyring-backed source's stored password and inlines it (combine with `--reveal` to print verbatim) |

```sh { title='List saved sources' }
iq ls
```
```sh { title='List groups' }
iq ls -g
```
```sh { title='List saved sources, the saved options (if there is any) and the format for file sources' }
iq ls -v
```
```sh { title='List saved sources, prints passwords verbatim' }
iq ls --reveal
```

## Move `mv`

`iq mv <old> <new> [flags]`

Rename a source to a new full handle, or move it into a group by giving a
group-qualified target (`iq mv books prod/books`).

When `<old>` names a group, every source under it is re-prefixed
(`iq mv prod staging` renames `prod/*` to `staging/*`).

The active source and group follow the move. A keyring-backed source's stored
credential moves with it.

```sh { title='Rename source named "shop" to "catalog"' }
iq mv shop catalog
```
```sh { title='Move source named "books" into the group "prod"' }
iq mv books prod/books
```
```sh { title='Move every source in the group named "prod" to the group named "staging"' }
iq mv prod staging
```

## Ping `ping`

`iq ping [name...] [flags]`

Open each source and round-trip a cheap command (for example Redis `PING` or
MongoDB `{ping:1}`), reporting its driver and the round-trip time, or the error.

With no arguments the active source is pinged, otherwise each argument is a
source handle or a group (pinging every member). `--all` pings every saved
source and takes no arguments.

`--timeout` bounds each check. Exits with a non-zero exit code if any
source is unreachable.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--all` | ✗ | ping every saved source, rejects source arguments |
| | `--timeout` | `5s` | per-query timeout :material-earth:{ title="Global flag" } |

```sh { title='Ping the active source' }
iq ping
```
```sh { title='Ping the sources named "cache" and "shop"' }
iq ping cache shop
```
```sh { title='Ping the sources in the group named "dev"' }
iq ping dev
```
```sh { title='Ping every saved source' }
iq ping --all
```

## Remove `rm`

`iq rm <name>... [flags]`

Remove saved sources or whole groups. Each argument is a source handle or a
group name (which removes every source under it).

The removal is atomic, if any argument names neither a source nor a group,
nothing is removed. A keyring-backed source's stored credential is deleted too.

```sh { title='Remove a single source named "cache"' }
iq rm cache
```
```sh { title='Remove multiple sources named "cache", "shop" and "dev/books"' }
iq rm cache shop dev/books
```
```sh { title='Remove all sources in the group named "dev"' }
iq rm dev
```

## Show/Set `src`

`iq src [name] [flags]`

Show or set the active source.

`--src` :material-earth:{ title="Global flag" } is global, every command accepts it, see
[Global flags](global-flags.md#global-flags).

Once a source is active, every query runs against it. Select a different source for a single
command with `--src`/`-s`, without changing the active one. Address a MongoDB collection or a
Cassandra table with a dotted `handle.collection` / `handle.table` suffix. With no active source
and no `--src` the command errors, there is no ambient URI or environment fallback.

```sh { title='Show the active source' }
iq src
```
```sh { title='Set the active source to "shop"' }
iq src shop
```

!!! tip "Per command source selection"

    You can run commands against a specific source which can be different from
    the active one, for example `iq --src books '.["2"]'`.

## Inspect `inspect`

`iq inspect [source] [flags]`

Show a source's native server/database introspection.

The positional argument names the source, like `iq inspect books`, with none it
uses --src or the active source.

Certain sources accept `sq`-style addressing (for example MongoDB
`<source>.<collection>` or Cassandra `<source>.<table>`) to pick the
keyspace, overriding the default in the source's URI (for example
`?collection=` or `?table=`).

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--list` | ✗ | list the subcommands/sections available for the source |
| | `--only <strings>` | ✗ | narrow to these sections or subcommands |
| | `--reveal` | ✗ | print an inline-stored password verbatim in the location header instead of redacting it |
| | `--expand` | ✗ | resolves a keyring-backed source's stored password and inlines it (combine with `--reveal` to print verbatim) |
| `-j` | `--json` | ✗ | emit machine-readable JSON |
| `-y` | `--yaml` | ✗ | emit machine-readable YAML |

```sh { title='Inspect the active source on the database level' }
iq inspect
```
```sh { title='Inspect a named source on the database level' }
iq inspect shop
```
```sh { title='Inspect a named source on the collection level, output in JSON' }
iq inspect shop.orders -j
```
```sh { title='Inspect a named source on the database level, narrow the sections to "dbStats" and "serverStatus"' }
iq inspect shop --only dbStats,serverStatus
```

## Diff `diff`

`iq diff <a>[=<jq>] <b>[=<jq>] [flags]`

Compare two saved sources across the layers a schemaless store can meaningfully
compare. Selecting no layer defaults to `--data`, layers combine.

Each side can carry a `jq` filter, so a diff can be scoped to part of a
keyspace. Per side as `<source>=<jq>` or for both at once with `--filter`,
a side's own filter takes precedence.

Exits non-zero when the sources differ and zero when they match
(diff(1)-style[^2]), so scripts can branch on the exit status. Bounded by
`--timeout`.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--data` | ✓ | diff items key by key (cross-driver allowed, for example MongoDB `_id` and Redis key) |
| | `--filter <string>` | none | jq filter you root at `.[]`, scoping both sides, a spec's own `source=<jq>` overrides it for that side |
| | `--patch` | ✗ | emit an RFC 6902 JSON Patch[^1] that transforms the left source into the right (single layer only, for `--data` the pointers read `/<key>/<field>` over the whole keyspace map, excludes `--json`/`--yaml`/`--set-arrays`) |
| | `--schema` | ✗ | diff an inferred field/type shape (cross-driver allowed) based on a sample (use `--sample` to change the sample size) |
| | `--sample <int>` | `1000` | max items sampled per side for --schema (0 = all) |
| | `--stats` | ✗ | diff native introspection trees (same driver only), use with `--section` to narrow it down |
| | `--section <strings>` | full set | introspection section(s) for `--stats` (comma-separated or repeatable) |
| | `--set-arrays` | ✗ | compare arrays order-insensitively as multisets (duplicates counted), reporting membership deltas at the array's own path with no index segment, a pure reorder becomes no difference |
| `-j` | `--json` | ✗ | emit machine-readable delta in JSON |
| `-y` | `--yaml` | ✗ | emit machine-readable delta in YAML |

```sh { title='Diff the "prod" and "staging" source with a filter that applies to both sides' }
iq diff prod staging --filter '.[] | select(.status == "new")'
```
```sh { title='Diff the "prod" and "staging" source with a per-side filter' }
iq diff 'prod=.[] | select(.type == "order")' 'staging=.[] | select(.kind == "ORDER")'
```
```sh { title='Diff a single key from the "prod" and "staging" source' }
iq diff 'prod=.["orders:42"]' 'staging=.["orders:42"]'
```

!!! tip "Filtering"

    Write the filter `.[]`-rooted, as on a plain query (iteration is not
    implicit here, unlike `--insert`), because a keyed diff needs each item to
    keep its key and a pushable `select(...)` narrows the read at the backend
    rather than merely narrowing the report.

    `iq diff prod staging --filter '.[] | select(.status == "new")'`

    A filter can instead name a single key (`prod=.["orders:42"]`) to compare
    one document, where an absent key reports as removed rather than as a
    change to null.

    A filter that collapses the keyspace (`keys`, `map(...)`) or fans one item
    out into several values (`.[] | .tags[]`) is refused, neither leaves a key
    to match on.

!!! warning "Data diff"

    `--data` without filtering reads both keyspaces fully into memory, so it
    costs memory proportional to the two sources, a deliberate tradeoff, because
    an added/removed diff needs both key sets at once.

    A filter narrows that read, and a pushable one narrows it at the backend.

    Cross driver can be useful for verifying a migration, but the identity
    match is only as meaningful as the keys lining up, a power-user tool, not a
    schema comparison.

!!! warning "Schema diff"

    `--schema` used across drivers gives each field path (with `[]`
    array-element and `{}` map-value wildcards) a canonical type
    (`integer`/`number`/`string(fmt)`/`map`/`array`) and a `required`/`optional`
    presence, so it measures logical shape rather than sampling luck and stays
    quiet under resampling.

    The shape is sampled (with size set with `--sample`) and inferred, never
    declared, so a wider sample yields a truer shape.

    Two backends that genuinely normalize a native type differently (a
    timestamp as an RFC3339[^3] string vs an epoch number) still diff, that is
    the JSON each serves back, and the format tags make the row legible rather
    than mysterious.

    Arrays are aligned by a longest common subsequence, so a single insertion
    reports one addition rather than a cascade at every later index.

## Schema `schema`

`iq schema [source[=<jq>]] [flags]`

Sample a source and project a schema inferred from its values (the same
inference [`diff --schema`](#diff-diff) uses).

Unlike [`inspect`](#inspect-inspect), which shows a backend's native
introspection, `schema` infers a driver-agnostic shape, never declared, the
field/type structure is sampled (with a sample size defined with `--sample`)
from the values themselves, so a wider sample yields a truer shape.

It describes values, not keys, a non-object keyspace is legal (a string
keyspace emits `{"type":"string"}`), a filter that fans one item out into
several values is fine here even though a keyed [`diff`](#diff-diff) must
refuse it.

| short :material-flag-outline: | long :material-flag-outline: | default | description |
| --- | --- | --- | --- |
| | `--filter <string>` | none | jq filter scoping which items the shape is inferred from, the spec form `source=<jq>` sets it per source |
| | `--format <string>` | `jsonschema` | picks the contract dialect the shape projects into, JSON Schema draft 2020-12[^4] (`jsonschema`) or Open Data Contract Standard v3.1.0[^5] (`odcs`) |
| | `--sample <int>` | `1000` | max items sampled (0 = all) |
| `-y` | `--yaml` | ✗ | emit YAML instead of JSON (`jsonschema` only, `odcs` is always YAML) |

```sh { title='Schema of the active source' }
iq schema
```
```sh { title='Schema of the keyspace "orders" in the source "shop"' }
iq schema shop.orders
```
```sh { title='Schema of the source "dev" using a larger sample size' }
iq schema dev --sample 5000
```
```sh { title='Schema of a filtered subset in the keyspace "orders" in the source "dev"' }
iq schema 'dev.orders=.[] | select(.active)'
```
```sh { title='Save schema into a file from keyspace "orders" from the source "dev"' }
iq schema dev.orders > orders.schema.json
```
```sh { title='Save schema into a file from keyspace "orders" from the source "dev" in ODCS format' }
iq schema dev.orders --format odcs > orders.odcs.yaml  # emit an ODCS v3.1.0 contract
```

!!! tip "Scoped schema"

    A `source=<jq>` spec (or `--filter`) scopes which items the shape is
    inferred from, so `iq schema 'prod=.[] | select(.active)'` describes only
    the active ones, the sample cap then applies to the survivors, so a
    selective filter walks further into the keyspace to fill it.

!!! note "JSON Schema"

    JSON Schema draft 2020-12 (`jsonschema`, the default) can be used for
    interop with code generators, like
    [`quicktype`](https://github.com/glideapps/quicktype).

    `iq schema prod.orders > orders.schema.json && quicktype -s schema
    orders.schema.json -l go`

!!! note "Open Data Contract Standard"

    Open Data Contract Standard v3.1.0 (`odcs`) is a YAML data contract for
    tools like
    [`datacontract-cli`](https://github.com/datacontract/datacontract-cli),
    [Soda](https://github.com/sodadata/soda-core)
    and [Great Expectations](https://greatexpectations.io/).

    It carries nine logical types with no binary or decimal member, so `iq`'s
    date-time and date formats demote to `logicalType: date` with a JDK format
    pattern, a UUID stays `string` with the `uuid` format, and base64-binary
    and exact-decimal values stay plain `string`.

    The contract's identifiers (`id`, `name`, schema-object name) derive
    deterministically from the source handle and keyspace, no timestamps or
    random ids.

[^1]: JSON Patch defines a JSON document structure for expressing a sequence of
operations to apply to a JavaScript Object Notation (JSON) document. It is
suitable for use with the HTTP PATCH method. The "application/json-patch+json"
media type is used to identify such patch documents.
https://datatracker.ietf.org/doc/html/rfc6902
[^2]: `diff(1)` is a classic Unix command-line utility that compares two files
(or directories) line by line and outputs the differences between them.
https://pubs.opengroup.org/onlinepubs/9699919799/utilities/diff.html
[^3]: Date and time format for use in Internet protocols that is a profile of
the ISO 8601 standard for representation of dates and times using the Gregorian
calendar https://datatracker.ietf.org/doc/html/rfc3339
[^4]: JSON Schema is a declarative language for defining structure and
constraints for JSON data. https://json-schema.org/draft/2020-12
[^5]: The Open Data Contract Standard (ODCS) is an open-source, vendor-neutral specification (maintained under the Linux Foundation's LF AI & Data) for defining data contracts in machine-readable format (YAML or JSON). https://bitol-io.github.io/open-data-contract-standard
