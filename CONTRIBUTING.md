# Contributing

## Requirements

- Go 1.27.0+ (matches `go.mod`)
- Docker: integration tests and the local stack; not needed for `go test -short`
- [gofumpt](https://github.com/mvdan/gofumpt), [goimports](https://pkg.go.dev/golang.org/x/tools/cmd/goimports),
  and [golangci-lint](https://golangci-lint.run) v2.13+ (earlier releases bundle a
  staticcheck that panics on Go 1.27 syntax) on `PATH`: `make check` runs them,
  and `make tools-dev` does not install them
- [uv](https://docs.astral.sh/uv/): the docs site and `make security` (which runs
  zizmor through `uvx`); not needed to build or use `iq`

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
make security       # govulncheck + osv-scanner + gitleaks (tree + git history) + zizmor, SBOMs to dist/
make capabilities   # capslock capability drift (runs only when go.mod/go.sum moved)
make mutation       # mutago mutation gate over the branch diff
make ci             # all of the above, in order; start the compose stack first
make sbom           # SPDX + CycloneDX SBOMs only
```

### make check

Format (`gofumpt` + `goimports`), `go vet`, `go build`, `golangci-lint`
(gosec included), `deadcode`, the demo stamp gate ([Recorded demo](#recorded-demo)),
the mutation verdict tests (`bash scripts/test/mutation-verdict.sh`: fixture shards
from `scripts/test/mutation-verdict-fixtures.py` through `scripts/mutation-verdict.sh`,
the parse, pack and rate steps of `scripts/mutation-plan.sh` on prepared dry runs
(`--pack`, `--rate`, `--stale`), and `scripts/mutation-fingerprint.sh` in a small copy of the
repository; no network, no container, no mutago run), and `go test -short` with a coverage report. Lint findings in `../<worktree>/...`
paths are a stale cache from a removed worktree; check clears the cache and
retries once.

### make cover

Full container-backed suite with `-coverpkg=./...`; fails below
`IQ_COVER_MIN` (default 80). `IQ_COVER_SHORT=1` runs a fast report-only pass.
CI splits the run across runners with two more variables. `IQ_COVER_PKGS` (a
space-separated package list) tests only those packages and writes the partial
profile to `coverage.out`, with no report and no floor. `IQ_COVER_MERGE` (a
space-separated list of partial profiles) runs no tests: it joins the profiles
into `coverage.out`, then gives the report and applies the floor. The join gives
the same numbers as one full run, because every partial profile comes from
`-coverpkg=./...`. Locally, `make cover` still runs the whole suite serially.

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
(unverified, not killed). One exception: a full scan of `./cmd` passes on a
covered-code MSI floor, a literal in the wrapper, because `cmd` holds the
composition root and the presentation code and its critical paths are moving
under `internal/`; every other target and every diff-scoped run stays
zero-survivor. A genuine equivalent is accepted into
`mutago-baseline.json` with a justification in `mutago-baseline.notes.md`.
Run with the integration services up.

- `IQ_MUTATION_BASE`: base ref; empty for a full-module scan; a package arg
  (`bash scripts/mutation-gate.sh ./cmd`) full-scans that package
- `IQ_MUTATION_WORKERS`: parallel mutants (default 1; raise to 2-3 only when
  the run is already memory-bounded, e.g. inside a systemd-run MemoryHigh unit,
  and only with every `IQ_<DRIVER>_URL` unset: parallel suites on one shared
  backend fail on each other's data and mutago scores that as a kill, so the
  wrapper refuses the combination)
- `IQ_MUTATION_TIMEOUT_COEFFICIENT`: per-mutant timeout multiplier (default
  5; raise it for a legitimately slow package instead of letting mutants time out)
- `IQ_MUTATION_UPDATE_BASELINE=1`: accept equivalents (append-only)
- `IQ_MUTATION_MUTANT=<id>`: re-run one mutant as a diagnostic; it can report
  a false KILLED, so do not confirm a kill with it (see the hardening steps)
- `IQ_MUTATION_DRYRUN=1`: mutant-count preview; scope it to one package
- `IQ_MUTATION_MUTATORS="a/b c/d"`: shard mode, only these mutators (names from
  `mutago --list-mutators`); needs a package or file argument and runs with no
  gate flag, because the merge of all shards is the gate (see Continuous
  integration). A file argument (`internal/numfmt/decimal.go`) mutates that one
  file; the wrapper makes it absolute, since a `./`-prefixed file target gives
  other mutant ids (mutago#248)
- `IQ_MUTATION_INSTALL_DIR=<dir>`: only install the pinned mutago into `<dir>`
  (three attempts with backoff) and print the binary path
- `IQ_MUTATION_MUTAGO_BIN=<path>`: use that installed binary; accepted only for
  an executable file built from the pinned version (`go version -m`)
- `IQ_MUTATION_FULL=1`: `scripts/mutation-plan.sh` plans every package, also
  those whose stored result is current
- `IQ_MUTATION_STATE_REMOTE=<uri>`: `scripts/mutation-state.sh` reads and writes
  this remote instead of the GitHub one (a local bare repository for a test)
- `IQ_MUTATION_MERGE_SCRIPT=<path>`: `scripts/mutation-verdict.sh` calls this
  script in place of `scripts/mutation-merge.sh` (the fixture tests give a
  corrupt merge result with it)

Hardening a package that has never had a full scan is a different job from the
per-change gate, and the cost model decides the method: mutago reruns the whole
package suite per covered mutant, so a full scan costs `mutants x suite
duration` and is the expensive step, not the fixing. Work it in this order.

1. Take the escape list from the package's `deep-mutate` shards rather than
   enumerating locally: download its `mutation-<slug>-<shard>` artifacts (for
   example `mutation-drivers-couchbase-3`) and read the
   `cells/NNN/mutago-agentic.json` files, which list every escaped mutant with
   the stable id `IQ_MUTATION_MUTANT` takes, and, more usefully, the diff of
   each one. The artifacts upload also when a shard fails. One edit can appear
   in two cells under two mutators and so two ids; either id matches the
   baseline. A job log alone carries the diffs without the ids, in which case
   replay each logged diff against the source and run the suite to reproduce
   the list.
2. Fix and verify one mutant at a time by ground truth: apply the mutant's own
   `diff` field with `git apply`, run the package suite, then restore the file.
   That costs one suite run, about 25 s for `cmd`. Do NOT use
   `IQ_MUTATION_MUTANT=<id>` to confirm a kill: measured on 2026-09-05, the
   diagnostic reported KILLED for three mutants that stayed alive when the same
   mutation was applied by hand, and an id only resolves under the same
   enumeration set that produced it (a scan run with `--match` gives ids that a
   plain package run cannot find). Keep the diagnostic for probing whether an
   order-dependent escape is flaky.
3. Close with exactly two full scans: `IQ_MUTATION_UPDATE_BASELINE=1`, which
   doubles as the verification and prints exactly the survivors to justify, then
   a plain run for the verdict. The closing scan is also what catches a new test
   slowing the suite enough to push other mutants into timeout, which a
   single-mutant run cannot see.

Point a container-backed package at a running service (`IQ_<DRIVER>_URL`, see
`.env.example`) before any of this, or every mutant pays for a fresh container.

### Commit identity (`make hooks`)

```bash
make hooks   # per checkout, worktrees included
```

Points `core.hooksPath` at the committed `.githooks`. Both hooks compare a
commit's address against the `user.email` configured in a config file, read
with `--show-origin` so a `git -c user.email=...` cannot move both sides of the
comparison at once. `pre-commit` resolves the identity the commit will carry,
so it catches a `--author`, a `-c` override and a `GIT_AUTHOR_EMAIL` alike;
`pre-push` re-checks author and committer across everything being published,
which is what catches commits the hook never saw: a `--no-verify`, a tool that
bypasses hooks, a replayed rebase, or history written before the hooks existed.
The address a repository publishes is the one in its config, and removing a
different one from published history costs a force-push over every clone. A
deliberate override, such as applying someone else's patch, passes
`--no-verify`.

### make ci

check + cover + security + capabilities + mutation, in that order, the
capability step sits before mutation so their memory peaks never overlap.
Slowest target (mutago reruns the suite per mutant); with one worker start a
shared stack first so the containers are reused.

### Continuous integration

`.github/workflows/ci.yml` runs on the GitHub mirror only (the primary remote has
Actions off; `docs.yml` deploys the site and `release.yml` publishes releases,
both guarded the same way). Every pull request and every push to `main` runs one
job per gate. A pull request branch runs through `pull_request` only, not also
through `push`. The first job, `changes`, decides whether the change touches code.
It sets `code=false` only when every changed file is on a short list that no Go job
reads (Markdown files outside `skills/`, `docs/` outside the man page and the
completions, `.github/` except `ci.yml`, `.agents/`, and the app configs). Then the
Go jobs are skipped, and a skipped job passes its required check, so a docs-only or
workflow-only pull request takes about 2 minutes. Any other file, a change to
`ci.yml` itself, or a diff that fails runs every job. The last job, `ci-ok`, is the
only required check of the `main` ruleset: it needs every per-change job and fails
when one of them failed or was cancelled (a skipped job passes). The per-job checks
cannot be required themselves, because GitHub reports a skipped matrix job under
its bare name. A new per-change job goes into the `needs` list of `ci-ok`. The jobs
per gate:
`lint` (format, vet, golangci-lint), `test` (`go test -short -shuffle=on` on
Linux, macOS and Windows), `coverage` (four `coverage (<group>)` jobs each test a
group of packages on their own runner, then `coverage` joins the partial profiles,
makes sure that each package is in exactly one group, applies the floor, and does
a reporting-only Codecov upload, `CODECOV_TOKEN` secret), `e2e` (redis pass, then
the mongo live flow), `cross` (CGO-off builds for the three shipped targets),
`vuln` (govulncheck), `osv` (OSV plus the permissive license allowlist), `sbom`
(syft), `deadcode`, `secrets` (gitleaks, tree and history), `capabilities`
(`scripts/capabilities.sh` against the PR base), `mutate-diff`
(`scripts/mutation-gate.sh` against the PR base), `workflows` (zizmor over
`.github/`) and `docs` (site build). The
weekly `deep-*` jobs (Mondays, or `workflow_dispatch`: Actions, CI, Run
workflow) re-run the vulnerability, secret and zizmor workflow scans against fresh data
(`deep-scan`) and run an incremental, sharded mutation scan. The stored result of
each package lives on the `badges` branch (`state/<slug>.json`, next to the
badge endpoint `mutation.json`; `scripts/mutation-state.sh` reads and writes it,
with the token in an HTTP header from the environment, never in a URI or an
argument, and three attempts for each network step). Scheduled and manual runs
of one ref share one concurrency group, so two scans of that ref never overlap;
a running scan is not cancelled, and a dispatch on a branch never replaces a
waiting scan of `main`.

`deep-plan` (`scripts/mutation-plan.sh`) plans only the packages whose
fingerprint changed or whose last scan failed, or every package on a forced run: the `full` input, or a
scheduled run in the first seven days of the month. The fingerprint
(`scripts/mutation-fingerprint.sh`) covers the package's own files, its tests
and testdata, its baseline entries, `go.mod`, `go.sum`, `.mutago.yml`, the
mutation scripts (gate, plan, shard, merge, verdict, summary) and the Go
version. It does not cover the backend images or other packages; the monthly
full run catches that drift. The `packages` input (space-separated, for example
`./internal/numfmt`) limits the plan for a manual test; the plan removes a
trailing slash and a duplicate, and stops on a path that is not a module
package. The unit of work is a (file, mutator) cell from a dry run; the sum of
the cells must equal the dry run total. The plan packs cells into shards of
about 150 min at the measured seconds per mutant (a package with no stored
result uses its starting rate from the table in the script, else 180 s with a
backend and 15 s without; `bash scripts/mutation-plan.sh --rate <badges-dir>
<package>` prints the rate). A cell over 150 min gets its own shard and a
warning; a cell over 165 min stops the plan, because its job cannot finish. The
full plan goes to the `scan-plan` artifact; the job output keeps only the
package, slug and shard numbers that the matrix needs.

`deep-mutate` runs one shard per runner (`scripts/mutation-shard.sh`: it reads
its cells from the plan artifact, runs one ungated wrapper run per cell,
installs mutago once, and removes the value of each `IQ_*_URL` from the cell
logs; eight runners at a time; a driver job starts its compose service(s) once
and sets the `IQ_*_URL` override, so per-mutant test runs reuse the running
service the way the local per-driver recipe does, couchbase provisioned by
`scripts/seed-couchbase.sh` with `IQ_SEED_BUCKET` and `IQ_SEED_DATA=0`). The
shard artifact uploads also on failure and replaces the artifact of an earlier
attempt of the same job.

`deep-badge` (`scripts/mutation-verdict.sh`, read-only) runs when the shards
finished, also when some failed. It judges each package on its own: a missing,
duplicate, failed or timed-out shard, a report of another commit, mutago or Go
version or other `.mutago.yml` or baseline hashes, a cell report with no
mutants or with more mutants than the plan, or a merge result that is not valid
fails that package, and the package gets a failure marker in place of its
state (`status: "failed"`, no summary), so the next plan scans it again and no
badge is published while the marker exists. An unreadable report or a report
of a package outside the plan fails the package of its artifact directory
(`mutation-<slug>-<shard>`); only when no package can be found does the verdict
stop and write nothing. It merges the
shards of each package with `scripts/mutation-merge.sh` (each edit once, by
checksum; an escape is new only when none of its ids is in the baseline; a
kill in one shard and an escape in another prints a warning), fails a package
on a new escape or an errored mutant, and holds `./cmd` to the wrapper's floor
on its merged score. The score is killed / (killed + escaped) on covered code,
the same formula in `scripts/mutation-summary.sh`. It writes the state of each
package that passed and removes the state of a package the module no longer
has; the job is red when any package failed. It writes a new badge only when
no package failed and every package has current state, else the previous badge
stays. `deep-publish`, the only job with write access, pushes the new state
and badge from `main` only.

Accepted risk: anyone who can push the `badges` branch can forge the stored
state (a result or a fingerprint) and so skip a package's scan until the next
monthly full run. The branch cannot be protected, because CI force-pushes it.
Parallelism is across runners only: one container
at a time per machine is what keeps the gate's timeouts honest.

Containers run one at a time per runner in CI: the `coverage (<group>)` and `mutate-diff` jobs set
no `IQ_*_URL` except HBase's, so each driver's `TestMain` provisions its own testcontainer,
`GOFLAGS=-p=1` serialises the package test binaries (coverage and mutation),
the e2e job brings up a single compose service per pass, and a `deep-mutate`
shard starts only the compose services of its own package. HBase has no testcontainers path, so the
`coverage (cassandra-neo4j-hbase)` job and the hbase `deep-mutate` job start its compose service (host
networking) and set `IQ_HBASE_URL`; everywhere else it skips, as it does locally
without `IQ_HBASE_URL`. The `test` job runs without
`-race` until the order-dependent data race in `cmd` (`color.NoColor`) is fixed.
Tool versions are pinned in the workflow's `env` block; keep them in sync with
the Makefile and `scripts/`.

Posture and upkeep around the pipeline, all on GitHub:

- `.github/workflows/scorecard.yml` runs the OpenSSF Scorecard on pushes to
  `main` and weekly, uploads the SARIF to the Security tab and publishes the
  score behind the README badge. Every action in every workflow is pinned by
  commit SHA with the release in a trailing comment; keep it that way (a tag
  pin is a Scorecard deduction and a supply-chain gap).
- [zizmor](https://docs.zizmor.sh) audits the workflow files themselves: unpinned
  or impostor actions, credentials the checkout leaves on disk, cache poisoning on
  the release paths, and template injection into a `run` block. The version is
  pinned as `ZIZMOR_VERSION` in `ci.yml` and `scripts/security.sh`, and Renovate
  tracks both. `make security` runs it locally, the `workflows` job runs it on
  every push and pull request, and `deep-scan` re-runs it weekly against fresh
  advisory data. There is no config file, so an accepted finding is an inline
  `# zizmor: ignore[<audit>]` comment with a reason on the offending line. Three
  exist. In `ci.yml`, the `capabilities` and `mutate-diff` jobs keep the
  checkout credential because they fetch the pull-request base branch. In
  `devin-review.yml`, the `pull_request_target` trigger is accepted because the
  job checks out nothing and puts no pull request data into a shell command.
- `renovate.json` drives Renovate (the Mend GitHub App): one grouped PR a week
  for minor and patch bumps, one PR per major, Go toolchain bumps on their own,
  action digests refreshed, and the tool versions in `ci.yml`, the Makefile
  and `scripts/` tracked through custom regex managers. A human merges; a PR
  that moves `go.mod` runs `make capabilities`, which is the review AGENTS.md
  asks for on a dependency change.
- `osv-scanner.toml` carries the license overrides the CI `osv` job needs:
  the job gates every dependency against the permissive allowlist in `ci.yml`
  (`make security` scans for vulnerabilities only), and deps.dev reports a
  module's documentation license (CC-BY) or UNKNOWN for a few modules whose
  code is permissive; each override names the LICENSE file it was read from.
  A new violation means reading the module's LICENSE, then either an override
  with a reason or a different dependency.
- `.coderabbit.yaml` configures CodeRabbit's pull-request review, with
  per-path instructions distilled from AGENTS.md; it reviews, it never
  approves or merges.
- `.github/workflows/devin-review.yml` posts a link to the Devin Review
  (`devinreview.com`) on each new pull request. The link gives Devin no access
  to the repository, and a human opens it. Automatic Devin reviews need the
  Devin GitHub App and paid credits, and are not used.
- `socket.yml` configures the Socket GitHub App: on every pull request that
  moves `go.mod` or `go.sum` it reports what the new module versions do
  (install scripts, obfuscation, typosquats, maintainer changes), the
  complement of `make capabilities`, which reports what our own dependency
  tree can do; it comments, it never blocks a merge.
- `.github/workflows/install-smoke.yml` verifies a published release the way
  a user installs it, on fresh hosted runners with no checkout: the Homebrew
  cask on macOS, the Scoop manifest on Windows, `install.sh`, the `.deb` and
  `go install` on Linux, each ending in `iq --version` equal to the tag and a
  query against a registered dump. It runs when a Release is published and on
  demand (Actions, Install smoke, Run workflow, with an optional tag); it
  needs the repo, the tap and the bucket to be public.
- `SECURITY.md` is the vulnerability-reporting policy: GitHub private
  vulnerability reporting only.

## Docs

```bash
make docs        # build the Zensical site into docs/site/ (needs uv)
make docs-serve  # serve on 0.0.0.0:8000
```

Pages under `docs/docs/` are hand-maintained; nothing regenerates them from
the README. Both targets first write the gitignored `docs/docs/llms-full.txt`,
every nav page concatenated in order, published at
`https://zsltg.github.io/iq/llms-full.txt` so an agent reads the whole manual in
one fetch.

### Recorded demo

```bash
make demo         # re-record the README demo if the code it shows has changed
make demo-record  # record unconditionally (FORCE=1 make demo does the same)
make demo-check   # fail when the demo shows code that has since moved
```

`docs/docs/assets/demo.svg` is generated, not hand-captured: `expect` types the
commands, `asciinema` (3.x or newer) records the session, `asciinema convert`
rewrites the cast as asciicast v2, and `termsvg` (pinned in `scripts/demo/demo.mk`,
provisioned by the target itself into a throwaway GOBIN via `go install`, no PATH
dependency, no `go.mod` change) renders it as an animated SVG: vector text stepped
through by a CSS keyframe animation, so it plays anywhere an `<img>` does and
scales to the README and docs columns without blur. `scripts/demo/postrender.py`
then embeds a ~3.5 KB subset of Source Code Pro (OFL-1.1, `scripts/demo/demo-font.*`)
so every browser draws the text on termsvg's 12px grid and the cursor stays on the
last character (termsvg only names a font stack, and a fontconfig that resolves it
to a font with another advance makes the cursor drift); it refuses a recording
that types a glyph outside the subset, and `make demo-font` regenerates the subset
from the pinned upstream release (needs the network and `uvx`) after the glyph
list in `scripts/demo/font.sh` is widened. `make demo-record` names whichever tool
is missing or too old, brings up the `mongo` compose service, and seeds it;
recording is manual, since CI has neither expect nor asciinema.

`docs/demo.stamp` is the hash of every source the SVG was recorded from.
`make demo-check` re-hashes them and fails, naming the files that moved, so a
stale recording is a failed gate rather than something noticed months later; it
needs git and `sha256sum` only, which is why it runs in `make check` and in CI.
Never hand-edit the stamp, and commit it with the SVG in the same change. Details
in [scripts/demo/README.md](scripts/demo/README.md).

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
  `feat/`, `fix/`, `chore/`, `test/`, `ci/`, `docs/`; one atomic task per branch.
- Open a pull request against `main` and fill in the template. The maintainer
  merges it fast-forward after the required checks pass, so keep the branch
  rebased on `main`. The merge button is not used.
- [Conventional Commits](https://www.conventionalcommits.org/):
  `<type>(<scope>): <description>`, lowercase imperative.
- If AI contributed to a commit, end its message with a `Co-Authored-By:`
  trailer naming the model and version, e.g.
  `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

## AI-assisted contributions

`iq` itself is built with AI assistance, so AI-assisted pull requests are
welcome. They meet the same bar as any other change, and three rules apply:

- Disclose it. Every commit that an AI tool helped with ends with the
  `Co-Authored-By:` trailer above, and the pull request template asks for it.
- Run the gates locally before you open the pull request: `make check`,
  `make cover`, `make security`, `make e2e`, and `scripts/mutation-gate.sh`
  (plus `scripts/capabilities.sh` when `go.mod` or `go.sum` changed). The
  mutation gate is diff-scoped by default: it mutates only the lines you
  changed, and a full package scan is not required. CI runs the gates again,
  but a pull request that fails them locally wastes review time.
- Own the diff. You can explain every line, why the tests prove it, and why a
  surviving mutant is a real equivalent before it enters the baseline.

A pull request that breaks one of these rules is closed with a pointer to this
section, not reviewed line by line.

Until the mutago v2.10.16 re-baseline is done, some accepted escapes in
`mutago-baseline.json` carry IDs from the older version. The diff-scoped gate can
then fail on a line you changed, even though the escape there was accepted
before. Say so in the pull request, and the maintainer re-checks and handles the
entry. This note goes away with the re-baseline.

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
