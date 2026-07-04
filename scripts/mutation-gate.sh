#!/usr/bin/env bash
# Mutation gate. gremlins v0.6.0 reports surviving mutants but does not set a
# failing exit code, so this wrapper enforces zero survivors and zero timeouts.
# Serial workers plus a wide timeout keep the Redis-integration runs stable.
# The suite provisions its own containers, but gremlins reruns it per mutant, so
# start a shared stack and point the tests at it to avoid per-mutant churn:
#   docker compose up -d --wait
#   export IQ_REDIS_URL=redis://localhost:6379/0 IQ_MONGO_URL=mongodb://localhost:27017/iq_test
set -uo pipefail

if ! out=$(gremlins unleash --workers 1 --timeout-coefficient 20 "$@" 2>&1); then
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
