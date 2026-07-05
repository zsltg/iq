#!/usr/bin/env bash
# Mutation gate. gremlins v0.6.0 reports surviving mutants but does not set a
# failing exit code, so this wrapper enforces zero survivors and zero timeouts.
# Serial workers plus a wide timeout keep the Redis-integration runs stable.
# By default it scopes to the current branch's diff against the base ref
# (IQ_MUTATION_BASE, default main) via gremlins --diff, so a change is gated only
# on the lines it touched — the fast path for a feature branch. Widen with an
# explicit path argument, pass your own --diff, or set IQ_MUTATION_BASE= (empty)
# to mutate the full scope.
# The suite provisions its own containers, but gremlins reruns it per mutant, so
# start a shared stack and point the tests at it to avoid per-mutant churn:
#   docker compose up -d --wait
#   export IQ_REDIS_URL=redis://localhost:6379/0 IQ_MONGO_URL=mongodb://localhost:27017/iq_test
# The Redis integration tests pin themselves to reserved databases (14 and 15),
# so a shared stack seeded on DB 0 by scripts/seed.sh keeps its data through the run.
set -uo pipefail

# Scope to the branch diff unless the caller supplied their own --diff or cleared
# the base. A base ref that does not resolve falls back to a full scan, loudly.
scope=()
base="${IQ_MUTATION_BASE-main}"
if [[ -n "$base" && "$*" != *--diff* ]]; then
  if git rev-parse --verify --quiet "$base" >/dev/null; then
    scope=(--diff "$base")
  else
    echo "mutation gate: base ref '$base' not found; scanning full scope" >&2
  fi
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
