#!/usr/bin/env bash
# Mutation gate. Runs github.com/quality-gates/mutago and lets it exit-code-enforce:
# --fail-on-escaped exits 4 the moment a mutant survives a covered test, so this
# wrapper only shapes the scope and forwards the exit code. Serial workers plus a
# wide per-mutant timeout keep the container-integration runs stable.
#
# Version-hermetic invocation. mutago is pinned by MUTAGO_VERSION (below) and provisioned
# per run into a throwaway GOBIN via `go install <pinned>@version`, then executed
# directly. This removes any dependency on a mutago binary on PATH (the gate no longer
# needs `make tools-dev`) and never touches this module's go.mod. Direct execution is
# deliberate over `go run`: `go run` collapses a non-zero program exit to 1, which would
# make an escaped-mutant verdict (exit 4) indistinguishable from a run error, whereas the
# installed binary preserves mutago's exact exit codes and streams output live.
#
# What counts as a failure. --fail-on-escaped fails on any *escaped* mutant that is
# not recorded in the baseline (mutago-baseline.json); an escaped mutant means a
# covered test asserts nothing, so strengthen the test. --coverage keeps uncovered
# lines out of the escaped set (they become "not covered", never a failure), so the
# gate mirrors the old zero-survivor-on-covered-code contract. Timed-out mutants are
# reported as "errored" and are *not* gated — the wide --timeout-coefficient keeps a
# legitimately slow suite from erroring; a genuine hang still shows up in the output.
# Every gate run also writes mutago-agentic.json (--logger-agentic-json): enriched,
# LLM-consumable data for each escaped mutant, including the stable id used below to
# re-run one mutant. It is regenerated per run and gitignored next to report.json.
#
# Accepted baseline entries are justified one-per-line in mutago-baseline.notes.md
# (committed): consult it before re-litigating a refactor-resurfaced equivalent, and
# add a line there whenever you extend mutago-baseline.json.
#
# Scope. With no argument it scopes to the branch's diff against the base ref
# (IQ_MUTATION_BASE, default origin/main) via mutago's --git-diff-lines, so a change
# is gated only on the lines it touched — the fast path for a feature branch. The
# base is handed to mutago as the merge-base *commit* with HEAD, so a local base ref
# that has drifted from the remote still yields the true branch point, and it works
# from a linked git worktree (git computes the diff, mutago restricts to it). Pass a
# package path (e.g. ./cmd) for a full mutation scan of that package instead: a path
# drops the diff-scoping flags and mutates the whole package. Set IQ_MUTATION_BASE=
# (empty) to mutate the whole module.
#
# IQ_MUTATION_MUTANT=<id> re-runs a single mutant by its stable id (copy the id field
# from mutago-agentic.json) via mutago's --run-mutant-id: after strengthening a test,
# re-verify one survivor in seconds instead of rerunning the whole gate, and probe an
# order-dependent escape for flakiness by running the same mutant a few times. mutago
# suppresses the gate verdict and summary in this mode, so it is a diagnostic, not a
# gate. Point it at the mutant's package (e.g. ./drivers/file) for a fast enumeration;
# a whole-module ./... target still works but re-enumerates everything.
#
# IQ_MUTATION_DRYRUN=1 is a mutant-count preview whose cost depends on the form: a
# package-arg dry run (e.g. ./internal/numfmt) is instant and runs no tests; a
# diff-scoped or whole-module (./...) dry run first runs the --coverage instrumented
# test pass — whole-target, memory-heavy, buffered until exit — before counting. The
# count is a whole-target upper bound either way. Scope dry runs to one package and
# never launch one alongside a live gate — the two contend for memory (oomd kill).
#
# IQ_MUTATION_UPDATE_BASELINE=1 records the current survivors into mutago-baseline.json
# and exits 0 (accept genuine equivalent mutants deliberately, then commit the file
# and add a justification line to mutago-baseline.notes.md).
#
# The suite provisions its own containers, but mutago reruns it per mutant, so start
# a shared stack and point the tests at it to avoid per-mutant churn:
#   docker compose up -d --wait
#   export IQ_REDIS_URL=redis://localhost:6379/0 IQ_MONGO_URL=mongodb://localhost:27017/iq_test
# The Redis integration tests pin themselves to reserved databases (14 and 15),
# so a shared stack seeded on DB 0 by scripts/seed-redis.sh keeps its data through the run.
set -uo pipefail

# Pinned mutago version — keep in step with AGENTS.md and `make tools-dev` (Makefile).
MUTAGO_VERSION=v2.7.7
mutago_pkg=github.com/quality-gates/mutago/v2/cmd/mutago

# Provision the pinned mutago into a throwaway GOBIN and run that binary directly, so the
# gate needs no mutago on PATH and preserves exact exit codes (see header). Cleaned on any
# exit. `go install pkg@version` is module-independent: it does not read or write go.mod.
mutago_bindir=$(mktemp -d) || {
  echo "mutation gate: could not create temp dir" >&2
  exit 1
}
trap 'rm -rf "$mutago_bindir"' EXIT
if ! GOBIN="$mutago_bindir" go install "${mutago_pkg}@${MUTAGO_VERSION}"; then
  echo "mutation gate: could not install mutago ${MUTAGO_VERSION}" >&2
  exit 1
fi
mutago="$mutago_bindir/mutago"

# A package-path argument (one starting with . or /) selects a full scan of that
# package: the git-diff flags are dropped so the whole package is mutated, not just
# a diff. Otherwise the run is scoped to the branch diff via --git-diff-lines.
has_path=0
for arg in "$@"; do
  [[ "$arg" == .* || "$arg" == /* ]] && has_path=1
done

scope=()
base="${IQ_MUTATION_BASE-origin/main}"
if [[ "$has_path" -eq 0 && -n "$base" ]]; then
  # Diff against the branch point. merge-base is robust to a base ref that has
  # advanced on the remote; fall back to the ref itself, then to a full scan.
  if ref=$(git merge-base "$base" HEAD 2>/dev/null) || ref=$(git rev-parse --verify --quiet "$base"); then
    scope=(--git-diff-lines --git-diff-base "$ref")
  else
    echo "mutation gate: base ref '$base' not found; scanning full scope" >&2
  fi
fi

# When no path is given, mutate the whole module; a path narrows to that package.
targets=("$@")
[[ "$has_path" -eq 0 ]] && targets=(./...)

# Dry run is a mutant-count preview whose cost depends on the form. A package-arg dry
# run is instant and runs no tests; a diff-scoped or whole-module (./...) dry run first
# runs the --coverage instrumented test pass (whole-target, memory-heavy, buffered)
# before counting. The count is a whole-target upper bound either way and ignores
# --git-diff-lines. Scope dry runs to one package and never run one beside a live gate.
if [[ "${IQ_MUTATION_DRYRUN-}" == "1" ]]; then
  if [[ "$has_path" -eq 1 ]]; then
    echo "mutation gate: dry run (instant mutant-count preview, no tests)"
  else
    echo "mutation gate: dry run (runs the --coverage instrumented pass first; whole-target, memory-heavy)"
  fi
  "$mutago" --dry-run "${scope[@]}" "${targets[@]}"
  exit $?
fi

mode=()
if [[ "${IQ_MUTATION_UPDATE_BASELINE-}" == "1" ]]; then
  echo "mutation gate: updating baseline (accepting current survivors, no gate)"
  mode=(--update-baseline)
fi

# Single-mutant diagnostic: mutago runs only this id and suppresses the gate verdict.
mutant=()
if [[ -n "${IQ_MUTATION_MUTANT-}" ]]; then
  echo "mutation gate: re-running single mutant ${IQ_MUTATION_MUTANT} (diagnostic, no gate)"
  mutant=(--run-mutant-id="${IQ_MUTATION_MUTANT}")
fi

"$mutago" \
  --coverage \
  --fail-on-escaped \
  --baseline mutago-baseline.json \
  --logger-agentic-json \
  --ignore-msi-with-no-mutations \
  --workers 1 \
  --timeout-coefficient 20 \
  "${mode[@]}" "${mutant[@]}" "${scope[@]}" "${targets[@]}"
status=$?

# mutago exits 0 (gate passed), 4 (a mutant escaped), or another code for a run
# error; --dry-run and --update-baseline always exit 0. Forward the verdict.
if [[ "$status" -eq 0 ]]; then
  [[ ${#mode[@]} -eq 0 ]] && echo "mutation gate passed: no new escaped mutants"
  exit 0
fi
if [[ "$status" -eq 4 ]]; then
  echo "mutation gate FAILED: a mutant escaped (see the diffs above); kill it or, if it is a genuine equivalent mutant, accept it with IQ_MUTATION_UPDATE_BASELINE=1" >&2
  exit 1
fi
echo "mutation gate: mutago failed to run (exit $status)" >&2
exit 1
