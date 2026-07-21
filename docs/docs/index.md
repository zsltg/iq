# iq

*This page mirrors the project [README](https://github.com/zsltg/iq/blob/main/README.md), which
remains the source of truth until the documentation is fully migrated.*

A Go command-line tool that runs [jq](https://jqlang.github.io/jq/) filters against NoSQL
databases. Redis, MongoDB, Apache Cassandra, Amazon DynamoDB, Apache HBase, Apache CouchDB,
Couchbase, Neo4j, Elasticsearch, and OpenSearch are supported; the
backend is chosen by the URL scheme, and the query core is driver-agnostic so further backends slot
in behind the same port.

The filter is both the transform and the key selector: its top-level paths name the keys to
fetch, so the store only ever reads a bounded set of keys — never a full keyspace scan, unless
you ask for one explicitly. Fetched values are normalized to JSON and the filter then runs
entirely client-side, so its semantics are identical for every backend.

`iq` is inspired by [sq](https://github.com/neilotoole/sq): much of its command surface — the
`<source>.<collection>` addressing along with many subcommands and flags — deliberately follows
sq's so the tool feels familiar.

!!! warning "Not production-ready"

    `iq` has potential rough edges — don't rely on it for critical work yet.

## Requirements

- Go 1.26+
- Docker (for the integration tests, which start ephemeral Redis + MongoDB + Cassandra + DynamoDB Local + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch containers; not needed for `go test -short`. HBase integration tests run only against a `docker compose` cluster named by `IQ_HBASE_URL`)
- [uv](https://docs.astral.sh/uv/) (optional, docs-only) — builds and serves the documentation site under `docs/` (`make docs` / `make docs-serve`); not needed to build or use `iq` itself

## Build

```bash
go build -o iq .          # plain build
make build                # build with version metadata embedded
```

`make build` injects the version, commit, and build date via ldflags; a plain `go build` still
reports a version recovered from Go's embedded build info. Check it with `iq version` or
`iq --version`.

## Releasing

Versioning is driven by [Conventional Commits](https://www.conventionalcommits.org/): the release
tooling reads the commit log, computes the next [semantic version](https://semver.org/), and
regenerates `CHANGELOG.md`. It is all-Go and local — no CI service or GitHub required.

```bash
make tools                        # one-time: install svu + git-chglog into GOPATH/bin
bash scripts/release.sh --dry-run # preview the next version and CHANGELOG.md diff, no changes
make release                      # bump, regenerate CHANGELOG.md, commit, and tag
git push --follow-tags            # publish the tag (release.sh never pushes for you)
```

`make release` must run on a clean `main`. `svu` picks the bump from the commit types since the
last tag (`feat` → minor, `fix` → patch, a `!`/`BREAKING CHANGE` → major); with no tags yet the
first release comes out as `v0.1.0`.
