# AGENTS.md
Telegraph style, every line binds. Root rules and policies only; per-book depth in the distilled guideline files listed under Guidelines. Load every session: clean-code.mini, a-philosophy-of-software-design.mini, clean-architecture.mini, the-pragmatic-programmer.mini. Read on demand when the work matches: designing-data-intensive-applications.mini, release-it.mini, refactoring.mini.
## Project
A Go command-line tool that connects to NoSQL databases and runs queries. Single static binary; the CLI is a thin delivery mechanism over a driver-agnostic query core.
## Tech Stack
Load-bearing shape only; no framework chosen yet, README carries specifics once they exist.
- Language: Go, built to a single static binary.
- CLI parsing, configuration, NoSQL drivers and output formatting: compose permissive OSS, reinvent last; every pick permissive-licensed, dependency-light, no telemetry or PII.
## Commands
Standard Go toolchain; README is the full catalogue once it exists.
- Build: `go build ./...`.
- Test, scoped to your diff by default: `go test ./<pkg>/...`; full run `go test ./...`; 100% pass.
- Vet and lint: `go vet ./...`, `golangci-lint run`.
- Format, canonical and deterministic, always run before commit: `gofmt -w .` (or `goimports -w .`).
- Vulnerabilities, before adding or upgrading a dependency: `govulncheck ./...`.
## Coding Conventions
Boring, linear, readable code; day-to-day doctrine loads every session from clean-code.mini, a-philosophy-of-software-design.mini and clean-architecture.mini.
- Compose OSS, reinvent last: standard library, then a maintained permissive library, then hand-roll only for a genuine determinism, footprint or license gap.
- Keep the query core driver-agnostic: domain and query logic never import a specific NoSQL driver or the CLI framework; drivers and the CLI are outer details behind ports wired at a composition root.
- Cross boundaries with plain types: pass request and response structs across the core boundary, never a driver row, framework context or raw flag struct; adapters translate at the edge.
- Fail fast on hostile input: validate at function entry and return immediately; treat args, config, connection strings, query fragments and database responses as untrusted.
- Never build a query from unsanitized input: parameterize every query and filter; no string concatenation or templating of user input into a query.
- Errors are values: wrap with context via `%w`, handle at a boundary, never ignore a returned error, never leak internals to the user.
- Bound every outbound call: pass a context with an explicit timeout or deadline, never an infinite wait; bound retries with backoff and jitter; retry only idempotent operations, never validation or permanent failures.
- Stream large results: paginate or stream result sets rather than loading whole into memory; do not hold a connection across a slow call; release every resource on success and failure paths.
- No dead code: delete unused files, exports and dependencies; `go vet`, `staticcheck` and `deadcode` catch what review misses, run before merge.
- Make impossible states unrepresentable: closed types over sentinel strings or parallel nullable fields; one source of truth per fact.
- Tests table-driven and flat: one behaviour per case, a `t.Run` subtest per row, arrange-act-assert, no factories or shared mutable fixtures; keep functions pure and isolatable.
- Simplest mechanism that fits: no plugin frameworks, code generation or heavy patterns unless asked.
- Resolve uncertainty deliberately: costly to reverse and unclear, ask; cheap, proceed on the most reasonable reading and record the assumption; unsure it works, run a small experiment and report.
- Push back when it matters: surface real risks and deviations, skip style nits.
- Definition of Done: scoped tests, `go vet`, lint, gofmt clean, edge cases, updated docs, honest closeout of what was skipped, assumed or left.
## Docs stay current
- README Common commands is the full catalogue; update it in the same change that adds or alters a developer-facing command, dependency or environment variable; environment variables also update `.env.example`.
- A change to the system's shape (a new datastore target, a new delivery surface, a changed connection contract) updates the README Architecture section in the same change.
## Boundaries
Never:
- Hand-edit generated artifacts (`go generate` output, vendored code).
- Strip, hide or bypass existing behaviour to shrink a diff or pass a test; change behaviour deliberately and say so.
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
- Pre-merge: scoped tests, `go vet`, `golangci-lint run`, gofmt clean; 100% pass.
## Guidelines
Distilled book files live in `.agents/`, referenced below by name.
Generic doctrine, always loaded:
- clean-code.mini: naming, functions, types, errors, test hygiene.
- a-philosophy-of-software-design.mini: module shape, deep modules, complexity budget.
- clean-architecture.mini: dependency direction, ports and adapters, driver-agnostic core.
- the-pragmatic-programmer.mini: one source of truth, orthogonality, reversible choices, tracer bullets, automation.
Specific doctrine, read on demand:
- designing-data-intensive-applications.mini: writing the query, data-model or connection layer; NoSQL consistency, staleness, partitioning, schema evolution, idempotency.
- release-it.mini: writing database or network calls; timeouts, bounded retries, result-set limits, validating responses, failing fast.
- refactoring.mini: restructuring existing code without changing behaviour.
