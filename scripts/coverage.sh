#!/usr/bin/env bash
# Test coverage: measure, report per-package + total, and enforce a no-regression
# floor. Defaults to the full suite (needs Docker, or point IQ_*_URL at a running
# stack — the same convention as scripts/mutation-gate.sh). IQ_COVER_SHORT=1 runs
# the fast -short path report-only: the container-backed driver paths skip, so it
# understates coverage and never gates. The floor is IQ_COVER_MIN (default 80,
# just under the measured full-suite baseline); set it empty to disable. HBase
# counts low unless IQ_HBASE_URL points at a running cluster. HTML report:
# `go tool cover -html=coverage.out`.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

profile=coverage.out
min="${IQ_COVER_MIN-80}"

if [[ "${IQ_COVER_SHORT-}" == "1" ]]; then
  echo "coverage: -short (report only; understates container-backed drivers)"
  go test -short -covermode=atomic -coverprofile="$profile" ./... || exit 1
  gate=0
else
  echo "coverage: full suite (ephemeral containers unless IQ_*_URL is set)"
  go test -covermode=atomic -coverprofile="$profile" ./... || exit 1
  gate=1
fi

# Per-package statement coverage, computed from the profile so the suite runs once.
# Profile lines are "path:start.col,end.col numStatements executionCount".
echo
echo "== per-package =="
awk '
  NR > 1 {
    path = $1; sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", path)
    pkg = path; sub(/\/[^/]+$/, "", pkg); sub("github.com/zsltg/iq/", "", pkg)
    total[pkg] += $2
    if ($3 + 0 > 0) covered[pkg] += $2
  }
  END {
    for (p in total) printf "%6.1f%%  %s\n", (total[p] ? 100 * covered[p] / total[p] : 0), p
  }
' "$profile" | sort -n

total="$(go tool cover -func="$profile" | tail -1 | awk '{print $NF}')"
echo
echo "== total: $total =="

if [[ -n "$min" && "$gate" -eq 1 ]]; then
  num="${total%\%}"
  if awk -v t="$num" -v m="$min" 'BEGIN { exit !(t + 0 < m + 0) }'; then
    echo "coverage: FAILED — total $total is below the ${min}% floor (raise coverage or adjust IQ_COVER_MIN)" >&2
    exit 1
  fi
  echo "coverage: passed — total $total meets the ${min}% floor"
fi
