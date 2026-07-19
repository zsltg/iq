# AGENTS.md
Telegraph style, every line binds. Root rules and policies only; per-book depth in the distilled guideline files listed under Guidelines.
## Project
A Go command-line tool that connects to NoSQL databases and runs queries. Single static binary; the CLI is a thin delivery mechanism over a driver-agnostic query core.
## Tech Stack
Load-bearing shape only; no framework chosen yet, README carries specifics once they exist.
- Language: Go, built to a single static binary.
- CLI parsing, configuration, NoSQL drivers, output formatting: libraries TBD; every pick permissive-licensed, dependency-light, no telemetry or PII.
## Commands
Standard Go toolchain; README is the full catalogue once it exists.
- Build: `go build ./...`.
- Test, scoped to your diff by default: `go test ./<pkg>/...`; full run `go test ./...`; skip container-backed integration tests with `go test -short ./...`; 100% pass.
- Vet and lint: `go vet ./...`, `golangci-lint run`; fix every reported issue before committing, do not wait to be asked; atop the v2 defaults (`staticcheck`, `unused`, among others) the config enables `godot` (comments end with a period), `gosec` (the Go SAST, excluded from `_test.go` fixtures), `errorlint`, `testifylint`, `bodyclose`, `noctx`, and `misspell`.
- Format, canonical and deterministic, run before lint and commit: `gofumpt -w .` (stricter gofmt superset) then `goimports -w .` for import grouping.
- Vulnerabilities and supply chain: `govulncheck ./...` before adding or upgrading a dependency; the full sweep `make security` (`scripts/security.sh`) adds `osv-scanner`, `gitleaks` secret-scanning over the tree and history, and an `syft` SBOM written to `dist/`; needs the network.
- Mutation gate: `scripts/mutation-gate.sh` (also `make mutation`); the wrapper provisions the pinned mutago itself (`go install github.com/quality-gates/mutago/v2/cmd/mutago@v2.7.7` into a throwaway GOBIN, run directly — no PATH dependency, no go.mod change; `make tools-dev` still installs it for ad-hoc use); mutago exit-code-enforces via `--fail-on-escaped`, the wrapper shapes scope and forwards the verdict, running serially (`--workers 1`) with a wide `--timeout-coefficient`; an escaped covered mutant fails it (a covered test that asserts nothing — strengthen the test), `--coverage` keeps uncovered lines out of the escaped set (zero-survivor on covered code), timed-out mutants are reported "errored" and not gated; by default scopes to the branch diff against `origin/main` via the merge-base commit (`--git-diff-lines --git-diff-base`), so it works from a linked worktree and tolerates a drifted local base; a change is gated only on lines it touched — override the base with `IQ_MUTATION_BASE` (empty for a full scan) or pass a package path for a full scan of that package (a path drops the diff-scoping flags); a genuine equivalent mutant is accepted into the committed `mutago-baseline.json` via `IQ_MUTATION_UPDATE_BASELINE=1` (line-independent IDs, only new escapes then fail — this records accepted equivalents, it does not weaken the gate) with a one-line justification added to the committed `mutago-baseline.notes.md`; every run writes the gitignored `mutago-agentic.json` (LLM-consumable escaped-mutant data), and `IQ_MUTATION_MUTANT=<id>` re-runs one mutant by that id as a fast diagnostic (re-verify a survivor or probe an order-dependent escape); `IQ_MUTATION_DRYRUN=1` is a mutant-count preview: package-arg form is an instant count with no tests, diff-scoped or `./...` form first runs the `--coverage` instrumented pass (whole-target, memory-heavy) — scope dry runs to one package, never beside a live gate; run with the integration services up so backend adapters are covered.
- Aggregate gate: `make check` (`scripts/check.sh`) is the fast offline gate — format, `go vet`, `go build ./...`, lint, dead code, and `go test -short` with a coverage report; `make ci` runs check, cover, security, and the mutation gate together (the mutation step reruns the suite per mutant, so it is the slowest; start a shared stack first).
- Coverage: `make cover` (`scripts/coverage.sh`) runs the full suite with `-coverpkg=./...` (so cross-package coverage counts and a testless package still enters the denominator) and fails below the floor (`IQ_COVER_MIN`, default 80); `IQ_COVER_SHORT=1` for a report-only run; needs the integration services up, since `-short` understates the drivers.
- End-to-end: `make e2e` builds the binary and drives it black-box through `os/exec` (package `e2e`, skips under `-short`).
- Dead code: `deadcode -test ./...` (whole-program), wired into `make check`.
- Toolchain: `make tools-dev` installs the quality and security tools (mutago, deadcode, govulncheck, osv-scanner, gitleaks, syft) into GOPATH/bin.
- Release: `make release` (`scripts/release.sh`, needs `svu` and `git-chglog`: `make tools`); computes the next semver from Conventional Commits, regenerates `CHANGELOG.md`, commits, and tags on clean `main`; never pushes; preview with `bash scripts/release.sh --dry-run`; version metadata is embedded by `make build` via ldflags.
## Coding Conventions
Boring, linear, readable code.
- Compose OSS, reinvent last: standard library, then a maintained permissive library, then hand-roll only for a genuine determinism, footprint or license gap.
- Keep the query core driver-agnostic: domain and query logic never import a specific NoSQL driver or the CLI framework; backend adapters and the CLI are outer details behind ports wired at a composition root.
- Cross boundaries with plain types: pass request and response structs across the core boundary, never a driver row, framework context or raw flag struct; backend adapters translate at the edge.
- Fail fast on hostile input: validate at function entry and return immediately; treat args, config, connection strings, query fragments and database responses as untrusted.
- Never build a query from unsanitized input: parameterize every query and filter; no string concatenation or templating of user input into a query.
- Errors are values: wrap every error with context at the boundary it crosses so the message anchors at our caller not deep in an external library, via `fmt.Errorf("...: %w", err)`; handle at a boundary, never ignore a returned error, never leak internals to the user.
- Wrap stdlib and third-party errors at the point they enter our code; package-level sentinels stay `errors.New` for `errors.Is`/`errors.As` and get wrapped with context where they are returned; when call-site stack traces become worth it, adopt a tracing error library (e.g. github.com/cockroachdb/errors) rather than scattering stackless errors.
- Bound every outbound call: pass a context with an explicit timeout or deadline, never an infinite wait; bound retries with backoff and jitter; retry only idempotent operations, never validation or permanent failures.
- Stream large results: paginate or stream result sets rather than loading whole into memory; do not hold a connection across a slow call; release every resource on success and failure paths.
- No dead code: delete unused files, exports and dependencies; `go vet`, `staticcheck` and `deadcode` catch what review misses — `deadcode` runs in `make check`.
- Make impossible states unrepresentable: closed types over sentinel strings or parallel nullable fields; one source of truth per fact.
- Tests table-driven and flat: one behaviour per case, a `t.Run` subtest per row, arrange-act-assert, no factories or shared mutable fixtures; keep functions pure and isolatable.
- Prefer testify assertions with `require` (fails fast, the default) over `assert` (continues); use `assert` only to report several independent failures in one run.
- Container-backed integration tests call `testing.Short()` and skip under `go test -short`; `go test -short ./...` is the fast dependency-free path, full `go test ./...` needs the containers up.
- Simplest mechanism that fits: no plugin frameworks, code generation or heavy patterns unless asked.
- Resolve uncertainty deliberately: costly to reverse and unclear, ask; cheap, proceed on the most reasonable reading and record the assumption; unsure it works, run a small experiment and report.
- Push back when it matters: surface real risks and deviations, skip style nits.
- Definition of Done: scoped tests, `make check` clean (format, vet, build, lint, dead code), coverage floor met (`make cover`), security sweep clean (`make security`), `make e2e` passing, mutation gate green (zero surviving mutants on covered code), edge cases, updated docs, honest closeout of what was skipped, assumed or left.
## Adding a driver
The port is trivially thin (`Store.Query`); the cost is around the adapter, and the gates below enforce it. Gate every candidate datastore — especially an enterprise or cloud-managed one — on all four before writing adapter code; a No on any one is a stop, not a workaround. A datastore that passes gets its adapter and plan doc designed against [driver-contract](.agents/driver-contract.md); its registry — option spellings, host ports, exec families — binds.
- Hermetic test image: a freely-runnable local image or emulator that `testcontainers` can drive, no EULA-gated pull, no live cloud account, no CI credentials; without it the coverage floor and the mutation gate cannot go green on the driver's covered code, so it fails the Definition of Done. DynamoDB Local passes; DataStax/Couchbase/Oracle Enterprise images and the flaky Cosmos emulator do not — target the open-source edition, which shares the driver.
- Permissive, telemetry-free SDK: the Go driver is permissive-licensed and emits no telemetry or usage metrics by default; a proprietary or non-permissive driver is a hard stop, and every new SDK widens the `govulncheck`/`osv-scanner`/SBOM surface.
- Auth fits the connection contract: authentication reduces to config the composition root injects, with no native dependency (Kerberos/GSSAPI) and no live-account requirement; token refresh and rotation obey the secret rules — never log a credential, release every resource, bound every outbound call.
- Query semantics fit the model: the datastore's query shape maps onto the selector's classification and the pushdown-to-predicate mapping; a backend with hard constraints (partition-key-required, per-request cost units, row-key-range-only) that forces a redesign or defeats pushdown is a design decision to raise first, and updates the README Architecture Mermaid in the same change.
## Docs stay current
- README Common commands is the full catalogue; update it in the same change that adds or alters a developer-facing command, dependency or environment variable; environment variables also update `.env.example`.
- A change to the system's shape (a new datastore target, a new delivery surface, a changed connection contract) updates the README Architecture section in the same change.
- A change to the selector's classification, the pushdown-to-predicate mapping, or a core port updates the README Architecture Mermaid diagram in the same change; the diagrams are port-level, so a backend-adapter change updates the Architecture prose and the driver table instead, never the diagram; keep the committed diagram in sync, never redraw it from scratch.
## Boundaries
Never:
- Hand-edit generated artifacts (`go generate` output, vendored code).
- Strip, hide or bypass existing behaviour to shrink a diff or pass a test; change behaviour deliberately and say so.
- Weaken the mutation gate to pass; a surviving mutant means a test asserts nothing, so strengthen the test instead.
- Build a query from unsanitized input, or log or print a credential, token or connection string.
- Render a raw driver error, stack trace or database internal to the user; clear, safe messages only.
- Hardcode or commit secrets; credentials come from environment or a secret store.

Only when asked:
- Commit or push; add or upgrade a dependency (trips supply-chain and licensing review); run a destructive database operation the command did not request.

Always:
- Validate untrusted input at the boundary and keep the query core independent of any specific driver.
## Source Control & Commits
- Trunk-based: `main` always buildable; short-lived branches, one atomic task each, prefixed `feat/`, `fix/`, `chore/`, `test/`; no unrelated changes bundled.
- Never work on `main` unless explicitly asked; start every task in its own git worktree branched from `origin/main`; never switch branches in a shared checkout; merge to `main` fast-forward-only, never paper over a conflict.
- Conventional Commits: `<type>(<scope>): <description>`, lowercase imperative; types feat, fix, docs, style, refactor, perf, test, build, ci, chore; agent-authored commits end with a `Co-Authored-By:` trailer.
- Pre-merge: `make check` clean, `make cover` above floor, `make security` clean, `make e2e` passing, `scripts/mutation-gate.sh` green; 100% pass.
## Guidelines
Distilled book files live in `.agents/books/`, referenced below by name; adapted from ciembor/agent-rules-books (MIT), see `.agents/books/LICENSE`.

Generic doctrine, always loaded:
- [clean-code.mini](.agents/books/clean-code.mini.md): naming, functions, types, errors, test hygiene.
- [a-philosophy-of-software-design.mini](.agents/books/a-philosophy-of-software-design.mini.md): module shape, deep modules, complexity budget.
- [clean-architecture.mini](.agents/books/clean-architecture.mini.md): dependency direction, ports and adapters, driver-agnostic core.
- [the-pragmatic-programmer.mini](.agents/books/the-pragmatic-programmer.mini.md): one source of truth, orthogonality, reversible choices, tracer bullets, automation.

Specific doctrine, read on demand:
- [designing-data-intensive-applications.mini](.agents/books/designing-data-intensive-applications.mini.md): writing the query, data-model or connection layer; NoSQL consistency, staleness, partitioning, schema evolution, idempotency.
- [release-it.mini](.agents/books/release-it.mini.md): writing database or network calls; timeouts, bounded retries, result-set limits, validating responses, failing fast.
- [refactoring.mini](.agents/books/refactoring.mini.md): restructuring existing code without changing behaviour.
- [driver-contract](.agents/driver-contract.md): designing or changing a backend adapter or its plan doc; URL and option registry, key and value shape, reads, pushdown, writes, admin ports, exec families, safety, test infra, host-port table.
