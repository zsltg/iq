#!/usr/bin/env bash
# Mutation gate. Runs github.com/quality-gates/mutago and lets it exit-code-enforce:
# --fail-on-escaped exits 4 the moment a mutant survives a covered test, so this
# wrapper only shapes the scope and forwards the exit code. Serial workers plus a
# bounded per-mutant timeout keep the container-integration runs stable.
#
# Version-hermetic invocation. mutago is pinned by MUTAGO_VERSION (below) and provisioned
# per run into a throwaway GOBIN via `go install <pinned>@version`, then executed
# directly. This removes any dependency on a mutago binary on PATH (the gate no longer
# needs `make tools-dev`) and never touches this module's go.mod. Direct execution is
# deliberate over `go run`: `go run` collapses a non-zero program exit to 1, which would
# make an escaped-mutant verdict (exit 4) indistinguishable from a run error, whereas the
# installed binary preserves mutago's exact exit codes and streams output live.
#
# Stable policy lives in the committed .mutago.yml (passed via --config on the gate
# invocation); the invocation-varying and load-bearing flags stay on the command line
# here. The baseline path stays a CLI flag because v2.7.7 has no config key for it.
#
# What counts as a failure. --fail-on-escaped fails on any *escaped* mutant that is
# not recorded in the baseline (mutago-baseline.json); an escaped mutant means a
# covered test asserts nothing, so strengthen the test. --coverage keeps uncovered
# lines out of the escaped set (they become "not covered", never a failure), so the
# gate mirrors the old zero-survivor-on-covered-code contract. An errored mutant — most
# often one that timed out — is *not* gated by mutago itself: its MSI arithmetic counts an
# error as a kill, so a hung mutant would pass silently. This wrapper therefore reads
# report.json after a gate run and fails on stats.errorCount > 0, naming each errored
# mutant. That turns the tail from a silent hole into a signal, which is what lets
# --timeout-coefficient stay tight (5; raise it for a legitimately slow package with
# IQ_MUTATION_TIMEOUT_COEFFICIENT, a positive integer multiplier of the instrumented
# baseline). The check is skipped in the non-gating modes (--update-baseline, single-mutant).
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
# from a linked git worktree (git computes the diff, mutago restricts to it). A
# diff-scoped run also narrows its *targets* to the packages holding changed .go files
# instead of enumerating ./...: changed lines are a subset of changed files, which are a
# subset of changed packages, so the mutant set is identical while the enumeration pass —
# the memory peak of a run, since mutago loads and instruments every target package —
# shrinks to what the branch touched. An empty derivation falls back to ./...; a run
# never starts with no targets. Pass a package path (e.g. ./cmd) for a full mutation
# scan of that package instead: a path drops the diff-scoping flags and mutates the
# whole package. Set IQ_MUTATION_BASE= (empty) to mutate the whole module.
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

# Per-mutant timeout, as a multiplier of the instrumented baseline. 5 is tight enough that
# an infinite-loop mutant dies in minutes rather than tens of them; the errored-mutant check
# after the run is what keeps that bound from hiding a legitimately slow suite. Raise it when
# a real suite needs the room. Validated before any work: a non-positive or non-numeric
# value is a stop, never a silent fallback to the default.
timeout_coefficient="${IQ_MUTATION_TIMEOUT_COEFFICIENT-5}"
if [[ ! "$timeout_coefficient" =~ ^[1-9][0-9]*$ ]]; then
  echo "mutation gate: IQ_MUTATION_TIMEOUT_COEFFICIENT must be a positive integer, got '$timeout_coefficient'" >&2
  exit 1
fi

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
diff_ref=""
base="${IQ_MUTATION_BASE-origin/main}"
if [[ "$has_path" -eq 0 && -n "$base" ]]; then
  # Diff against the branch point. merge-base is robust to a base ref that has
  # advanced on the remote; fall back to the ref itself, then to a full scan.
  if ref=$(git merge-base "$base" HEAD 2>/dev/null) || ref=$(git rev-parse --verify --quiet "$base"); then
    scope=(--git-diff-lines --git-diff-base "$ref")
    diff_ref="$ref"
  else
    echo "mutation gate: base ref '$base' not found; scanning full scope" >&2
  fi
fi

# Targets. A path argument narrows to that package; otherwise the default is the whole
# module. A diff-scoped run instead enumerates only the packages holding changed .go
# files: --git-diff-lines already restricts *mutation* to changed lines, and changed
# lines ⊆ changed files ⊆ changed packages, so the mutant set is provably identical —
# only the enumeration shrinks, and enumeration is what pins memory on a whole-module
# target. Deleted files are excluded (--diff-filter=d); go list drops any directory that
# is not a package (testdata, fixtures) so a stray path cannot abort the run.
targets=("$@")
if [[ "$has_path" -eq 0 ]]; then
  targets=(./...)
  if [[ -n "$diff_ref" ]]; then
    mapfile -t changed_dirs < <(
      git diff --name-only --diff-filter=d "$diff_ref" -- '*.go' |
        while IFS= read -r file; do dirname "$file"; done | sort -u
    )
    changed_pkgs=()
    for dir in "${changed_dirs[@]}"; do
      [[ -n "$dir" ]] || continue
      # A repo-root file yields ".", which is already a valid package path.
      pkg="./$dir"
      [[ "$dir" == "." ]] && pkg="."
      go list "$pkg" >/dev/null 2>&1 && changed_pkgs+=("$pkg")
    done
    if [[ ${#changed_pkgs[@]} -gt 0 ]]; then
      targets=("${changed_pkgs[@]}")
      echo "mutation gate: enumerating ${#changed_pkgs[@]} changed package(s): ${changed_pkgs[*]}"
    else
      echo "mutation gate: no changed Go packages resolved; enumerating the whole module"
    fi
  fi
fi

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
  --config .mutago.yml \
  --coverage \
  --fail-on-escaped \
  --baseline mutago-baseline.json \
  --logger-agentic-json \
  --ignore-msi-with-no-mutations \
  --workers 1 \
  --timeout-coefficient "$timeout_coefficient" \
  "${mode[@]}" "${mutant[@]}" "${scope[@]}" "${targets[@]}"
status=$?

# Errored mutants are not gated by mutago: its MSI arithmetic counts an error as a kill, so
# a mutant that hung or crashed the suite would pass silently — exactly the hole a tight
# --timeout-coefficient would otherwise widen. In gate mode only (--update-baseline and the
# single-mutant diagnostic are deliberately non-gating), read report.json and fail on any
# error, naming each one. A missing or unreadable report is a warning, never a crash: the
# code mutago already returned stays the verdict. python3 over jq: it ships with the box.
if [[ ${#mode[@]} -eq 0 && ${#mutant[@]} -eq 0 ]]; then
  errored=$(
    python3 - <<'PY'
import json
import sys

try:
    with open("report.json", encoding="utf-8") as handle:
        report = json.load(handle)
except (OSError, ValueError):
    sys.exit(2)

if int(report.get("stats", {}).get("errorCount", 0)) <= 0:
    sys.exit(0)

for mutant in report.get("errored") or []:
    mutator = mutant.get("mutator") or {}
    print("  {}:{} {}".format(
        mutator.get("originalFilePath", "?"),
        mutator.get("originalStartLine", "?"),
        mutator.get("mutatorName", "?"),
    ))
sys.exit(1)
PY
  )
  report_status=$?
  if [[ "$report_status" -eq 1 ]]; then
    echo "mutation gate FAILED: a mutant errored or timed out. mutago does not gate these (it scores an error as a kill), so the wrapper fails the run — an errored mutant is unverified, not killed. Fix the hang, or raise IQ_MUTATION_TIMEOUT_COEFFICIENT (default 5) if the suite is legitimately slow:" >&2
    [[ -n "$errored" ]] && echo "$errored" >&2
    [[ "$status" -eq 0 ]] && exit 1
  elif [[ "$report_status" -ne 0 ]]; then
    echo "mutation gate: could not read report.json; errored mutants were not checked" >&2
  fi
fi

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
