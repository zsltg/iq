---
icon: material/book-open-blank-variant-outline
---

# Common commands

*This page is the full catalogue of developer-facing commands and environment variables, and
the only place it lives — the [README](https://github.com/zsltg/iq/blob/main/README.md) is the
user-facing overview. A change that adds or alters a command, a dependency or an environment
variable updates this page in the same change.*

```bash
go build -o iq .          # build the binary
make build                # build with version metadata embedded
iq version                # print version, commit, build date, and Go version
iq config set format yaml # persist a default flag value (add --src <name> to scope it to a source)
iq config ls -v           # list every persistable option: value, default, and help
iq --config ./iq.toml ls  # run against an alternate config file (overrides IQ_CONFIG)
iq --src books --insert books2 # copy a source into another (handle → handle, cross-driver ok; jq filter transforms each item)
iq --src cache --typed -o dump.jsonl   # dump a source to a re-importable typed JSONL file (Redis-lossless)
iq add file:///backups/prod.rdb -n snap   # register a dump file as a read-only source
iq --src snap '.[] | select(.active)'     # query a dump offline (RDB, BSON, mongoexport, DynamoDB JSON, Cassandra CSV, Neo4j APOC JSON, JSONL)
iq --src snap --insert prod               # restore a dump into a live source (both registered with iq add)
iq data clear books       # empty a container (drop removes it; both prompt unless --force)
iq schema prod.orders     # infer a draft 2020-12 JSON Schema from a sampled source (--sample; describes values, not keys)
iq schema prod.orders > s.json && quicktype -s schema s.json -l go  # generate typed models (a schema is field names/types, a few hundred bytes — not a dump of documents)
iq schema prod.orders --format odcs > orders.odcs.yaml  # emit an Open Data Contract Standard v3.1.0 contract (YAML)
iq --src prod '.[]' --format parquet -o orders.parquet   # export results as an Apache Parquet file (typed columns from the shape sample; binary, refused to a terminal — redirect or -o)
go test -short ./...      # fast unit tests, no external services
go test ./...             # full suite; starts ephemeral Redis + MongoDB + Cassandra + DynamoDB Local + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch via testcontainers-go (HBase needs IQ_HBASE_URL)
docker compose up -d --wait   # optional: local Redis + MongoDB + Cassandra + DynamoDB Local + HBase + CouchDB + Couchbase + Neo4j + Elasticsearch + OpenSearch for manual exploration (:6379, :27017, :9042, :8000, :2181, :5984, :8091-8096/:11210, :7687, :9200, :9201)
bash scripts/seed-redis.sh    # load example data into the running Redis
bash scripts/seed-mongo.sh    # load example documents into the running MongoDB
bash scripts/seed-cassandra.sh    # load example rows into the running Cassandra
bash scripts/seed-dynamodb.sh    # load example items into the running DynamoDB Local
bash scripts/seed-hbase.sh    # load example rows into the running HBase
bash scripts/seed-couchdb.sh    # load example documents into the running CouchDB
bash scripts/seed-couchbase.sh    # provision + load example documents into the running Couchbase
bash scripts/seed-neo4j.sh    # load an example graph into the running Neo4j
bash scripts/seed-elasticsearch.sh    # load example documents into the running Elasticsearch
bash scripts/seed-opensearch.sh    # load example documents into the running OpenSearch (:9201)
docker compose down       # stop the local services
gofumpt -w . && goimports -w .   # format
go vet ./... && golangci-lint run   # vet and lint
govulncheck ./...         # dependency vulnerability scan
bash scripts/mutation-gate.sh   # mutation gate, scoped to the branch diff vs origin/main and enumerating only the packages with a changed non-test file (fails on any escaped mutant not in the baseline, and on any errored/timed-out one; IQ_MUTATION_TIMEOUT_COEFFICIENT, default 5; IQ_MUTATION_WORKERS, default 1); set IQ_*_URL to a pre-started stack
bash scripts/capabilities.sh    # capability gate: what a dependency can *do*, compared against the committed capslock baseline (skips unless go.mod/go.sum changed vs origin/main; ~7.3 GB peak RSS when it runs)
make check                # fast offline gate: format, vet, build, lint, dead code, unit tests + coverage report
make cover                # full suite + coverage floor (IQ_COVER_MIN, default 80); IQ_COVER_SHORT=1 for a fast report-only run
make security             # supply-chain + secrets sweep (govulncheck, osv-scanner, gitleaks) + SBOMs to dist/
make sbom                 # write SPDX + CycloneDX SBOMs of the module to dist/
make e2e                  # black-box smoke tests that build and drive the iq binary (+ live Redis/Mongo round-trips when IQ_REDIS_URL/IQ_MONGO_URL are set)
make bench                # JSON decode + pre-filter benchmarks (no containers); pair two runs with benchstat: make bench | tee new.txt; benchstat old.txt new.txt
make docs                 # build the documentation site (Zensical) into docs/site/ (needs uv; pages under docs/docs/ are hand-maintained, nothing regenerates them)
make docs-serve           # serve the documentation site on 0.0.0.0:8000, reachable over the LAN (needs uv)
make capabilities         # capability-drift gate (capslock) vs capslock-baseline.json; runs only when go.mod/go.sum changed (IQ_CAPS_FORCE=1 forces it, IQ_CAPS_BASE overrides the base ref, IQ_CAPS_GOOS=darwin|windows is a review aid, IQ_CAPS_UPDATE_BASELINE=1 records a new set)
make mutation             # mutation gate over the branch diff vs origin/main (part of make ci)
make ci                   # full pre-merge gate: check + cover + security + capabilities + mutation (needs Docker + network)
make tools                # install release tools (svu, git-chglog) into GOPATH/bin
make tools-dev            # install the quality/security toolchain (mutago, capslock, deadcode, govulncheck, osv-scanner, gitleaks, syft)
make version              # print the version the next release would take
bash scripts/release.sh --dry-run   # preview the next release without changing anything
make release              # bump version, regenerate CHANGELOG.md, commit, and tag
```

Integration tests skip under `go test -short`. The full `go test ./...` needs Docker: it starts
an ephemeral Redis, MongoDB, Cassandra, DynamoDB Local, CouchDB, Couchbase, Neo4j, Elasticsearch, and OpenSearch via
[testcontainers-go](https://github.com/testcontainers/testcontainers-go) on random ports and
tears them down afterwards — no manual `docker compose up` (Cassandra takes ~1 minute to become
ready; Couchbase is the slowest, ~30-60 s). Set `IQ_REDIS_URL` / `IQ_MONGO_URL` / `IQ_CASSANDRA_URL` / `IQ_DYNAMODB_URL` / `IQ_COUCHDB_URL` / `IQ_COUCHBASE_URL` / `IQ_NEO4J_URL` / `IQ_ELASTICSEARCH_URL` / `IQ_OPENSEARCH_URL`
to point at an already-running server (for example the `docker compose` stack) to skip container
startup; the mutation gate, which reruns the suite per mutant, wants this to avoid churn. Against
a shared Redis the suites operate on reserved databases so no run flushes another's data — 15 and 14
are the `cmd` integration scratch pair, 13 is the Redis driver's private DB, and 12 is the black-box
e2e round-trip's — so data seeded into DB 0 by `scripts/seed-redis.sh` survives a test run. The live
e2e flows (`e2e/live_test.go`) drive the built binary against a real backend and run only when
`IQ_REDIS_URL` / `IQ_MONGO_URL` are set, with no localhost fallback, so the offline suite never needs
a server. **HBase is the exception**: its native RPC
needs a fixed-hostname cluster (testcontainers' random ports would break the region server's
advertised name), so its integration tests run only when `IQ_HBASE_URL` points at a running cluster
(`docker compose up -d --wait hbase`); without it they skip. The compose HBase service uses host
networking so a host-side client reaches the region server, and takes ~1–2 minutes to become ready.
The compose stack runs under a fixed project name (`iq`) on a pinned `10.100.0.0/24` bridge, so
`docker compose` behaves the same from any worktree and the auto-assigned bridge subnet can't
collide with a LAN host.

The mutation gate runs [mutago](https://github.com/quality-gates/mutago) and scopes to the current
branch's diff against `origin/main` by default (`--git-diff-lines`), so it only mutates the lines a
change touched. A diff-scoped run also narrows its *targets* to the packages holding changed `.go`
files rather than enumerating `./...`: changed lines are a subset of changed files, which are a
subset of changed packages, so the mutant set is provably identical while the enumeration pass — the
memory peak of a run, since mutago loads and instruments every target package — shrinks to what the
branch touched. A derivation that resolves to nothing (no Go changes) falls back to `./...`, so a run
never starts with no targets. The wrapper provisions the pinned mutago itself (`go install ...@v2.7.7` into a
throwaway GOBIN, run directly), so the gate needs no mutago on `PATH` and does not touch `go.mod`;
`make tools-dev` still installs mutago for ad-hoc use. Stable, invocation-independent policy lives in
the committed `.mutago.yml` (passed via `--config`); the per-run and load-bearing flags stay on the
command line. The base is handed to mutago as the merge-base commit with `HEAD`, so it works from
a linked git worktree and tolerates a local base ref that has drifted from the remote. The gate is
`--fail-on-escaped`: a covered mutant that survives (asserts nothing) fails it, while `--coverage`
keeps uncovered lines out of the escaped set — the zero-survivor-on-covered-code contract. An
errored mutant — usually one that timed out — fails the gate as well. mutago does not gate these
itself (its MSI arithmetic scores an error as a kill), so a hung mutant would pass silently; the
wrapper therefore reads `report.json` after a gate run and fails on `stats.errorCount > 0`, naming
each errored mutant's file, line and mutator. An errored mutant is unverified, not killed. That check
is what allows a tight `--timeout-coefficient` (5, a multiplier of the instrumented baseline): raise
it with `IQ_MUTATION_TIMEOUT_COEFFICIENT` (a positive integer) when a package's suite is legitimately
slow, rather than letting a slow mutant vanish into an ungated bucket. The check is skipped in the
non-gating modes (`IQ_MUTATION_UPDATE_BASELINE=1`, `IQ_MUTATION_MUTANT=<id>`). Workers stay serial by
default; `IQ_MUTATION_WORKERS` (a positive integer) raises them, which is worth doing only when the
run is already memory-bounded — inside a `systemd-run --user --scope -p MemoryHigh=10G` unit, 2–3 is
the useful range. A genuine equivalent mutant that cannot be killed is accepted into
`mutago-baseline.json` (committed) with `IQ_MUTATION_UPDATE_BASELINE=1`, after which only *new*
escapes fail — the baseline uses line-number-independent IDs so it survives refactors. mutago
*replaces* the baseline with the current run's survivors, so under the default diff scoping it would
silently drop every accepted entry outside the diff; the wrapper snapshots the committed file and
merges it back, making an update a pure append and printing the ids it accepted. Strengthen the tests
first — the update run reruns the same mutants, so it doubles as the verification — then check the
printed ids against the justifications in `mutago-baseline.notes.md` before committing. An id you did
not expect is a prompt to re-verify it with `IQ_MUTATION_MUTANT=<id>` (order-dependent escapes are
flaky-killable), not to commit it. Override the
base ref with `IQ_MUTATION_BASE` (set it empty for a full-module scan), or pass a package path (e.g.
`bash scripts/mutation-gate.sh ./cmd`) for a full scan of that package (a path drops the diff-scoping
flags). Every run also writes `mutago-agentic.json` (gitignored) with LLM-consumable data for each
escaped mutant; `IQ_MUTATION_MUTANT=<id>` re-runs a single mutant by that stable id (a fast
diagnostic to re-verify one survivor or probe an order-dependent escape for flakiness — point it at
the mutant's package). Accepted baseline entries carry a one-line equivalence justification in the
committed `mutago-baseline.notes.md`. `IQ_MUTATION_DRYRUN=1` is a mutant-count preview whose cost
depends on the form: a package-arg dry run is an instant count that runs no tests, while a
diff-scoped or `./...` dry run first runs the `--coverage` instrumented test pass (whole-target,
memory-heavy) before counting. Scope dry runs to one package and never run one alongside a live gate.

The capability gate runs [capslock](https://github.com/google/capslock) and answers what the
dependency tree can actually *do* — the question `govulncheck` (is a dependency
known-vulnerable), `osv-scanner` and `gitleaks` (did we leak a secret) do not ask. It compares
the module's `(package, capability)` set against the committed `capslock-baseline.json` and
lets capslock exit-code-enforce: 0 when the set matches, 1 on drift in *either* direction, 2 on
a run error, which the wrapper keeps distinct. The wrapper provisions the pinned capslock
itself (`go install ...@v0.3.2` into a throwaway GOBIN, run directly), so the gate needs no
capslock on `PATH` and does not touch `go.mod`; `make tools-dev` installs it for ad-hoc use.
Because the analysis costs ~7.3 GB peak RSS and ~39 s, the gate is conditional: it runs only
when `go.mod` or `go.sum` differ from the merge-base with the base ref (`IQ_CAPS_BASE`, default
`origin/main`) and otherwise prints the reason and exits 0, so a docs-only or code-only branch
pays nothing. `IQ_CAPS_FORCE=1` runs it regardless. Two limits are accepted deliberately:
package granularity compares capability *sets*, so a package that already holds a capability
can gain new call paths into it invisibly (the tree is saturated — `drivers/redis` alone
carries `ARBITRARY_EXECUTION`), and the comparison is bidirectional, so a benign dependency
bump that *drops* a capability fails the gate too. Both are resolved the same way: read the
call paths capslock prints, then record the new set with `IQ_CAPS_UPDATE_BASELINE=1` (which
regenerates the baseline, trimmed to the `capabilityInfo` array with `jq`, and prints every row
gained or lost) and justify each new `EXEC` / `ARBITRARY_EXECUTION` / `MODIFY_SYSTEM_STATE` /
`SYSTEM_CALLS` row in the committed `capslock-baseline.notes.md` — which also records the known
false positives, so a row like `internal/render` → `NETWORK` (interface dispatch through
`io.Writer`) is not re-litigated. The baseline is linux-only; `IQ_CAPS_GOOS=linux|darwin|windows`
re-runs the analysis for another shipped target as a review aid at driver admission and is
*expected* to report differences, so read it as evidence rather than as a verdict (a value
outside that whitelist is a hard stop, and a non-linux run may not regenerate the baseline).
A Go toolchain bump churns the baseline tree-wide, since capability rows include stdlib-derived
paths, and may require bumping the pinned capslock version alongside the regeneration.

Quality gates are local and layered — the project uses no CI service. `make check` is the fast,
offline pre-commit gate (format, `go vet`, `go build`, `golangci-lint`, dead code via
`deadcode`, and `go test -short` with a coverage report). If lint reports issues in
`../<worktree>/...` paths — golangci-lint's cache outliving a worktree you have since removed —
`make check` clears the cache and retries once, so that failure heals itself instead of blocking
on findings from files this tree does not contain. `make cover` runs the full
container-backed suite and enforces a coverage floor (`IQ_COVER_MIN`, default 80;
`IQ_COVER_SHORT=1` for a fast report-only run). `make security` sweeps dependencies and secrets
(`govulncheck`, `osv-scanner`, `gitleaks`) and writes SBOMs to `dist/`; gosec runs as the Go SAST
inside `golangci-lint run`. `bash scripts/capabilities.sh` (also `make capabilities`) is the
capability-drift gate, which runs only when the dependency graph moved. `bash
scripts/mutation-gate.sh` (also `make mutation`) is the mutation gate. `make ci` runs check,
cover, security, capabilities, and the mutation gate together — the full pre-merge gate; the
capability step sits between security and mutation so its analysis never overlaps the mutation
gate's memory. Because mutago reruns the suite per mutant, `make ci` is the slowest target; start a shared
stack (`docker compose up -d --wait`) first so the containers are reused. Install the toolchain once
with `make tools-dev`.
