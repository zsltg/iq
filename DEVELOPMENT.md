# Development

The full developer guide: the local stack, every quality gate and its
settings, continuous integration, the docs and the demo recording, the
toolchain, releasing, and the architecture. It is the catalogue of developer
commands. To contribute a change, start with [CONTRIBUTING.md](CONTRIBUTING.md).

## Build

```bash
go build -o iq .   # plain build
make build         # embeds version, commit, and build date via ldflags
./iq version       # check the result
```

A plain `go build` still reports a version recovered from Go's embedded build
info. `iq version` prints the version, commit, build date, and Go version;
`iq --version` prints the bare version alone, so scripts can read it without
parsing: `0.37.0` for a release build (no `v`), `v0.37.0` for a `go install`
build (the module version), and `dev+<commit>` for an untagged build.

## Test backends

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

## Benchmarks

```bash
make bench   # decode, filter, raw-byte prefilter, number-conversion, and dump I/O benchmarks
```

No containers. To compare a change, run it before and after, and pair the two
runs with `benchstat`.

## Local stack

```bash
docker compose up -d --wait      # all backends on their published ports
bash scripts/seed-<backend>.sh   # example data: redis, mongo, cassandra, dynamodb,
                                 # hbase, couchdb, couchbase, neo4j, elasticsearch, opensearch
docker compose down
```

The stack runs under a fixed project name (`iq`) on a pinned `10.100.0.0/24`
bridge, so compose behaves the same from any worktree. If `10.100.0.0/24`
overlaps a network on your machine (a LAN or a VPN), change the subnet in
`compose.yaml`. OpenSearch is published on 9201 to avoid
Elasticsearch on 9200, and HBase uses host networking (see `compose.yaml`).

`compose.yaml` is the one source of the test images. Each image is pinned to a
version tag plus the digest of its multi-arch index, for example
`redis:8.10.2@sha256:...`. HBase is the exception. Its image has no patch
tags and no multi-arch index, so its digest names a linux/amd64 manifest. The Go
tests that start a container read the image through `internal/testimage`, so the
tests and the local stack run the same images. To change a version, change the tag and the digest in `compose.yaml`
together. Run `docker buildx imagetools inspect <image>` to read the index
digest of a tag.

## Quality gates

Local and layered: the quality gates run on your machine first, and
`.github/workflows/ci.yml` runs the same set on the GitHub mirror (see
[Continuous integration](#continuous-integration)). The full doctrine binds in
[AGENTS.md](AGENTS.md). The gates to run before a pull request are listed in
[CONTRIBUTING.md](CONTRIBUTING.md#before-you-open-a-pull-request).

```bash
make check          # fast offline gate: format, vet, build, lint, workflow lint, dead code, short tests
make cover          # full suite + coverage floor
make security       # govulncheck + osv-scanner (vulnerabilities, license allowlist) + gitleaks (tree + git history) + zizmor, SBOMs to dist/
make capabilities   # capslock capability drift (runs only when go.mod/go.sum moved)
make mutation       # mutago mutation gate over the branch diff
make ci             # all of the above, in order; start the compose stack first
make fuzz           # Go native fuzz targets over the untrusted parsers (not part of make ci)
make sbom           # SPDX + CycloneDX SBOMs only
```

### make check

`make check` runs `scripts/check.sh`.

Format (`gofumpt` + `goimports`), `go vet`, `go build`, `golangci-lint`
(gosec included), `actionlint`, `deadcode`, the demo stamp gate ([Recorded demo](#recorded-demo)),
the mutation verdict tests (`bash scripts/test/mutation-verdict.sh`: fixture shards
from `scripts/test/mutation-verdict-fixtures.py` through `scripts/mutation-verdict.sh`,
the parse, pack and rate steps of `scripts/mutation-plan.sh` on prepared dry runs
(`--pack`, `--rate`, `--stale`), and `scripts/mutation-fingerprint.sh` in a small copy of the
repository; no network, no container, no mutago run), the capability wrapper tests
(`bash scripts/test/capabilities.sh`: simulated Go and Capslock commands, needs `jq`,
no network, no container), the go install check (`bash scripts/go-installable.sh`
and its fixture tests in `scripts/test/go-installable.sh`), and `go test -short` with a coverage report. Lint findings in `../<worktree>/...`
paths are a stale cache from a removed worktree; check clears the cache and
retries once. Do not clear the cache unconditionally. A warm lint run takes about
3 seconds, compared with about 65 seconds after a cache clear.

`actionlint` checks the workflow files in `.github/workflows/`, at the version in
`ACTIONLINT_VERSION`. It runs `shellcheck` on the `run:` blocks only when
`shellcheck` is on `PATH`. For an accepted shellcheck finding, add an inline
`# shellcheck disable=SC<number>` line with a reason inside the `run:` block. For a
finding with no inline form, add an `ignore` regex with a reason to
`.github/actionlint.yaml`, scoped to one workflow file.

Run `gofumpt -w .`, then `goimports -w .`, before lint or commit.
The lint configuration extends the v2 defaults, including `staticcheck` and
`unused`. It enables `godot`, `gosec`, `errorlint`, `testifylint`, `bodyclose`,
`noctx`, `misspell`, `modernize`, and `gocognit`. Comments must end with a period.
The security analyzer `gosec` excludes `_test.go` fixtures.
The linter `gocognit` fails a function with a cognitive complexity above 30.
Exclusions in `.golangci.yml` keep the functions that scored above 30 when the linter was added.
Remove an exclusion when a pull request refactors its function below 30.
Never add an exclusion for new code.
The modernizer proposes current Go forms, including `slices`, `maps`, `range n`,
`strings.SplitSeq`, and `errors.AsType`. Use `golangci-lint run --fix` for its fixes.
The dead-code command is `deadcode -test ./...`, which analyzes the whole program.
The go install check fails when `go.mod` has a `replace` or an `exclude` directive.
`go install github.com/zsltg/iq@<version>` refuses such a module, but a build from
the checkout accepts it. The CI `lint` job runs the same check.
Fix every reported issue before committing.

### Security gate (`scripts/security.sh`)

Before an authorized dependency addition or upgrade, run `make security`, or run
`govulncheck` alone with the pinned version:
`. scripts/tool-versions.env && go run golang.org/x/vuln/cmd/govulncheck@"$GOVULNCHECK_VERSION" ./...`.
`make security` needs network access. It runs these tools through `go run` at the
versions in `scripts/tool-versions.env`, so the first run fetches them from the
module proxy:

- `govulncheck` finds vulnerabilities in reachable Go code.
- `osv-scanner` scans vulnerabilities and every Go module against
  `scripts/license-allowlist.txt`. The CI `osv` job reads the same allowlist.
- `gitleaks` scans the working tree and Git history for secrets.
- `zizmor` audits `.github/` through `uvx`, with the version pinned by
  `ZIZMOR_VERSION` in `scripts/tool-versions.env`. Without `GH_TOKEN` or `GITHUB_TOKEN`, it runs only offline audits.
- `syft` writes a software bill of materials (SBOM) to `dist/`. The SBOM lists the Go module graph only.
  It runs `make sbom`, which holds the syft options.

For an accepted zizmor finding, add an inline `# zizmor: ignore[...]` with a reason.
Run `make sbom` when only the SBOM artifacts are needed.

### make cover

`make cover` runs `scripts/coverage.sh`.

Full container-backed suite with `-coverpkg=./...`; fails below
`IQ_COVER_MIN` (default 80). `IQ_COVER_SHORT=1` runs a fast report-only pass.
CI splits the run across runners with two more variables. `IQ_COVER_PKGS` (a
space-separated package list) tests only those packages and writes the partial
profile to `coverage.out`, with no report and no floor. `IQ_COVER_MERGE` (a
space-separated list of partial profiles) runs no tests: it joins the profiles
into `coverage.out`, then gives the report and applies the floor. The join gives
the same numbers as one full run, because every partial profile comes from
`-coverpkg=./...`. Locally, `make cover` still runs the whole suite serially.
Cross-package coverage counts, and packages without their own tests still enter
the denominator. Short mode understates driver coverage and does not satisfy
the full coverage gate.

### End-to-end validation

Full `make cover` includes the `e2e` package and its binary build.
`make e2e` runs that package alone. Both commands test the binary through `os/exec`.
Run either command after relevant changes.
On the same tree with the same backend configuration, a passing full `make cover` needs no separate `make e2e` run.

The optional live flows need `IQ_REDIS_URL` and `IQ_MONGO_URL` to point at running backends.
Each flow skips when its variable is unset. There is no localhost fallback.
For live coverage, configure both variables as described in [Test backends](#test-backends) before either command.
Report any skipped live flows. The entire `e2e` package skips under `-short`.

### Capability gate (`scripts/capabilities.sh`)

[Capslock](https://github.com/google/capslock) reports what the dependency tree can do.
The gate compares that report against the committed `capslock-baseline.json`.
It runs only when `go.mod` or `go.sum` differ from the merge-base with `IQ_CAPS_BASE`.
On drift, read the call paths before recording a new baseline with `IQ_CAPS_UPDATE_BASELINE=1`.

The wrapper installs `github.com/google/capslock/cmd/capslock` at the version of
`CAPSLOCK_VERSION` in `scripts/capabilities.sh` into a temporary `GOBIN`. It runs the binary directly, with no `PATH` or `go.mod` change.
The wrapper exits 0 when there is no drift. It exits 1 for drift and run errors,
including Capslock status 2. The run-error message states that no capability
verdict was reached. A build failure is not a capability regression.

The command uses `-granularity=package -output=compare capslock-baseline.json`
over `./...`. Capslock reads no test files, so `e2e` and testcontainers are outside
the graph. The run takes about 39 seconds and peaks at about 7.3 GB of memory.
`make ci` runs it before mutation so those memory peaks do not overlap.
Without a dependency change, the wrapper prints the reason and exits 0.
First-party execution and network additions remain subject to `gosec` in `make check`.

Package-level comparison detects capability sets, not new paths to an existing
capability. For example, `drivers/redis` already holds `ARBITRARY_EXECUTION`.
Function-level comparison costs about 37 times as many rows and changes on renames.
The comparison also fails when a dependency drops a capability.
Read the printed call paths for both gains and losses.

An update uses `-omit_paths -output=json` and `jq` to keep only the
`capabilityInfo` array that comparison reads. It prints every row gained or lost.
Justify each new `EXEC`, `ARBITRARY_EXECUTION`, `MODIFY_SYSTEM_STATE`, or
`SYSTEM_CALLS` row in `capslock-baseline.notes.md`.
Read the recorded false positives before reopening them. For example,
`internal/render` reports `NETWORK` through `io.Writer` interface dispatch.
Other rows need no individual note under this policy.

The committed baseline is Linux-only. The wrapper rejects updates for non-Linux targets.
When `IQ_CAPS_GOOS` is unset, the update guard reads the target from `go env GOOS`.
Set `IQ_CAPS_GOOS=linux` to update the baseline from another host.
`IQ_CAPS_GOOS` accepts only `linux`, `darwin`, or `windows` and rejects other values.
A Darwin or Windows run provides review evidence and is expected to differ.
A Go toolchain bump can change the baseline across the tree and require a new
pinned capslock version when the baseline is regenerated.

- `IQ_CAPS_BASE`: base ref (default `origin/main`)
- `IQ_CAPS_FORCE=1`: run regardless
- `IQ_CAPS_GOOS=linux|darwin|windows`: review aid for other targets, expected to differ
- `IQ_CAPS_UPDATE_BASELINE=1`: record a new baseline

### Mutation gate (`scripts/mutation-gate.sh`)

[mutago](https://github.com/quality-gates/mutago) over the branch diff vs
`origin/main`, targets narrowed to the changed packages. The contract is zero
new survivors on covered code. Investigate each escaped covered mutant.
Strengthen tests for a real behavior gap. A surviving mutant can also be a
genuine equivalent, which needs the justification below.
An errored or timed-out mutant also fails because it is unverified, not killed.
One exception applies only to a full scan of `./cmd`.
It uses `--min-covered-msi` with the covered-code mutation score (MSI) floor committed in the wrapper.
The package holds composition and presentation code, and its critical paths are
moving under `internal/`. Every other target and every diff-scoped run stays
zero-survivor. A genuine equivalent is accepted into
`mutago-baseline.json` with a justification in `mutago-baseline.notes.md`.

The wrapper installs `github.com/quality-gates/mutago/v2/cmd/mutago` at the version
of `MUTAGO_VERSION` in `scripts/mutation-gate.sh` into a temporary `GOBIN` and runs
that binary directly.
It needs neither a `PATH` entry nor a `go.mod` change.
Stable policy lives in `.mutago.yml`, passed with `--config`.
Required and invocation-specific flags stay on the command line.
`--fail-on-escaped` enforces the verdict, and `--coverage` excludes uncovered lines
from the survivor set. The wrapper returns that verdict.
Mutago counts errors as kills, so the wrapper also reads `report.json`.
It fails when `stats.errorCount > 0` and names each errored mutant.

Diff scope uses the merge-base commit and `--git-diff-lines --git-diff-base`.
It works from linked worktrees and tolerates a local base that moved.
The wrapper narrows enumeration to packages with changed non-test `.go` files.
This preserves the mutant set while reducing memory use during enumeration.
It drops test-only packages when production packages changed.
In particular, `e2e` does not charge its binary rebuild to every mutant.
A test-only diff still enumerates the changed test packages.
A diff without Go files exits successfully with nothing to mutate.
Any other empty scope derivation falls back to `./...`.
A package argument removes diff scope and scans that entire package.

The worker count and timeout coefficient must be positive integers.
The default is `--workers 1 --timeout-coefficient 5`.
For serial scans, set `IQ_<DRIVER>_URL` to reuse a running service and avoid a fresh container for each mutant.
See `.env.example` for backend configuration.

For parallel scans, leave every `IQ_<DRIVER>_URL` unset so supported suites start isolated containers.
Raise workers to 2 or 3 only within an existing memory bound, such as
`systemd-run MemoryHigh=10G`.
Parallel tests against a shared backend corrupt each other's fixtures and produce
false kills. The wrapper rejects that combination.

For HBase integration coverage, use one worker and set `IQ_HBASE_URL` to a running cluster.
Without that variable, HBase integration tests skip. They do not start isolated containers.

Baseline IDs do not depend on line numbers. An update must preserve accepted
entries outside the diff. The wrapper saves the committed baseline and merges
those entries back because mutago replaces its baseline with the run's survivors.
The update is append-only and prints exactly the IDs that it accepts.
Accept an ID only when it matches an examined equivalent and its note.
Investigate unexpected IDs before acceptance. A single-mutant diagnostic cannot
prove a kill, as the hardening procedure below explains.

Every run writes the gitignored `mutago-agentic.json` with escaped-mutant data.
A failed gate prints each new escape ID and writes the gitignored
`mutago-baseline.candidate.json`. That candidate contains the committed baseline
plus the new escapes. Accept only justified equivalents from it.
The CI `mutate-diff` artifact includes those files and `report.json`, so CI
escape analysis needs no local rerun just to obtain their IDs.

A package dry run counts mutants without tests. A diff or `./...` dry run first
runs the whole-target instrumented coverage pass and can use substantial memory.
Scope dry runs to one package. Never run a dry run beside a live mutation gate.

- `IQ_MUTATION_BASE`: base ref; empty for a full-module scan; a package arg
  (`bash scripts/mutation-gate.sh ./cmd`) full-scans that package
- `IQ_MUTATION_WORKERS`: parallel mutants, default 1, subject to the memory and backend constraints above
- `IQ_MUTATION_TIMEOUT_COEFFICIENT`: per-mutant timeout multiplier (default
  5; raise it for a legitimately slow package instead of letting mutants time out)
- `IQ_MUTATION_UPDATE_BASELINE=1`: accept examined equivalents with notes through an append-only update.
  Copy the candidate over the baseline only when every escape is a justified
  equivalent. Kill the other survivors first.
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

Apply the worker and backend rules above to these steps.

### Prove one mutant on CI

The workflow `.github/workflows/mutant-proof.yml` is manual. A hand proof needs the full package suite against a real backend. For the JVM
backends (hbase, cassandra) a local run is too heavy, so this manual workflow
runs the proof on GitHub. It applies the patch of ONE mutant, starts the
backend of the package with `scripts/ci-backend.sh` (the same script that the
`deep-mutate` job calls), and runs `go test -count=1 -p=1` on the package.

The workflow file must be on `main` before `gh workflow run` can dispatch it.
After that, `--ref` picks the branch whose code the run tests.

```sh
git diff -U3 > /tmp/mutant.diff   # or the `diff` field of a mutago-agentic.json entry
gh workflow run mutant-proof.yml --ref <branch> \
  -f package=./drivers/cassandra \
  -f patch="$(base64 -w0 /tmp/mutant.diff)" \
  -f run='^TestScan'                # optional go test -run regex
```

The `diff` fields in `mutago-agentic.json` use `--- Original` and `+++ New`
headers. `git apply` needs `a/<path>` and `b/<path>` headers, so rewrite them
first, with `<path>` the repo-relative path of the file:

```sh
sed -e 's|^--- Original|--- a/<path>|' -e 's|^+++ New|+++ b/<path>|' mutant.diff > fixed.diff
```

A patch from `git diff` needs no change. The run uses `-vet=off`, as mutation
testing does, so a vet finding never reads as a kill.

The inputs:

- `package`: `./cmd`, or `./drivers/<name>`, or `./internal/<name>`.
- `patch`: a unified diff of exactly one non-test `.go` file in that package
  directory, at most 64 KB after decoding.
- `run`: an optional `go test -run` regex. An empty value runs the whole suite.

How to read the verdict:

- Job red, summary `Mutant KILLED`: the suite failed with the mutant applied.
  This proves a kill.
- Job green, summary `Mutant SURVIVED`: the suite passed with the mutant
  applied. This proves an equivalent mutant, or a missing test.
- Job red, summary `Mutant DID NOT COMPILE`: the mutant breaks the build. This
  is not a kill.
- Job red before the test step: an input check failed, read the error line.

The summary names the package, the file and the first changed line. The log
prints the applied `git diff`.

### Fuzz targets (`scripts/fuzz.sh`)

Go native fuzzing over the parsers that read untrusted bytes, one target per
`go test -fuzz` invocation because that is all the flag takes. Each target
bounds its own work (it passes over an oversized input and caps how many
records it drains), so the time budget is honest.
The eight targets live beside package tests in `fuzz_test.go`.
They need no containers or network.

- `internal/jqfmt`: `FuzzFormat`, the printer is idempotent and its output
  re-parses, and the colored output matches the plain one once the ANSI
  escapes are removed.
- `internal/rawpred`: `FuzzMatch`, a filter compiled through `gojq.Parse` and
  `pushdown.Compile` never drops a document the reference evaluator matches,
  and the prepared `Matcher` agrees with the package-level `Match`.
- `drivers/file`: `FuzzOpenReader` over every dump format, and `FuzzCBORRecord`
  over the decode cache codec.
- `internal/query`: `FuzzJSONLRoundTrip`, a typed dump is a fixpoint and both
  readers read it the same.
- `internal/numfmt`: `FuzzConvertNumber`, an integer is exact in every mode.
- `internal/shape`: `FuzzInfer`, inference ignores the item keys and the order
  they arrive in.
- `internal/diff`: `FuzzPatch`, the tree walk and the RFC 6902 patch agree on
  whether two documents differ.

`IQ_FUZZ_TIME` sets the budget per target (default `20s`). The CI `fuzz` job
uses the default on every change that touches code, and the weekly `deep-fuzz`
job runs the same script with `IQ_FUZZ_TIME=5m`.

Seed inputs and every committed crasher run as ordinary subtests under
`go test -short`, so `make check` and `make cover` already cover them. A
crasher is a real bug: Go writes the input to `<pkg>/testdata/fuzz/<Name>/`,
so read it, fix the code at the layer that is responsible, and commit that
file with the fix as the regression seed. Never delete it to make a run pass.

### Commit identity (`make hooks`)

```bash
make hooks   # per checkout, worktrees included
```

Points `core.hooksPath` at the committed `.githooks`. Two hooks compare a
commit's address against the `user.email` configured in a config file, read
with `--show-origin` so a `git -c user.email=...` cannot move both sides of the
comparison at once. `pre-commit` resolves the identity the commit will carry,
so it catches a `--author`, a `-c` override and a `GIT_AUTHOR_EMAIL` alike;
`pre-push` re-checks author and committer across everything being published,
which is what catches commits the hook never saw: a `--no-verify`, a tool that
bypasses hooks, a replayed rebase, or history written before the hooks existed.
`pre-push` skips a commit that a remote-tracking ref already holds, because it is
already published. This lets a squash merge from GitHub, whose committer is
`noreply@github.com`, reach `origin` and the next pull request branch.
The third hook, `commit-msg`, reads the message text. Every email address in a
commit message must be the author or committer address of that same commit, or
match the allowlist: `*@users.noreply.github.com`, `noreply@github.com`,
`noreply@anthropic.com` and `noreply@openai.com`. The rule is in
`scripts/commit-emails.sh`, and the allowlist is in that file only. The first
case lets an outside contributor sign off with their own public address. A
hand-typed `Signed-off-by:` line with another address fails. Do not type an
address into a message: `git commit -s` adds the sign-off with your configured
address. The script reads non-ASCII addresses too. It checks the whole
message file, comment lines and the text after a scissors line included, because
the cleanup mode of git decides what stays in the commit. A foreign address fails
even when git would strip its line. `pre-push` runs the same script over the commits that it publishes, and
the `dco` job runs it over the pull request range. A later commit does not remove an
address from history, so a failure on existing commits needs a rewrite of each
named message: `git commit --amend -s` for the last commit, or a rebase that
edits an older one. A pushed commit then needs a force-push. `bash scripts/test/commit-emails.sh`
tests the script, and `make check` runs it.
The address a repository publishes is the one in its config, and removing a
different one from published history costs a force-push over every clone. A
deliberate override, such as applying someone else's patch, passes
`--no-verify`.

### make ci

check + cover + security + capabilities + mutation, in that order, the
capability step sits before mutation so their memory peaks never overlap.
Slowest target (mutago reruns the suite per mutant). Start the compose stack and
set the `IQ_<BACKEND>_URL` variables first (see `.env.example`), so the tests
reuse the running services. Without the URLs, each package starts its own
containers, and the run takes much longer.

## Continuous integration

`.github/workflows/ci.yml` runs on the GitHub mirror only (the primary remote has
Actions off; `docs.yml` deploys the site and `release.yml` publishes releases,
both guarded the same way). Every pull request runs one job per gate. A pull
request branch runs through `pull_request` only, not also through `push`. The
ruleset of `main` requires a pull request for each change, so each pushed tree
was tested as a pull request. A push to `main` runs only `changes`, the coverage
jobs and `go-cache`. The coverage jobs keep the Codecov baseline of `main`, and
`go-cache` saves the Go cache. The first job, `changes`, decides whether the
change touches code.
It sets `code=false` only when every changed file is on a short list that no Go job
reads (Markdown files outside `skills/`, `docs/` outside the man page and the
completions, `.github/` except `ci.yml` and `.github/actions/`, `.agents/`, and the app configs). Then the
Go jobs are skipped, and a skipped job passes its required check. The `docs` job
builds the site and runs only when `changes` sets `docs=true`. That happens when
the diff changes a file under `docs/` or `ci.yml`, or when the diff fails. A
pull request that changes only a Markdown file outside `docs/` runs the fast
jobs only, and it takes about 2 minutes. A pull request that changes a file
under `docs/` also builds the site. Any other file, a change to `ci.yml` or to a
shared action in `.github/actions/`, or a diff that fails runs every job. The
`code`, `full`, `snapshot` and `docs` outputs of `changes` are true only on a
pull request. The `coverage` output ignores the event, so a push to `main` with
code changes runs the coverage jobs. The last job, `ci-ok`, runs on pull
requests only. It is the
only required check of the `main` ruleset: it needs every per-change job and fails
when one of them failed or was cancelled (a skipped job passes). The per-job checks
cannot be required themselves, because GitHub reports a skipped matrix job under
its bare name. A new per-change job goes into the `needs` list of `ci-ok`.

A draft pull request runs the fast jobs only. `changes` also sets `full=true`
when `code=true` and the pull request is not a draft. The slow jobs (`test`, the
coverage jobs, `e2e`, `capabilities`, `mutate-diff` and `fuzz`)
run only then, and `ci-ok` fails on a draft. `changes` sets `snapshot=true` when
the pull request is not a draft and the diff changes `.goreleaser.yaml`,
`go.mod`, `go.sum`, `scripts/third-party-licenses.sh`,
`scripts/tool-versions.env`, `ci.yml` or a file in `.github/actions/`, or deletes or renames `LICENSE`,
`README.md`, the man page or a completion file. `release-snapshot` runs only
then, because it takes about 11 minutes. The release copies those four kinds of
files by fixed names, so a change to their content cannot break it. The `sbom`
job runs when `snapshot=true` or when the diff changes `Makefile` (the `sbom`
output of `changes`). It runs `make sbom`, which holds the syft options. The SBOM
lists the Go module graph only, so the files of the docs site do not start it. Mark the
pull request ready for review when the review rounds settle: the
`ready_for_review` event starts the full run once. `changes` reads the draft
state from the API, not from the event, so a push just before "ready for review"
still gets the full run. `ci-ok` fails on a draft and does not skip, because
GitHub counts a skipped required check as a pass, and a skipped draft `ci-ok`
would let a pull request merge while its full run is still in progress. The runs
of one pull request share a concurrency group, so a new push cancels the run of
the previous push. A push to `main` is never cancelled.
The jobs per gate:
`go-cache` (builds the Go cache, see below), `lint` (format, a build without cgo and with `-trimpath`, golangci-lint), `test` (`go test -short -shuffle=on` on
Linux, macOS and Windows, with `-race` on Linux), `coverage` (four `coverage (<group>)` jobs each test a
group of packages on their own runner, then `coverage` joins the partial profiles,
makes sure that each package is in exactly one group, applies the floor, and does
a reporting-only Codecov upload, `CODECOV_TOKEN` secret), `e2e` (redis pass, then
the mongo live flow),
`release-snapshot` (a goreleaser snapshot with no publish and no signature, then
a check of the third-party license texts in the archives and the `.deb`),
`vuln` (govulncheck), `osv` (OSV plus the permissive license allowlist, then
`scripts/third-party-licenses.sh` on every change), `sbom` (syft), `deadcode`,
`secrets` (gitleaks, the tree and the commits of the pull request), `capabilities`
(`scripts/capabilities.sh` against the PR base), `mutate-diff`
(`scripts/mutation-gate.sh` against the PR base; it uploads the artifact
`mutate-diff` with `report.json`, `mutago-agentic.json` and, after a failure
on an escape, `mutago-baseline.candidate.json`, so the ids of the escapes need
no local re-run), `workflows` (zizmor and actionlint over
`.github/`), `fuzz` (`scripts/fuzz.sh`, the default budget per target), `dco`
(`scripts/dco.sh`, a `Signed-off-by:` for the author of each pull request commit,
and `scripts/commit-emails.sh`, no unallowed email address in a commit message,
on pull requests only) and
`docs` (site build). A pull request builds `linux/amd64` in `lint`, `darwin/arm64`
in the macOS leg of `test` and `windows/amd64` in the Windows leg of `test`. It
does not build `linux/arm64`, `darwin/amd64` or `windows/arm64`. That gap is
accepted, and `release-snapshot` builds all six targets when a release input
changes. The jobs `workflows`, `osv`, `secrets`, `demo-check` and
`dco` run on pull requests only. The
weekly `deep-*` jobs (Mondays, or `workflow_dispatch`: Actions, CI, Run
workflow) re-run the vulnerability, secret and zizmor workflow scans against fresh data
(`deep-scan`, which keeps the only gitleaks scan of the full history), give every fuzz target five minutes instead of twenty seconds
(`deep-fuzz`), run the full suite, container-backed tests included, with the race detector
(`deep-race`, in the package groups of `coverage-group`, reports only, no merge waits for it), and run an incremental, sharded mutation scan. The stored result of
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
about 120 min at the measured seconds per mutant (a package with no stored
result uses its starting rate from the table in the script, else 180 s with a
backend and 15 s without; `bash scripts/mutation-plan.sh --rate <badges-dir>
<package>` prints the rate). A cell over 120 min gets its own shard and a
warning; a cell over 285 min stops the plan, because its job cannot finish. The
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
attempt of the same job. Each cell runs under two limits: 2 GiB per file (`ulimit -f`) and 8 GiB of
virtual memory (`ulimit -v`). A mutant can turn a write loop into an endless one,
and the output goes to a file or to memory. A full disk or an exhausted memory shuts
the runner down before any timeout fires. Go ignores SIGXFSZ, so a write over the
file limit fails with EFBIG ("file too large"), and an allocation over the memory
limit is a fatal out-of-memory error. In both cases the test fails. The limits cover
the gate process tree only, not the backend containers. The cell log is written
outside the limits.

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
without `IQ_HBASE_URL`. The Linux leg of the `test` job runs with `-race`; the
macOS and Windows legs do not, because the race detector needs cgo there. The
`cmd` tests that run the root command are not parallel: its PreRun writes the
process-wide `color.NoColor`, which parallel formatter tests read.

The Go cache holds the module cache and the build cache. One composite action,
`.github/actions/setup-go`, installs Go and restores the cache. Each job that
needs Go runs `actions/checkout` first and then this action, because the cache
key hashes files of the workspace. The action does not save. The `codeql.yml` and
`mutant-proof.yml` workflows use it too. `release.yml` and `install-smoke.yml`
keep `actions/setup-go` with no cache. The key is `go-<OS>-<architecture>-<hash>`.
The hash covers `go.mod`, `go.sum`, `scripts/tool-versions.env`, `ci.yml` and the
action file. The key has no commit, so one entry exists for each set of
dependencies. When no entry matches, a job restores the newest entry of the same
OS and architecture (the restore key `go-<OS>-<architecture>-`) and compiles the
rest.

The cache has one entry for each runner OS. The Linux entry holds the variants
and the shared tools. The macOS and Windows entries hold the no-cgo build and the
plain test compile. The action has an input `restore`, which is `'true'` by
default. A job that sets `restore: 'false'` installs Go and skips the restore.
Then the output `cache-hit` is empty. The jobs `capabilities`, `workflows` and
`secrets` set it, because they compile no iq package and the restore costs more
than the tool compile that it saves. The script `scripts/capabilities.sh` skips itself unless `go.mod` or `go.sum`
changed, and a run that does not skip is rare. `workflows` runs `actionlint`,
which the cache warms, but the compile is shorter than the restore.

The `go-cache` job is the only job that saves. It runs on a push to `main`, and on
a pull request that is not a draft when the diff changes `ci.yml` or
`.github/actions/` (the `cache` output of `changes`). On a push, it saves one
entry for each runner when the exact key does not exist. On a pull request, it
builds and saves nothing, so a mistake in a build step fails the pull request and not
`main`. It looks up the exact key only and never restores the fallback entry,
because that entry would carry every old module version into the new one, and Go
never trims the module cache. The Linux leg builds the variants that the jobs
use: `go build`, `CGO_ENABLED=0 go build -trimpath`, and the test binaries of a
plain build, of `-race` and of `-covermode=atomic -coverpkg=./...`. It also runs
each fuzz target for one iteration, and compiles `golangci-lint`, `govulncheck`,
`deadcode` and `actionlint` with `go install`, with no run, because a tool can
call a host that the job blocks. It does not compile `syft`, `osv-scanner` and
`gitleaks`. The jobs `sbom`, `osv` and `secrets` compile them, because these tools
bring a large module graph that every Linux job would download. The macOS and
Windows legs of `go-cache` run `go build -trimpath` and the plain test compile
with no cgo. The `test` job sets `CGO_ENABLED=1` on Linux and `0` on macOS and
Windows, and builds with `-trimpath` there. These legs use the cached objects, and
the build is the only per-change build of those targets, because `cross` is gone.
`release-snapshot` gets no gain from the
cache, because goreleaser builds targets that the cache does not hold. The old
`setup-go-*` entries expire after seven days without a use. A maintainer can
delete them earlier with `gh cache delete`.

Tool versions are pinned once in `scripts/tool-versions.env`. Each workflow step
that runs a tool reads that file first. The pins of mutago and capslock stay in
`scripts/mutation-gate.sh` and `scripts/capabilities.sh`.

Posture and upkeep around the pipeline, all on GitHub:

- `.github/workflows/scorecard.yml` runs the OpenSSF Scorecard on pushes to
  `main` and weekly, uploads the SARIF to the Security tab and publishes the
  score behind the README badge. Every action in every workflow is pinned by
  commit SHA with the release in a trailing comment; keep it that way (a tag
  pin is a Scorecard deduction and a supply-chain gap).
  The one exception is the SLSA generator in `release.yml`, which is pinned by
  tag. `slsa-verifier` checks the provenance against the tag of the trusted
  reusable workflow, so a SHA pin would fail that check. The project accepts the
  Scorecard finding.
- The repository Actions setting allows only actions made by GitHub, actions from
  verified creators, and an allow-list with these exact patterns:
  `astral-sh/setup-uv@*`, `codecov/codecov-action@*`, `ossf/scorecard-action@*`,
  `goreleaser/goreleaser-action@*`, `sigstore/cosign-installer@*`,
  `slsa-framework/*`, `softprops/action-gh-release@*` and
  `step-security/harden-runner@*`. A reusable workflow from another repository
  also runs the actions that it uses itself, and each of those must be allowed
  too. The SLSA generator uses four actions of its own repository and
  `softprops/action-gh-release`; allowing only its workflow file stopped the
  v0.37.1 release before its first job (`startup_failure`). Before a new
  third-party workflow or action goes into a workflow, read which actions it uses
  and add them to the allow-list in the same change.
- Every job on a Linux runner starts with
  [Harden-Runner](https://github.com/step-security/harden-runner) in block mode
  (`egress-policy: block`) with `disable-telemetry: true`, so no data goes to
  StepSecurity. The `allowed-endpoints` input of each job lists every host and
  port that the job calls, sorted. All other outbound traffic fails, also the
  traffic of Docker containers. The lists come from the audit logs of the first
  week and of the release runs, and from the steps of each job. A job on a macOS
  or Windows runner
  does not run Harden-Runner, because block mode supports Linux runners only.
- In `ci.yml`, the Harden-Runner steps with the same host list and the same `if:`
  share one YAML anchor (`&harden-base`, `&harden-go`, `&harden-go-linux`,
  `&harden-docker` and `&harden-docker-elastic`). The first job that uses an
  anchor defines it, and the other jobs use the alias. A job that needs a host
  that the shared list does not have gets an inline step of its own. Never widen
  a shared list for one job. To add a host that all jobs of a group need, edit
  the anchor.
- When a job fails because of a blocked call, open the job log. The annotation
  names the blocked domain. Make sure that the call is expected, then add
  `host:443` to the `allowed-endpoints` list of that job in sorted order. A matrix
  job (`coverage-group`, `deep-mutate`, `mutant-proof`) has one list for all legs,
  so add the host there. When a new tool or compose image enters a job, add its
  hosts in the same change. Do not add a host that you did not check.
  Every job that runs `actions/setup-go` or the shared action
  `.github/actions/setup-go` also allows `go.dev:443` and `dl.google.com:443`.
  The action downloads Go from there when the Go version is not yet in the runner
  cache or in the GitHub version manifest. The shared action also needs
  `*.actions.githubusercontent.com:443` and `*.blob.core.windows.net:443`, which
  serve the cache.
- `.github/workflows/codeql.yml` runs CodeQL (the `security-and-quality` queries
  over the Go code) on every pull request, every push to `main` and weekly, and
  uploads the results to the Security tab. It is not a required check:
  golangci-lint with gosec and govulncheck stay the gates, and a CodeQL alert is
  read and closed in the Security tab. OpenSSF Scorecard's SAST check counts it.
- [zizmor](https://docs.zizmor.sh) audits the workflow files themselves: unpinned
  or impostor actions, credentials the checkout leaves on disk, cache poisoning on
  the release paths, and template injection into a `run` block. The version is
  pinned once as `ZIZMOR_VERSION` in `scripts/tool-versions.env`, and Renovate
  tracks it. `make security` runs it locally, the `workflows` job runs it on
  every push and pull request, and `deep-scan` re-runs it weekly against fresh
  advisory data. An accepted finding is an inline `# zizmor: ignore[<audit>]`
  comment with a reason on the offending line. Two exist. In `ci.yml`, the
  `capabilities` and `mutate-diff` jobs keep the checkout credential because
  they fetch the pull-request base branch. `.github/zizmor.yml` turns off one
  audit everywhere: `self-repository`, which asks for the form `$/.github/...`.
  OpenSSF Scorecard counts that form as an unpinned third-party action, so the
  workflows use `./.github/...`. The two forms run the same code, because no
  workflow checks out a commit other than the one that started it.
- [actionlint](https://github.com/rhysd/actionlint) checks that the workflow files
  are correct: the schema, the types in `${{ }}` expressions, `needs` and `outputs`
  references, action inputs, and the `run` blocks through `shellcheck`. The version
  is pinned once as `ACTIONLINT_VERSION` in `scripts/tool-versions.env`, and
  Renovate tracks it. `make check` runs it locally, and the `workflows` job runs it
  on every push and pull request, next to zizmor. One finding is accepted.
  In `ci.yml`, the `test` job leaves `$RACE` unquoted on purpose, because it is
  empty off Linux and must disappear from the command. It has an inline
  `# shellcheck disable=SC2086` line.
- `renovate.json` drives Renovate (the Mend GitHub App). It updates these items:
  the Go modules, the GitHub Actions (pinned by digest), the Go toolchain, the
  tool pins, the test images in `compose.yaml`, and the docs tooling in
  `docs/pyproject.toml` (`python` and `zensical`). A human merges every PR.
  The `:gitSignOff` preset makes Renovate sign off each commit that it makes.
  If a person adds a commit to a Renovate PR, that commit also needs a
  sign-off, or the `dco` job fails.
  A PR that moves `go.mod` runs `make capabilities`, which is the review that
  AGENTS.md asks for on a dependency change.
- Renovate sends minor and patch updates as one grouped PR on Monday.
  A breaking update arrives as a draft PR with the `breaking` label. Three
  kinds count as breaking: a major, a minor of a version below 1.0, and a Go
  toolchain update. To skip a breaking update, close its draft. Renovate then
  does not propose that update again. To adopt it, finish the work on the
  branch and mark the PR ready.
- Renovate waits three days after a release before it proposes the release.
  The Elasticsearch image does not wait, because its registry gives no release
  dates. Security updates do not wait. Security PRs depend on the Dependabot alerts of
  the repository, so keep the alerts and the dependency graph on. Dependabot
  security updates can stay off.
- iq imports `github.com/zsltg/rdb`, a fork of `github.com/hdt3213/rdb`. The
  fork fixes a fatal out-of-memory error on a length that an RDB file declares
  (upstream issue 75, pull request 76). The fork branch `zsltg-module-path`
  changes the module path, because `go install` refuses a module whose `go.mod`
  has a `replace` directive. Do not use a `replace` in `go.mod`. When upstream
  releases the fix, change the imports back to `github.com/hdt3213/rdb`, require
  its fixed version, and remove the require of `github.com/zsltg/rdb`.
- The tool pins live in `scripts/tool-versions.env`, `scripts/mutation-gate.sh`,
  and `scripts/capabilities.sh`. One custom manager reads them. Each pin needs a
  line directly above it in this form: `# renovate: datasource=go depName=<package>`.
  Use `datasource=pypi` for a PyPI tool. Without that line, Renovate does not
  see the pin.
- `scripts/license-allowlist.txt` is the permissive license allowlist, one SPDX
  ID per line. `make security`, the CI `osv` job and the weekly `deep-scan` read
  it and check every Go module against it (`osv-scanner --licenses`, `go.mod`
  only). `osv-scanner.toml` carries the license overrides that the check needs:
  deps.dev reports a
  module's documentation license (CC-BY) or UNKNOWN for a few modules whose
  code is permissive; each override names the LICENSE file it was read from.
  A new violation means reading the module's LICENSE, then either an override
  with a reason or a different dependency.
- `REVIEW.md` holds the review rules for every reviewer, human or AI: the
  priorities, the threat model, the critical areas and what not to flag. The
  coding rules stay in AGENTS.md. CodeRabbit reads both files, so a review
  rule changes in `REVIEW.md` only.
- `.codescene/code-health-rules.json` tunes the CodeScene code health review for
  Go test files: a cyclomatic complexity threshold of 15 in place of 9, and no
  "Bumpy Road Ahead" rule, because a table test or a fuzz target checks one
  invariant per branch. Production code keeps the default rules. The CodeScene
  review is not a required check.
- `.coderabbit.yaml` holds CodeRabbit's settings only (profile, automatic
  review, linters). It reviews, it never approves or merges.
- `socket.yml` configures the Socket GitHub App: on every pull request that
  moves `go.mod` or `go.sum` it reports what the new module versions do
  (install scripts, obfuscation, typosquats, maintainer changes), the
  complement of `make capabilities`, which reports what our own dependency
  tree can do; it comments, it never blocks a merge.
- `.github/workflows/install-smoke.yml` verifies a published release the way
  a user installs it, on fresh hosted runners with no checkout: the Homebrew
  cask on macOS, the Scoop manifest on Windows, `install.sh`, the `.deb` and
  `go install` on Linux, each ending in `iq --version` equal to the tag and a
  query against a registered dump. `release.yml` calls it after goreleaser for
  the new tag. It also runs on demand (Actions, Install smoke, Run workflow,
  with an optional tag). It needs the repo, the tap and the bucket to be public.
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

Keep `gofumpt`, `goimports`, and `golangci-lint` on `PATH`.
Nothing else needs an install step. The scripts and the Makefile run every other
tool through `go run <package>@<version>`, or through `uvx` for zizmor. The versions
are in `scripts/tool-versions.env`, the same versions that CI runs. The first run
of each tool needs network access to the module proxy, and later runs use the
module cache. The gates provision their own pinned mutago and capslock.

## Releasing

```bash
make version                        # print the version the next release would take
make changelog                      # regenerate CHANGELOG.md alone
bash scripts/release.sh --dry-run   # preview, no changes
make release-check                  # validate the goreleaser config
make release-snapshot               # local snapshot build of every artifact, no tag
```

The release scripts run `svu` and `git-chglog` through `go run` at the versions in
`scripts/tool-versions.env`. You install nothing, but the first run needs network access.

`main` accepts only squash-merged pull requests, so a release has two steps:

```bash
git fetch github
git worktree add -b chore/release-vX.Y.Z .claude/worktrees/release github/main
make -C .claude/worktrees/release release     # regenerate CHANGELOG.md, commit on the branch
git -C .claude/worktrees/release push github chore/release-vX.Y.Z
# open the pull request, titled "chore(release): vX.Y.Z", and squash-merge it
git pull --ff-only github main && git push origin main
make release-tag                              # tag the release commit on main
git push github vX.Y.Z && git push origin vX.Y.Z   # only this tag, never --tags
```

Conventional Commits drive the bump (`feat` → minor, `fix` → patch,
`!`/`BREAKING CHANGE` → major). With no tags yet the first release is
`v0.1.0`. `make release` runs only on a clean `chore/release` branch.
`make release-tag` runs only on a clean `main`, and tags HEAD only when HEAD is
the squash-merged release commit of the version that `make version` prints.
Neither step pushes. Publishing happens in the GitHub repository: the release
workflow runs goreleaser when a `v*` tag reaches it. goreleaser signs
`checksums.txt` with a keyless cosign signature (the workflow's GitHub OIDC
identity, `checksums.txt.sigstore.json`), and a second job adds SLSA level 3 build
provenance for every artifact (`slsa-github-generator`, `multiple.intoto.jsonl`).
Then the install smoke test runs. How a user checks the signature and the
provenance is on the docs home page, "Verify a release".

Each binary archive and each Linux package carries the license texts of every
linked module. The source archive does not, because goreleaser makes it from the
Git tree. goreleaser runs `scripts/third-party-licenses.sh` before the build.
The script runs `go list -deps` for the six release targets. For each module, it
copies the license and notice files into `third-party-licenses/`, with the Go
runtime license in `third-party-licenses/go/`. The script exits with an error if
a module has no license file. A NOTICE or PATENTS file alone does not count. The
script sets mode 0755 on each directory of the tree, so every user can read the
installed texts. `make security` and the CI `osv` job run it too, so a module
without a license file fails every pull request. The CI `release-snapshot` job
builds the release as a snapshot, with no publish and no signature. Then it
checks that each binary archive and the `.deb` package carry the same files as
the tree, and that the `.deb` directories have mode 0755. It runs only when a
file changes that can alter the build or the packaging. A code change alters
only which license texts go in the tree, and the `osv` job checks those. The
license policy stays in `scripts/license-allowlist.txt`. The script only
collects texts.

A release that fixes a vulnerability names its advisory ID. Put the ID
(`GHSA-...`, and the CVE when one exists) in the title of the fix pull request,
so the squash commit carries it into CHANGELOG.md. After the release is
published, add the ID to the GitHub release text and publish the advisory.

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
