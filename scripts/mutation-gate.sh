#!/usr/bin/env bash
# Mutation gate. gremlins v0.6.0 reports surviving mutants but does not set a
# failing exit code, so this wrapper enforces zero survivors and zero timeouts.
# Serial workers plus a wide timeout keep the Redis-integration runs stable.
#
# Scope. With no argument it scopes to the branch's diff against the base ref
# (IQ_MUTATION_BASE, default main) via `gremlins --diff <merge-base>`, so a change
# is gated only on the lines it touched — the fast path for a feature branch. The
# base resolves to the merge-base with HEAD, so a local base ref that has drifted
# from the remote still yields the true branch point, and it works from a linked
# git worktree (the diff is computed with real git, then handed to gremlins as a
# commit). Pass a package path (e.g. ./cmd) for a full mutation scan of that
# package instead: gremlins v0.6.0 returns zero mutants when --diff is combined
# with a path, so a path and diff-scoping are mutually exclusive. Set
# IQ_MUTATION_BASE= (empty) to mutate the whole module. IQ_MUTATION_DRYRUN=1
# previews the mutant scope without running the tests.
#
# The suite provisions its own containers, but gremlins reruns it per mutant, so
# start a shared stack and point the tests at it to avoid per-mutant churn:
#   docker compose up -d --wait
#   export IQ_REDIS_URL=redis://localhost:6379/0 IQ_MONGO_URL=mongodb://localhost:27017/iq_test
# The Redis integration tests pin themselves to reserved databases (14 and 15),
# so a shared stack seeded on DB 0 by scripts/seed-redis.sh keeps its data through the run.
set -uo pipefail

# A package-path argument (one starting with . or /) selects a full scan of that
# package. --diff must not be combined with it — gremlins v0.6.0 then finds zero
# mutants — so only add --diff when the caller passed neither a path nor their own.
has_path=0
for arg in "$@"; do
  [[ "$arg" == .* || "$arg" == /* ]] && has_path=1
done

scope=()
base="${IQ_MUTATION_BASE-main}"
if [[ "$has_path" -eq 0 && "$*" != *--diff* && -n "$base" ]]; then
  # Diff against the branch point. merge-base is robust to a base ref that has
  # advanced on the remote; fall back to the ref itself, then to a full scan.
  if ref=$(git merge-base "$base" HEAD 2>/dev/null) || ref=$(git rev-parse --verify --quiet "$base"); then
    scope=(--diff "$ref")
  else
    echo "mutation gate: base ref '$base' not found; scanning full scope" >&2
  fi
fi

if [[ "${IQ_MUTATION_DRYRUN-}" == "1" ]]; then
  echo "mutation gate: dry run (scope preview only, no tests executed)"
  gremlins unleash --dry-run "${scope[@]}" "$@"
  exit $?
fi

if ! out=$(gremlins unleash --workers 1 --timeout-coefficient 20 "${scope[@]}" "$@" 2>&1); then
  printf '%s\n' "$out"
  echo "mutation gate: gremlins failed to run" >&2
  exit 1
fi
printf '%s\n' "$out"

lived=$(printf '%s\n' "$out" | grep -oE 'Lived: [0-9]+' | grep -oE '[0-9]+' | head -1)
timedout=$(printf '%s\n' "$out" | grep -oE 'Timed out: [0-9]+' | grep -oE '[0-9]+' | head -1)

if [[ "${lived:-1}" -ne 0 || "${timedout:-1}" -ne 0 ]]; then
  echo "mutation gate FAILED: ${lived:-?} survivor(s), ${timedout:-?} timed out" >&2
  exit 1
fi
echo "mutation gate passed: 0 survivors, 0 timeouts"
