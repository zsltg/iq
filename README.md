<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/docs/assets/iq-logo-white.svg">
    <img src="docs/docs/assets/iq-logo.svg" alt="iq" width="60">
  </picture>
</p>

<p align="center">
  <!-- Three rows, one per cluster, so the grouping holds at any width. One: proof it works,
  escalating from "it builds" to "the tests assert something". Two: third-party assessment.
  Three: furniture. A badge earns a slot only by showing a number that could be bad and that a
  stranger reads in one second. -->
  <a href="https://github.com/zsltg/iq/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/zsltg/iq/ci.yml?branch=main" alt="CI"></a>
  <a href="https://codecov.io/gh/zsltg/iq"><img src="https://img.shields.io/codecov/c/github/zsltg/iq/main?label=coverage" alt="Coverage"></a>
  <!-- The mutation badge stays hidden until the weekly scan can publish a real value. The scan
  must first run as shards, and every package must be green on the current mutago version.
  <a href="https://github.com/zsltg/iq/blob/main/DEVELOPMENT.md#mutation-gate-scriptsmutation-gatesh"><img src="https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/zsltg/iq/badges/mutation.json" alt="Mutation score"></a>
  -->
  <br>
  <a href="https://scorecard.dev/viewer/?uri=github.com/zsltg/iq"><img src="https://api.securityscorecards.dev/projects/github.com/zsltg/iq/badge" alt="OpenSSF Scorecard"></a>
  <a href="https://www.bestpractices.dev/projects/14996"><img src="https://www.bestpractices.dev/projects/14996/badge" alt="OpenSSF Best Practices"></a>
  <a href="https://codescene.io/projects/85289"><img src="https://codescene.io/projects/85289/status-badges/average-code-health" alt="CodeScene Code Health"></a>
  <br>
  <a href="https://github.com/zsltg/iq/releases"><img src="https://img.shields.io/github/v/release/zsltg/iq" alt="Release"></a>
  <a href="https://pkg.go.dev/github.com/zsltg/iq"><img src="https://pkg.go.dev/badge/github.com/zsltg/iq.svg" alt="Go Reference"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/zsltg/iq" alt="Go version">
  <a href="https://github.com/zsltg/iq/blob/main/LICENSE"><img src="https://img.shields.io/github/license/zsltg/iq" alt="License: MIT"></a>
</p>

<p align="center">
  <a href="https://zsltg.github.io/iq/">Documentation</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#install">Install</a> ·
  <a href="#use-with-ai-agents">AI agents</a> ·
  <a href="#usage">Usage</a> ·
  <a href="#drivers">Drivers</a> ·
  <a href="#backups-and-dumps">Dumps</a> ·
  <a href="#contributing">Contributing</a>
</p>

# iq

`iq` runs [jq](https://jqlang.github.io/jq/) filters to query, dump, copy, diff and write data
across NoSQL databases, and their dump files, from a single static binary. See
[Drivers](#drivers) for supported databases.

<p align="center"><img src="docs/docs/assets/demo.svg" width="100%" alt="iq registers a MongoDB source, reads one document by key, filters a scan with a pushed-down predicate, explains the plan, and prints the result as gron"></p>

## Why iq

- Query any supported store with one language. The same jq filter runs on Redis, MongoDB,
  Cassandra, DynamoDB and the other [drivers](#drivers), and gives the same result on each. One
  filter can also [join data from several sources](https://zsltg.github.io/iq/query-data/#cross-source-queries),
  with `source()` or [`iq combine`](https://zsltg.github.io/iq/query-data/#combine-combine).
- A filter that names keys fetches only those keys. A `.[]` filter scans the source in
  pages.
- Query and compare dumps without a database server. Restore them to a live store.
- Compare two sources, or a source and a dump, by data, stats, or inferred schema.
  [`iq diff`](https://zsltg.github.io/iq/sources/#diff-diff) returns `diff(1)` exit codes, so a
  CI job can fail on drift.
- Infer a schema from a sample of any source with
  [`iq schema`](https://zsltg.github.io/iq/sources/#schema-schema), as
  [JSON Schema draft 2020-12](https://json-schema.org/draft/2020-12) or an
  [Open Data Contract Standard](https://bitol-io.github.io/open-data-contract-standard)
  v3.2.0 contract.
- Copy typed data across stores in one command, with no export script and no conversion
  script.
- Give an AI agent access to every store with one
  [Agent Skill and one MCP server](#use-with-ai-agents). The skill works with any agent that
  reads the Agent Skills format. The server is read-only by default.

`iq` is inspired by [sq](https://github.com/neilotoole/sq), whose command set it
deliberately follows to make the tool feel familiar. Read
[Comparison](https://zsltg.github.io/iq/comparison/) for how `iq` relates to other tools,
and [How it works](https://zsltg.github.io/iq/how-it-works/) for the read routes and
pushdown.

> [!NOTE]
> `iq` is built with AI assistance. Every change passes the full test suite,
> container-backed integration tests for every backend, and a mutation gate before it lands
> (see [CONTRIBUTING.md](CONTRIBUTING.md)).
>
> Feedback and bug reports are very welcome. Report security problems privately, see the
> [security policy](https://github.com/zsltg/iq/security/policy).

## Quick start

You need Docker, and `iq` on your `PATH`. To install `iq` on Linux or macOS:
```sh
curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/install.sh | sh
```

For other platforms, see [Install](#install). If the installer prints a note about `PATH`, add
the directory that the note names.

Run each command on its own. If a command fails, stop and read its error.

Start a disposable MongoDB that only this machine can reach:
```sh
docker run -d --name iq-quickstart -p 127.0.0.1:27018:27017 mongo:8.2
```

Register the database as the active source. `iq add` connects to make sure that the source
works, and `--timeout 30s` gives MongoDB time to start:
```sh
iq add -a --timeout 30s 'mongodb://localhost:27018/iq?collection=books'
```

Write two sample books. `iq` reads piped stdin as a source and takes each key from the `id`
field:
```sh
printf '%s\n' '{"id":1,"title":"Dune","year":1965,"price":9.99}' \
              '{"id":2,"title":"Project Hail Mary","year":2021,"price":14.5}' |
  iq --insert books --key-field id
```

Read one book by its key. `iq` fetches only that document:
```sh
iq '.["2"]'
```

The output is similar to the following:
```json
{
  "_id": "2",
  "id": 2,
  "price": 14.5,
  "title": "Project Hail Mary",
  "year": 2021
}
```

Filter a scan, and show the query plan:
```sh
iq '.[] | select(.year > 2015) | {title, price}' --explain
```

The output is similar to the following, where `server filter` shows the part of the filter
that MongoDB runs. The full filter then runs again on the client, so the result is the same
with or without pushdown:
```text
query plan
source: books (mongo)
...
mongo calls:
  find(<filter>): server-side pre-filter, then the full jq re-runs client-side
  cursor streamed in batches of 100

server filter (mongo find):
  {                                 — .year > 2015
    "$or": [
      {"year": {"$gt": 2015}},
      {"year": {"$type": ["string", "array", "object"]}}
    ]
  }
...
```

Without `--explain`, the same command prints `{"price": 14.5, "title": "Project Hail Mary"}`.

When you are done, remove the container and the source:
```sh
docker rm -f iq-quickstart
iq rm books
```

## Install

`iq` ships as a single static binary (no runtime dependencies, no CGO), prebuilt for Linux,
macOS, and Windows on amd64 and arm64.

### Linux

```sh
curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/install.sh | sh
```

The script downloads the release for your OS/arch, verifies its SHA-256 against the release
checksums, and installs the binary. `IQ_VERSION` pins a version and `IQ_INSTALL_DIR` picks the
target directory. Or grab a `.deb`, `.rpm`, `.apk`, or Arch `.pkg.tar.zst` from the
[releases](https://github.com/zsltg/iq/releases). Each release carries a cosign signature
and SLSA build provenance, see [Verify a release](https://zsltg.github.io/iq/#verify-a-release).

### macOS

```sh
brew install zsltg/tap/iq
```

The Linux `curl … | sh` one-liner works on macOS too.

### Windows

```powershell
scoop bucket add zsltg https://github.com/zsltg/scoop-bucket
scoop install iq
```

### Go

```sh
go install github.com/zsltg/iq@latest
```

### From source

```sh
git clone https://github.com/zsltg/iq
cd iq && make build
```

### Shell completions and man page

The `.deb`, `.rpm`, `.apk` and `.pkg.tar.zst` packages install bash, zsh and fish
completions and the `iq(1)` man page. For a brew, scoop, go-install or source build,
`iq completion <shell>` prints a completion script and `iq man` prints the man page. Read
[Shell completions](https://zsltg.github.io/iq/#shell-completions) for the setup of each
shell.

## Use with AI agents

### Agent Skill

`iq` ships an [Agent Skill](https://agentskills.io), a single Markdown file
([`skills/iq/SKILL.md`](skills/iq/SKILL.md)) that any agent reading the Agent Skills format
can load. It covers these topics:

- Finding a source
- `--explain` before every scan
- `--dry-run` before every write
- The machine-readable output flags and the JSON error shape
- The rules around destructive commands.

```sh
npx skills add zsltg/iq
```

### MCP server

For an agent that speaks [MCP](https://modelcontextprotocol.io), `iq mcp`
serves the same core over stdio.

It is read-only by default. `--allow writes|exec|destructive` opens the rest.
A tool that is not allowed is never registered. Every result is capped, every
error is redacted, and every destructive call is confirmed.

Claude Code:
```sh
claude mcp add iq -- iq mcp --timeout 30s
```

Codex CLI:
```sh
codex mcp add iq -- iq mcp --timeout 30s
```

Antigravity CLI:
```sh
agy mcp add iq -- iq mcp --timeout 30s
```

OpenCode, Cursor, Cline, the Antigravity IDE and Copilot in VS Code take a JSON block. Read
[AI agents](https://zsltg.github.io/iq/agents/#client-configuration) for the block and the
configuration file of each client.

## Usage

### Sources

Driver list:
```sh
iq driver ls
```

Add a collection named "books" from a MongoDB source and make it active:
```sh
iq add -a 'mongodb://localhost:27017/iq?collection=books'
```

Inspect a source:
```sh
iq inspect books
```

List sources:
```sh
iq ls
```

Check [Sources](https://zsltg.github.io/iq/sources/) for more details.

### Query data

Fetch an item with the ID "2":
```sh
iq '.["2"]'
```

Filter a scan and reshape each item:
```sh
iq '.[] | select(.year > 2015) | {title, price}'
```

Print a formatted query plan:
```sh
iq '.[] | select(.year > 2015) | {title, price}' --explain -v
```

Check [Query data](https://zsltg.github.io/iq/query-data/) for more details.

### Diff

Diff schema of two sources:
```sh
iq diff --schema dev qa
```

Diff the items with the same ID from two sources:
```sh
iq diff 'dev=.["1"]' 'qa=.["1"]'
```

Diff items key by key:
```sh
iq diff dev qa
```

Check [Diff](https://zsltg.github.io/iq/sources/#diff-diff) for more details.

### Write data

> [!IMPORTANT]
> Queries are read-only. `--insert`, `--replace`, `iq data clear`, `iq data drop` and
> `iq data delete` write to the target. `iq exec` forwards a native command to the database,
> so it can write too. Use `--explain` to see the query plan, or `--dry-run` to report the
> effect of a write without changing anything. `iq exec` has no dry run.

Copy one source into another, keys preserved (a cross-driver copy carries values only):
```sh
iq --src books --insert books2
```

Create a lossless (typed) dump:
```sh
iq --src cache --typed -o dump.jsonl
```

Add a dump as a source and restore it to a live source:
```sh
iq add file:///dump.jsonl -n snap
iq --src snap --insert cache
```

Copy only the items a filter selects:
```sh
iq '.[] | select(.year > 2015)' --src books --insert recent
```

Check [Write data](https://zsltg.github.io/iq/write-data/) for more details.

### Cross-source query

Compose across sources:
```sh
iq 'INDEX(source("users"; ".[]"); .id) as $u
    | source("orders"; ".[] | select(.total > 99)")
    | {name: $u[.userId].name, total}'
```

Combine across sources:
```sh
iq combine 'users=.[] | {id, name}' \
           'orders=.[] | select(.total > 99)' \
   --with '($users | INDEX(.id)) as $u | $orders[] | . + {name: $u[.userId].name}'
```

Check [Cross-source queries](https://zsltg.github.io/iq/query-data/#cross-source-queries) for more details.

### Keyspace commands

Delete two items:
```sh
iq data delete cache book:1 book:2
```

Empty a source called "cache":
```sh
iq data clear cache
```

Drop a collection called "orders":
```sh
iq data drop shop.orders
```

Check [Delete](https://zsltg.github.io/iq/write-data/#delete-data-delete),
[Clear](https://zsltg.github.io/iq/write-data/#clear-data-clear) and
[Drop](https://zsltg.github.io/iq/write-data/#drop-data-drop) for more details.

### UNIX pipes

Implicit stdin source:
```sh
cat dump.jsonl | iq '.[]'
```

Check [Insert](https://zsltg.github.io/iq/write-data/#insert-insert) for more details on piped stdin.

## Drivers

`iq` picks the backend from a source's URI scheme. The query core is driver-agnostic, so
further backends slot in behind the same port. The
[Drivers page](https://zsltg.github.io/iq/drivers/) documents these topics for each driver:

- Keyspace mapping
- Value encoding
- Predicate pushdown
- Raw commands.

| Name | Database | Versions |
| ---- | ----------- | -------- |
| `cassandra` | [Apache Cassandra](https://cassandra.apache.org/doc/) | 3.11+ |
| `couchbase` | [Couchbase](https://docs.couchbase.com/) | 7.x, 8.x (Community or Enterprise) |
| `couchdb` | [Apache CouchDB](https://docs.couchdb.org/) | 2.x, 3.x |
| `dynamodb` | [Amazon DynamoDB](https://docs.aws.amazon.com/dynamodb/) | AWS (managed) |
| `elasticsearch` | [Elasticsearch](https://www.elastic.co/docs/) | 8.x |
| `file` | Local dump file, read-only (see [Backups and dumps](#backups-and-dumps)) | — |
| `hbase` | [Apache HBase](https://hbase.apache.org/book.html) | 1.0+ |
| `mongo` | [MongoDB](https://www.mongodb.com/docs/) | 4.2+ |
| `neo4j` | [Neo4j](https://neo4j.com/docs/) | 5.x |
| `opensearch` | [OpenSearch](https://opensearch.org/docs/) | 2.x, 3.x |
| `redis` | [Redis](https://redis.io/docs/) | 7.0+ |

The Versions column lists the range of backend server versions the bundled client library
supports.

### Guarantees

Every backend honors the same contract. The
[Drivers page](https://zsltg.github.io/iq/drivers/#guarantees) gives the details for each
driver.

- **One URI, native nouns.** The URI scheme picks the driver. The keyspace is the backend's
  own noun, such as `?collection=`, `?table=` or `?index=`.
- **One jq interface.** A filter that names keys fetches only those keys, and a missing key
  reads as `null`. A `.[]` filter streams the keyspace in pages. A filter that needs the
  whole keyspace at once runs only with `--unbounded`.
- **Pushdown never changes results.** A pushed predicate only pre-filters. The full jq
  filter always runs again on the client, and `--explain` shows where each condition runs.
- **Capabilities are explicit.** If a backend cannot support a command, the command fails
  with a clear message instead of emulating it. For example, Redis has no `drop`.
- **Values round-trip.** Every value normalizes to JSON under a fixed encoding for each
  backend. A `--typed` dump restores losslessly through `--insert`.
- **Bounded and redacted.** `--timeout` bounds every backend call. `iq` removes a URI's
  password from every listing, log line and error.
- **Native commands.** `iq exec` sends a command in the backend's own language, such as CQL,
  Cypher or a MongoDB command document, to the backend unchanged.

## Backups and dumps

A `file://` source reads a database dump straight from disk. As a result, you can do these
operations on a snapshot through the same jq interface, with no running server:

- Query it
- Inspect it for shape
- Diff it against a live source
- Restore it.

A `file://` source is read-only. A `file://` endpoint is never a copy destination. `iq exec` and
`iq inspect`, which need a live server, do not apply.

Register a dump like any other source:
```sh
iq add -n snap file:///backups/prod.rdb
```

Query, diff and restore it with no server running:
```sh
iq --src snap '.["session:42"]'
iq diff snap cache --data
iq --src snap --insert cache
```

| Type | Description |
| ---- | ----------- |
| `jsonl` | iq typed JSON Lines / array |
| `yaml` | iq typed YAML |
| `mongoexport` | mongoexport Extended JSON |
| `bson` | mongodump BSON |
| `rdb` | Redis RDB snapshot |
| `?format=dynamodb-json` | DynamoDB S3 export / scan JSON |
| `?format=cassandra-csv` | cqlsh COPY TO CSV |
| `?format=neo4j-json` | Neo4j APOC JSON export |

A type with a bare name auto-detects (`file:///<file_path>`). For a type in the `?format=` rows,
you must pass the `?format=` form (`file:///<file_path>?format=<source_format>`). The
[Drivers page](https://zsltg.github.io/iq/drivers/#file-dumps) documents what produces each
format, the options some of them need, and the round-trip caveats.

## See also

Where `iq` sits among other tools, and the jq ecosystem its filter language carries over.

- [jq manual](https://jqlang.org/manual/): the language reference for the filters `iq` runs.
- [awesome-jq](https://github.com/jqlang/awesome-jq): a curated list of jq tools, guides, and resources.
- [sq](https://github.com/neilotoole/sq): jq-style queries over SQL databases and document files.
- [gojq](https://github.com/itchyny/gojq): the pure-Go jq implementation `iq` embeds.
- [jaq](https://github.com/01mf02/jaq): a Rust jq clone focused on speed and stricter semantics.
- [yq](https://github.com/mikefarah/yq): jq-style filters for YAML, TOML, and XML.
- [fq](https://github.com/wader/fq): jq for binary formats.
- [jc](https://github.com/kellyjonbrazil/jc): converts classic CLI output to JSON.
- [jqp](https://github.com/noahgorstein/jqp): a TUI playground that live-previews a filter as you type.
- [ijq](https://github.com/gpanders/ijq): interactive jq with a side-by-side input and output view.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) to start, and [DEVELOPMENT.md](DEVELOPMENT.md) for the
build, test and gate commands. Report security problems privately through the
[security policy](https://github.com/zsltg/iq/security/policy).

## License

MIT. See [LICENSE](LICENSE).
