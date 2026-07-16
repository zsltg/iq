#!/usr/bin/env bash
# Mutation gate. Runs github.com/quality-gates/mutago and lets it exit-code-enforce:
# --fail-on-escaped exits 4 the moment a mutant survives a covered test, so this
# wrapper only shapes the scope and forwards the exit code. Serial workers plus a
# wide per-mutant timeout keep the container-integration runs stable.
#
# What counts as a failure. --fail-on-escaped fails on any *escaped* mutant that is
# not recorded in the baseline (mutago-baseline.json); an escaped mutant means a
# covered test asserts nothing, so strengthen the test. --coverage keeps uncovered
# lines out of the escaped set (they become "not covered", never a failure), so the
# gate mirrors the old zero-survivor-on-covered-code contract. Timed-out mutants are
# reported as "errored" and are *not* gated — the wide --timeout-coefficient keeps a
# legitimately slow suite from erroring; a genuine hang still shows up in the output.
#
# Scope. With no argument it scopes to the branch's diff against the base ref
# (IQ_MUTATION_BASE, default origin/main) via mutago's --git-diff-lines, so a change
# is gated only on the lines it touched — the fast path for a feature branch. The
# base is handed to mutago as the merge-base *commit* with HEAD, so a local base ref
# that has drifted from the remote still yields the true branch point, and it works
# from a linked git worktree (git computes the diff, mutago restricts to it). Pass a
# package path (e.g. ./cmd) for a full mutation scan of that package instead: a path
# drops the diff-scoping flags and mutates the whole package. Set IQ_MUTATION_BASE=
# (empty) to mutate the whole module. IQ_MUTATION_DRYRUN=1 prints the mutant counts
# per file and mutator without running the tests (a whole-target upper bound —
# mutago's --dry-run ignores diff scoping). IQ_MUTATION_UPDATE_BASELINE=1 records the current
# survivors into mutago-baseline.json and exits 0 (accept genuine equivalent mutants
# deliberately, then commit the file with justification).
#
# The suite provisions its own containers, but mutago reruns it per mutant, so start
# a shared stack and point the tests at it to avoid per-mutant churn:
#   docker compose up -d --wait
#   export IQ_REDIS_URL=redis://localhost:6379/0 IQ_MONGO_URL=mongodb://localhost:27017/iq_test
# The Redis integration tests pin themselves to reserved databases (14 and 15),
# so a shared stack seeded on DB 0 by scripts/seed-redis.sh keeps its data through the run.
set -uo pipefail

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

# Dry run is a fast count preview: enumerate the mutations without --coverage (which
# would run the whole suite to build a profile) and without the gate. mutago's
# --dry-run counts the whole target (an upper bound) and ignores --git-diff-lines,
# so it is not diff-scoped even when the base ref is set.
if [[ "${IQ_MUTATION_DRYRUN-}" == "1" ]]; then
  echo "mutation gate: dry run (mutant counts only, no tests executed)"
  mutago --dry-run "${scope[@]}" "${targets[@]}"
  exit $?
fi

mode=()
if [[ "${IQ_MUTATION_UPDATE_BASELINE-}" == "1" ]]; then
  echo "mutation gate: updating baseline (accepting current survivors, no gate)"
  mode=(--update-baseline)
fi

mutago \
  --coverage \
  --fail-on-escaped \
  --baseline mutago-baseline.json \
  --ignore-msi-with-no-mutations \
  --workers 1 \
  --timeout-coefficient 20 \
  "${mode[@]}" "${scope[@]}" "${targets[@]}"
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
