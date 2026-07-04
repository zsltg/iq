# iq

A Go command-line tool that runs [jq](https://jqlang.github.io/jq/) filters against NoSQL
databases. Redis is the first supported backend; the query core is driver-agnostic so further
backends slot in behind the same port.

The filter is both the transform and the key selector: its top-level paths name the keys to
fetch, so the store only ever reads a bounded set of keys — never a full keyspace scan, unless
you ask for one explicitly. Fetched values are normalized to JSON and the filter then runs
entirely client-side, so its semantics are identical for every backend.

## Requirements

- Go 1.25+
- Docker (optional, for the local Redis used by integration tests)

## Build

```bash
go build -o iq .
```

## Usage

The default action is a jq filter. Its top-level paths name the keys to fetch; the result is
printed as JSON:

```bash
./iq '.greeting'                          # fetch key "greeting"
./iq '.["book:1"]'                        # a key containing a colon needs bracket-quoting
./iq '.["book:1"].title'                  # fetch book:1, extract one field
./iq '[ .["book:1"].title, .["book:2"].title ]'   # fetch both keys, project a field from each
./iq '.["book:2"].price | tonumber + 5'   # values are strings; convert before arithmetic
```

Always wrap the filter in single quotes. jq syntax is full of characters the shell would
otherwise expand or split — brackets (`[ ]`), whitespace, `|`, `*`, `$` — and bracket-quoting a
colon key like `.["book:1"]` reads as a glob to zsh (`no matches found`) or bash unless quoted.

### Bounded reads and scans

`iq` fetches exactly the keys your filter names, so a normal query's cost is bounded by the keys
you asked for, never by the size of the database. A filter that instead needs the whole dataset —
a bare `.`, value iteration `.[]`, `keys`, `map(...)`, `..` — is an **unbounded scan**. Those are
not refused, but they run only when you opt in with `--unbounded`:

```bash
./iq '.[] | select(.year)'                # error: requires an unbounded full-keyspace scan
./iq --unbounded 'keys'                    # list every key
./iq --unbounded '.'                       # the whole dataset as one JSON object
./iq --unbounded '[ .[] | objects | select((.year|tonumber) > 2015) | .title ]'   # discovery
```

The flag names the cost property (an unbounded read), not any one store's mechanism, so it will
mean the same thing for future backends (a Cassandra full scan, a CouchDB `_all_docs`).

### Value encoding

Each fetched Redis value is normalized to JSON by type:

| Redis type | JSON shape |
| --- | --- |
| string | the string verbatim (numeric strings stay strings; use `tonumber`) |
| hash | object `{field: value}` |
| list | array, in list order |
| set | array, sorted lexically (sets have no native order) |
| sorted set | array of `{"member": ..., "score": ...}`, in ascending score order |
| stream | array of `{"id": ..., "fields": {field: value}}`, in entry order |
| RedisJSON | the stored document, parsed as JSON |
| missing key | `null` |

Other module types (time series, bloom, …) have no frozen encoding yet; a named read of one is
refused with a clear message.

### Raw commands

`iq raw` forwards a command to the database verbatim and prints the reply in redis-cli style —
the escape hatch for writes, administration, and seeding the jq read path does not cover:

```bash
./iq raw SET greeting hello   # "OK"
./iq raw GET greeting         # "hello"
./iq raw INCR counter         # (integer) 1
./iq raw GET missing          # (nil)
```

Its output mirrors redis-cli's cooked style: bulk strings quoted, integers as `(integer) N`, a
missing value as `(nil)`, and arrays as a numbered, indented list. The client uses RESP2 so
aggregate replies match redis-cli's classic flat output. Status replies such as `OK` and `PONG`
appear quoted, a limitation of the underlying client, which does not distinguish them from bulk
strings.

### Connection

The database is addressed with a standard Redis connection URL
(`redis://[user:pass@]host:port[/db]`, `rediss://` for TLS), resolved in this order:

1. the `--url` / `-u` flag
2. the `IQ_REDIS_URL` environment variable
3. the default `redis://localhost:6379/0`

`--timeout` (default `5s`) bounds each query.

## Architecture

The query core is driver-agnostic and lives behind two ports a backend adapter implements:

- `internal/selector` — pure static analysis. `Keys` walks a parsed jq AST and classifies the
  filter: either a **bounded** set of named keys, or a **scan** (it needs the whole keyspace). It
  depends only on the jq library, never on a driver.
- `internal/query` — the use cases. `JQEngine` parses the filter, asks `selector` to classify it,
  refuses a scan unless the caller permits one, then fetches through the `KVStore` port (`Get` for
  named keys, `ScanAll` for a scan), assembles the `{key: value}` object, and runs the filter
  client-side, streaming each result. `Runner` is the raw-command use case behind the `Store` port.
- `internal/redis` — the Redis adapter. One `*Store` satisfies both ports: `Query` (raw) and
  `Get`/`ScanAll` (jq), with the type-to-JSON normalization frozen as the encoding contract.
- `cmd` — the CLI adapter. The root command is the jq action; `raw` is the verbatim escape
  hatch. JSON and redis-cli formatting live here, keeping the core free of any output format.

The filter runs entirely client-side over a materialized slice, so cost is `O(keys requested)`
and the jq semantics are identical for any future backend behind `KVStore`. jq is provided by
[gojq](https://github.com/itchyny/gojq) (pure Go, no cgo), which keeps `iq` a single static
binary and exposes the AST the key selector walks.

## Common commands

```bash
go build -o iq .          # build the binary
go test -short ./...      # fast unit tests, no external services
docker compose up -d --wait   # start a local Redis (redis:latest) on :6379
bash scripts/seed.sh      # load example data into the running Redis
go test ./...             # full suite, including Redis integration tests
docker compose down       # stop the local Redis
gofumpt -w . && goimports -w .   # format
go vet ./... && golangci-lint run   # vet and lint
govulncheck ./...         # dependency vulnerability scan
bash scripts/mutation-gate.sh   # mutation gate (run with Redis up; fails on any survivor/timeout)
```

Integration tests skip under `go test -short`; the full `go test ./...` needs Redis up (via
`docker compose up`) and connects to `IQ_REDIS_URL` or the local default.
