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
- Vet and lint: `go vet ./...`, `golangci-lint run`; fix every reported issue before committing, do not wait to be asked; the config enables `godot` (comments end with a period) and `unused`, among others.
- Format, canonical and deterministic, run before lint and commit: `gofumpt -w .` (stricter gofmt superset) then `goimports -w .` for import grouping.
- Vulnerabilities, before adding or upgrading a dependency: `govulncheck ./...`.
- Mutation gate: `scripts/mutation-gate.sh` (needs `gremlins`: `go install github.com/go-gremlins/gremlins/cmd/gremlins@latest`); it wraps gremlins to fail on any surviving or timed-out mutant because gremlins v0.6.0 reports but does not exit-code-enforce, running serially with a wide timeout; by default scopes to the branch diff against `main` (`gremlins --diff`) so a change is gated only on lines it touched — override the base with `IQ_MUTATION_BASE` (empty for a full scan) or widen with a package-path argument; run with the integration services up so backend adapters are covered.
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
- No dead code: delete unused files, exports and dependencies; `go vet`, `staticcheck` and `deadcode` catch what review misses, run before merge.
- Make impossible states unrepresentable: closed types over sentinel strings or parallel nullable fields; one source of truth per fact.
- Tests table-driven and flat: one behaviour per case, a `t.Run` subtest per row, arrange-act-assert, no factories or shared mutable fixtures; keep functions pure and isolatable.
- Prefer testify assertions with `require` (fails fast, the default) over `assert` (continues); use `assert` only to report several independent failures in one run.
- Container-backed integration tests call `testing.Short()` and skip under `go test -short`; `go test -short ./...` is the fast dependency-free path, full `go test ./...` needs the containers up.
- Simplest mechanism that fits: no plugin frameworks, code generation or heavy patterns unless asked.
- Resolve uncertainty deliberately: costly to reverse and unclear, ask; cheap, proceed on the most reasonable reading and record the assumption; unsure it works, run a small experiment and report.
- Push back when it matters: surface real risks and deviations, skip style nits.
- Definition of Done: scoped tests, `go vet`, `golangci-lint` clean, gofumpt and goimports clean, mutation gate green (zero surviving mutants on covered code), edge cases, updated docs, honest closeout of what was skipped, assumed or left.
## Docs stay current
- README Common commands is the full catalogue; update it in the same change that adds or alters a developer-facing command, dependency or environment variable; environment variables also update `.env.example`.
- A change to the system's shape (a new datastore target, a new delivery surface, a changed connection contract) updates the README Architecture section in the same change.
- A change to the selector's classification, the pushdown-to-predicate mapping, a core port, or a backend adapter updates the README Architecture Mermaid diagram in the same change; keep the committed diagram in sync, never redraw it from scratch.
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
- Isolate parallel work in a git worktree branched from `origin/main`; never switch branches in a shared checkout; merge to `main` fast-forward-only, never paper over a conflict.
- Conventional Commits: `<type>(<scope>): <description>`, lowercase imperative; types feat, fix, docs, style, refactor, perf, test, build, ci, chore; agent-authored commits end with a `Co-Authored-By:` trailer.
- Pre-merge: scoped tests, `go vet`, `golangci-lint run`, gofumpt and goimports clean, `scripts/mutation-gate.sh` green; 100% pass.
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
