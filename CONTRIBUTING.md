# Contributing

## Requirements

- Go 1.27.0+ (matches `go.mod`)
- Docker: integration tests and the local stack; not needed for `go test -short`
- [gofumpt](https://github.com/mvdan/gofumpt), [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports),
  and [golangci-lint](https://golangci-lint.run) v2.13+ (earlier releases bundle a
  staticcheck that panics on Go 1.27 syntax) on `PATH`: `make check` runs them,
  and `make tools-dev` does not install them
- [uv](https://docs.astral.sh/uv/): docs site only; not needed to build or use `iq`

## Build

```bash
go build -o iq .   # plain build
make build         # embeds version, commit, and build date via ldflags
iq version         # check the result
```

A plain `go build` still reports a version recovered from Go's embedded build
info. `iq version` prints the version, commit, build date, and Go version;
`iq --version` prints the bare version alone (`v1.2.3`, or `dev+<commit>` for
an untagged build) so scripts can read it without parsing.

## Testing

```bash
go test -short ./...   # fast unit tests, no external services
go test ./...          # full suite, ephemeral backends via testcontainers-go
make e2e               # black-box tests that build and drive the iq binary
make bench             # decode, filter, number-conversion, and dump I/O benchmarks; pair two runs with benchstat
```

- The full suite starts ephemeral Redis, MongoDB, Cassandra, DynamoDB Local,
  CouchDB, Couchbase, Neo4j, Elasticsearch, and OpenSearch on random ports and
  tears them down afterwards, no manual compose needed. Cassandra takes
  ~1 minute to become ready, Couchbase ~30–60 s.
- Set `IQ_<BACKEND>_URL` (`IQ_REDIS_URL`, `IQ_MONGO_URL`, …) to a pre-started
  server (for example the compose stack) to skip container startup; the
  mutation gate wants this. `.env.example` is the tracked template with the
  full list and per-backend guidance.
- HBase is the exception: its native RPC needs fixed hostnames, so its tests
  run only when `IQ_HBASE_URL` points at the compose cluster
  (`docker compose up -d --wait hbase`, host networking, ~1–2 minutes ready).
- Against a shared Redis the suites use reserved DBs, 15/14 `cmd` scratch,
  13 the Redis driver's, 12 e2e, so data seeded into DB 0 survives a run.
- Live e2e round-trips (`e2e/live_test.go`) run only when `IQ_REDIS_URL` /
  `IQ_MONGO_URL` are set, no localhost fallback.

## Local stack

```bash
docker compose up -d --wait      # all backends on their published ports
bash scripts/seed-<backend>.sh   # example data: redis, mongo, cassandra, dynamodb,
                                 # hbase, couchdb, couchbase, neo4j, elasticsearch, opensearch
docker compose down
```

The stack runs under a fixed project name (`iq`) on a pinned `10.100.0.0/24`
bridge, so compose behaves the same from any worktree and the subnet cannot
collide with a LAN host. OpenSearch is published on 9201 to avoid
Elasticsearch on 9200, and HBase uses host networking (see `compose.yaml`).

## Quality gates

Local and layered: the quality gates run on your machine first, and
`.github/workflows/ci.yml` runs the same set on the GitHub mirror (see
[Continuous integration](#continuous-integration)). The full doctrine binds in
[AGENTS.md](AGENTS.md).

```bash
make check          # fast offline gate: format, vet, build, lint, dead code, short tests
make cover          # full suite + coverage floor
make security       # govulncheck + osv-scanner + gitleaks (tree + git history), SBOMs to dist/
make capabilities   # capslock capability drift (runs only when go.mod/go.sum moved)
make mutation       # mutago mutation gate over the branch diff
make ci             # all of the above, in order; start the compose stack first
make sbom           # SPDX + CycloneDX SBOMs only
```

### make check

Format (`gofumpt` + `goimports`), `go vet`, `go build`, `golangci-lint`
(gosec included), `deadcode`, `go test -short` with a coverage report. Lint
findings in `../<worktree>/...` paths are a stale cache from a removed
worktree; check clears the cache and retries once.

### make cover

Full container-backed suite with `-coverpkg=./...`; fails below
`IQ_COVER_MIN` (default 80). `IQ_COVER_SHORT=1` runs a fast report-only pass.

### Capability gate (`scripts/capabilities.sh`)

Asks what the dependency tree can *do* ([capslock](https://github.com/google/capslock)),
compared against the committed `capslock-baseline.json`. Conditional: runs
only when `go.mod`/`go.sum` differ from the merge-base with the base ref
(~7.3 GB peak RSS when it runs). On drift, read the printed call paths,
regenerate with `IQ_CAPS_UPDATE_BASELINE=1`, and justify each new high-risk
row in `capslock-baseline.notes.md`.

- `IQ_CAPS_BASE`: base ref (default `origin/main`)
- `IQ_CAPS_FORCE=1`: run regardless
- `IQ_CAPS_GOOS=linux|darwin|windows`: review aid for other targets, expected to differ
- `IQ_CAPS_UPDATE_BASELINE=1`: record a new baseline

### Mutation gate (`scripts/mutation-gate.sh`)

[mutago](https://github.com/quality-gates/mutago) over the branch diff vs
`origin/main`, targets narrowed to the changed packages. The contract is zero
survivors on covered code: an escaped covered mutant means a test asserts
nothing, strengthen the test; an errored or timed-out mutant also fails
(unverified, not killed). A genuine equivalent is accepted into
`mutago-baseline.json` with a justification in `mutago-baseline.notes.md`.
Run with the integration services up.

- `IQ_MUTATION_BASE`: base ref; empty for a full-module scan; a package arg
  (`bash scripts/mutation-gate.sh ./cmd`) full-scans that package
- `IQ_MUTATION_WORKERS`: parallel mutants (default 1; raise to 2-3 only when
  the run is already memory-bounded, e.g. inside a systemd-run MemoryHigh unit)
- `IQ_MUTATION_TIMEOUT_COEFFICIENT`: per-mutant timeout multiplier (default
  5; raise it for a legitimately slow package instead of letting mutants time out)
- `IQ_MUTATION_UPDATE_BASELINE=1`: accept equivalents (append-only)
- `IQ_MUTATION_MUTANT=<id>`: re-run one mutant as a diagnostic
- `IQ_MUTATION_DRYRUN=1`: mutant-count preview; scope it to one package

### make ci

check + cover + security + capabilities + mutation, in that order, the
capability step sits before mutation so their memory peaks never overlap.
Slowest target (mutago reruns the suite per mutant); start a shared stack
first so the containers are reused.

### Continuous integration

`.github/workflows/ci.yml` runs on the GitHub mirror only (the primary remote has
Actions off; `docs.yml` deploys the site and `release.yml` publishes releases,
both guarded the same way). Every push and pull request runs one job per gate:
`lint` (format, vet, golangci-lint), `test` (`go test -short -shuffle=on` on
Linux, macOS and Windows), `coverage` (`scripts/coverage.sh` with the floor, then
a reporting-only Codecov upload, `CODECOV_TOKEN` secret), `e2e` (redis pass, then
the mongo live flow), `cross` (CGO-off builds for the three shipped targets),
`vuln` (govulncheck), `osv` (OSV plus the permissive license allowlist), `sbom`
(syft), `deadcode`, `secrets` (gitleaks, tree and history), `capabilities`
(`scripts/capabilities.sh` against the PR base), `mutate-diff`
(`scripts/mutation-gate.sh` against the PR base) and `docs` (site build). The
weekly `deep` job (Mondays, or `workflow_dispatch`) re-runs the vulnerability and
secret scans against fresh data and mutates the whole module
(`IQ_MUTATION_BASE=` empty), then publishes the covered-code MSI from
`mutago-summary.json` as the README mutation badge on the one-file `badges`
branch.

Containers run one at a time in CI: no `IQ_*_URL` is set, so each driver's
`TestMain` provisions its own testcontainer, `GOFLAGS=-p=1` serialises the
package test binaries (coverage and mutation), and the e2e job brings up a
single compose service per pass. HBase has no testcontainers path and skips in
CI, as it does locally without `IQ_HBASE_URL`. The `test` job runs without
`-race` until the order-dependent data race in `cmd` (`color.NoColor`) is fixed.
Tool versions are pinned in the workflow's `env` block; keep them in sync with
the Makefile and `scripts/`.

## Docs

```bash
make docs        # build the Zensical site into docs/site/ (needs uv)
make docs-serve  # serve on 0.0.0.0:8000
```

Pages under `docs/docs/` are hand-maintained; nothing regenerates them from
the README.

### Generated artifacts

```bash
make man          # regenerate the committed docs/man/iq.1
make completions  # regenerate the committed docs/completions/iq.{bash,zsh,fish,ps1}
```

Both are shipped by the release packaging and drift-guarded by tests, so any
change to a command, flag, or help string regenerates them in the same commit.

## Toolchain

```bash
make tools-dev   # mutago, capslock, deadcode, govulncheck, osv-scanner, gitleaks, syft
make tools       # release tools: svu, git-chglog
```

The gates provision their own pinned mutago and capslock; `make tools-dev` is
for ad-hoc use.

## Commits

- Trunk-based: `main` always buildable; short-lived branches prefixed
  `feat/`, `fix/`, `chore/`, `test/`; one atomic task per branch.
- [Conventional Commits](https://www.conventionalcommits.org/):
  `<type>(<scope>): <description>`, lowercase imperative.
- If AI contributed to a commit, end its message with a `Co-Authored-By:`
  trailer naming the model and version, e.g.
  `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

## Releasing

```bash
make tools                          # one-time: svu + git-chglog
make version                        # print the version the next release would take
make changelog                      # regenerate CHANGELOG.md alone
bash scripts/release.sh --dry-run   # preview, no changes
make release-check                  # validate the goreleaser config
make release-snapshot               # local snapshot build of every artifact, no tag
make release                        # bump, regenerate CHANGELOG.md, commit, tag
git push --follow-tags              # release.sh never pushes
```

Conventional Commits drive the bump (`feat` → minor, `fix` → patch,
`!`/`BREAKING CHANGE` → major); `make release` must run on a clean `main`;
with no tags yet the first release is `v0.1.0`. Publishing happens in the
GitHub repository: the release workflow runs goreleaser when a `v*` tag
reaches it.

## Architecture

Ports and adapters: a driver-agnostic query core behind ports, backend
adapters and the CLI at the edge. Depth lives in the README's Architecture
section and the docs site's How it works page; the adapter contract in
`.agents/driver-contract.md`.

- `internal/selector`: pure jq-AST analysis. Classifies a filter as a
  bounded set of named keys or a scan, and a scan as streamable
  (`.[]`-rooted) or holistic. Depends only on the jq library.
- `internal/query`: the use cases. `JQEngine` routes bounded → `Get`,
  streamable scan → filter per `ScanBatches` page, holistic → materialize
  (caller-gated by `--unbounded`); `Runner` is `iq exec`; `Combiner` and
  `CrossEngine` reach other sources through the `SourceOpener` port. Optional
  capability ports carry the rest: `FilteredScanner` (pushdown), `Estimator`
  (scan totals), `TypedReader` on the copy's read side, and the write side
  `Putter`, `Clearer`, `Dropper`, `Deleter`, an adapter implements what its
  model supports, and a command type-asserts and rejects cleanly when a port
  is absent.
- `drivers/*`: one adapter per backend, each freezing that backend's
  type-to-JSON normalization and its inverse for writes; `drivers/file` is
  the read-only adapter over dump files and piped stdin, with a decode cache.
- `internal/pushdown` / `internal/predicate`: compile the pushable part of a
  filter's `select()` into the backend-neutral `predicate.Node` the adapters
  translate; `internal/rawpred` evaluates the same predicate over raw bytes
  for the client-side prefilters.
- `internal/render`: the output renderers behind the format flags.
- `internal/diff`: driver-agnostic structural diff (LCS-aligned arrays,
  optional multiset arrays, RFC 6902 patch rendering); no I/O.
- `internal/shape`: schema inference (JSON Schema draft 2020-12, ODCS
  projection, parent-relative presence); no I/O.
- `internal/parquetout`: the parquet export; contains the Arrow dependency.
- `internal/config` / `internal/secret`: TOML store for sources and option
  defaults; keyring port so passwords never enter the config file.
- `cmd`: CLI adapter and composition root: the self-describing driver
  registry (`cmd/driver.go`, one entry per backend), source resolution,
  stored-option merging, output selection, diagnostics.
- Small helpers: `internal/jqfmt` (filter pretty-printing), `internal/numfmt`
  (number normalization), `internal/neo4jenvelope` (the shared Neo4j record
  envelope).
