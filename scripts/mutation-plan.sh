#!/usr/bin/env bash
# Plans the weekly mutation scan: which packages to scan, cut into shards.
#
#   scripts/mutation-plan.sh <badges-dir> [package...]
#   scripts/mutation-plan.sh --rate <badges-dir> <package>   # print the seconds per mutant
#   scripts/mutation-plan.sh --pack <work-dir>               # pack prepared dry runs (tests)
#
# <badges-dir> holds the last published `badges` branch (state/<slug>.json for each
# package). Without a package argument the plan covers every package with a non-test Go
# file. IQ_MUTATION_FULL=1 scans every package, whatever its state. The script cleans the
# package arguments: it removes a trailing slash and a duplicate, and it stops on a path that
# is not a package of the module.
#
# Output: the shard matrix as one JSON array on stdout, for the CI `deep-mutate` job. Each
# entry is {package, slug, shard, shards, cells}, and each cell is {file, mutator, mutants}.
# The report of the plan goes to stderr.
#
# Stale packages. A package is scanned when the full mode is on, when it has no state, or
# when its fingerprint (scripts/mutation-fingerprint.sh) differs from the stored one. The
# other packages keep their stored result.
#
# Cells. mutago has no shard flag. The unit of work is a (file, mutator) cell, and one gate
# run (scripts/mutation-gate.sh with IQ_MUTATION_MUTATORS and an absolute file target) does
# one cell. The cells come from the per-file counts of a package dry run, which reads
# .mutago.yml like a real run. A dry run count is an upper bound, so the plan is safe.
# Cells with zero mutants are not in the plan. The sum of the cells must be equal to the
# "Total: N" line of the dry run, so a line that the parser does not know stops the plan
# instead of dropping mutants.
#
# Shard size. The budget of a shard is 150 min, which leaves 30 min of the 180 min job for
# the setup. The cost of a cell is (mutants + 1) x seconds per mutant: each gate run also
# does one coverage pass of the package tests. The seconds per mutant come from the state
# of the last scan (secondsPerMutant). A package with no state uses its starting rate from
# the table below, else 180 s for a package that starts a backend in CI and 15 s for the
# other packages. The cells are
# packed largest first, each into the first shard that has room (first-fit decreasing). A
# cell that is larger than the budget gets its own shard and a warning. A cell that is
# larger than 165 min (the 180 min job less the setup) stops the plan, because its shard
# cannot finish. A package with no mutants gets one shard with no cells, so that the
# verdict still records its zero result.
#
# --pack runs only the parse and pack steps on a work directory that holds stale.tsv
# (package, slug, seconds per mutant, reason; tab-separated) and <slug>.dry for each row.
# scripts/test/mutation-verdict.sh uses it, so the test needs no mutago run.
set -euo pipefail

export LC_ALL=C

# Starting rates in seconds per mutant. The plan uses a rate only for a package with no
# state: the secondsPerMutant of the last scan always has priority. Each rate comes from
# one measurement:
#   ./drivers/couchbase     156  CI 2026-08-30: 69 mutants in 180 min
#   ./cmd                    25  local full scans of ./cmd
#   ./drivers/cassandra      20  local: about 4 h for 737 mutants
#   ./drivers/neo4j          21  local: about 4 h for 687 mutants
#   ./drivers/elasticsearch  72  local: about 15 h for 733 mutants
#   ./drivers/file            3  CI: 1417 mutants in 75 min
#   ./internal/query          4  gate 1 of the shard design: 83 cells in about 30 min
#   ./drivers/redis           8  local suite wall time 2026-09-27, doubled for build overhead and CI
#   ./drivers/dynamodb        6  local suite wall time 2026-09-27, doubled for build overhead and CI
#   ./drivers/hbase          26  local suite wall time 2026-09-27, doubled for build overhead and CI
#   ./drivers/couchdb        12  local suite wall time 2026-09-27, doubled for build overhead and CI
#   ./drivers/mongo           5  local suite wall time 2026-09-27, doubled for build overhead and CI
declare -A start_rate=(
  [./drivers/couchbase]=156
  [./cmd]=25
  [./drivers/cassandra]=20
  [./drivers/neo4j]=21
  [./drivers/elasticsearch]=72
  [./drivers/file]=3
  [./internal/query]=4
  [./drivers/redis]=8
  [./drivers/dynamodb]=6
  [./drivers/hbase]=26
  [./drivers/couchdb]=12
  [./drivers/mongo]=5
)

# Packages that start a backend in the CI `deep-mutate` job. Keep this list in step with
# the case block of that job in .github/workflows/ci.yml. With no state and no starting
# rate, such a package uses 180 s per mutant, and any other package 15 s.
backend_packages=" ./cmd ./drivers/redis ./drivers/mongo ./drivers/cassandra ./drivers/dynamodb ./drivers/hbase ./drivers/couchdb ./drivers/couchbase ./drivers/neo4j ./drivers/elasticsearch "

# rate <badges-dir> <package> <slug>: the seconds per mutant for the plan of a package.
rate() {
  local state="$1/state/$3.json" spm=""
  [[ -f "$state" ]] && spm=$(jq -r '.secondsPerMutant // "" | tostring' "$state")
  if [[ "$spm" =~ ^[0-9]+(\.[0-9]+)?$ ]] && [[ ! "$spm" =~ ^0+(\.0+)?$ ]]; then
    echo "$spm"
  elif [[ -n "${start_rate[$2]-}" ]]; then
    echo "${start_rate[$2]}"
  elif [[ "$backend_packages" == *" $2 "* ]]; then
    echo 180
  else
    echo 15
  fi
}

slug_of() {
  local slug
  slug=$(printf '%s' "$1" | tr '/.' '--' | sed 's/^-*//')
  echo "${slug:-root}"
}

# pack <work-dir>: parse the dry runs of the stale packages and pack their cells into
# shards. Prints the matrix on stdout.
pack() {
python3 - "$1" <<'PY'
import json
import os
import re
import sys

work = sys.argv[1]
BUDGET = 150 * 60
JOB_LIMIT = 165 * 60  # the 180 min job less the setup
MAX_JOBS = 256  # the GitHub limit for one matrix

matrix = []
too_large = []
for row in open(os.path.join(work, "stale.tsv"), encoding="utf-8"):
    package, slug, spm, reason = row.rstrip("\n").split("\t")
    spm = float(spm)
    cells = []
    current = None
    total = None
    in_files = True
    for line in open(os.path.join(work, slug + ".dry"), encoding="utf-8"):
        line = line.rstrip("\n")
        if line.startswith("Total: "):
            match = re.match(r"^Total: (\d+) mutation", line)
            if not match:
                sys.exit("mutation-plan: cannot read the dry run line {!r} of {}".format(line, package))
            total = int(match.group(1))
            continue
        if line.startswith("Per-mutator totals"):
            in_files = False
            continue
        if not in_files:
            continue
        if line.endswith(".go:") and not line.startswith(("\t", " ")):
            current = line[:-1]
            continue
        if line.startswith("\t") and current:
            name, _, count = line.strip().rpartition(": ")
            if not count.isdigit():
                sys.exit("mutation-plan: cannot read the dry run line {!r} of {}".format(line, package))
            if int(count) > 0:
                cells.append({"file": current, "mutator": name, "mutants": int(count)})
    if total is None:
        sys.exit("mutation-plan: the dry run of {} has no Total line".format(package))
    parsed = sum(c["mutants"] for c in cells)
    if total > 0 and not cells:
        sys.exit("mutation-plan: the dry run of {} has {} mutants but no per-file cells".format(package, total))
    if parsed != total:
        sys.exit("mutation-plan: the cells of {} hold {} mutants, but the dry run Total is {}".format(package, parsed, total))
    for cell in cells:
        if os.path.isabs(cell["file"]) or ".." in cell["file"].split("/"):
            sys.exit("mutation-plan: the dry run of {} names the file {!r}".format(package, cell["file"]))

    def cost(cell):
        return (cell["mutants"] + 1) * spm

    cells.sort(key=lambda c: (-cost(c), c["file"], c["mutator"]))
    shards = []
    for cell in cells:
        size = cost(cell)
        if size > JOB_LIMIT:
            too_large.append("{} {} x {} needs about {:.0f} min".format(package, cell["file"], cell["mutator"], size / 60))
            continue
        if size > BUDGET:
            print("mutation-plan: WARNING {} {} x {} needs about {:.0f} min, more than the budget of {} min; it gets its own shard".format(
                package, cell["file"], cell["mutator"], size / 60, BUDGET // 60), file=sys.stderr)
            shards.append({"cells": [cell], "seconds": size})
            continue
        for shard in shards:
            if shard["seconds"] + size <= BUDGET:
                shard["cells"].append(cell)
                shard["seconds"] += size
                break
        else:
            shards.append({"cells": [cell], "seconds": size})
    if not shards:
        shards = [{"cells": [], "seconds": 0}]
    mutants = sum(c["mutants"] for c in cells)
    longest = max(s["seconds"] for s in shards) / 60
    print("mutation-plan: {} ({}): {} mutants in {} cells, {} shard(s), longest about {:.0f} min at {:g} s per mutant".format(
        package, reason, mutants, len(cells), len(shards), longest, spm), file=sys.stderr)
    for index, shard in enumerate(shards, start=1):
        matrix.append({
            "package": package,
            "slug": slug,
            "shard": index,
            "shards": len(shards),
            "cells": sorted(shard["cells"], key=lambda c: (c["file"], c["mutator"])),
        })

if too_large:
    for item in too_large:
        print("mutation-plan: {}, more than the job limit of {} min".format(item, JOB_LIMIT // 60), file=sys.stderr)
    sys.exit("mutation-plan: {} cell(s) cannot finish in one job; measure the rate of the package or split the file".format(len(too_large)))
if len(matrix) > MAX_JOBS:
    sys.exit("mutation-plan: {} shard jobs is more than the GitHub matrix limit of {}".format(len(matrix), MAX_JOBS))
print("mutation-plan: {} shard job(s)".format(len(matrix)), file=sys.stderr)
json.dump(matrix, sys.stdout, separators=(",", ":"))
sys.stdout.write("\n")
PY
}

if [[ "${1-}" == "--pack" ]]; then
  [[ $# -eq 2 && -f "$2/stale.tsv" ]] || {
    echo "usage: mutation-plan.sh --pack <work-dir with stale.tsv>" >&2
    exit 1
  }
  pack "$2"
  exit 0
fi

if [[ "${1-}" == "--rate" ]]; then
  [[ $# -eq 3 ]] || {
    echo "usage: mutation-plan.sh --rate <badges-dir> <package>" >&2
    exit 1
  }
  rate "$2" "$3" "$(slug_of "$3")"
  exit 0
fi

badges="${1:?usage: mutation-plan.sh <badges-dir> [package...]}"
shift
cd "$(git rev-parse --show-toplevel)" || exit 1

module=$(go list -m)
module_packages=$(go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... | sed "s|^${module}|.|")
if [[ -z "$module_packages" ]]; then
  echo "mutation-plan: go list found no package in the module" >&2
  exit 1
fi
packages=()
if [[ $# -gt 0 ]]; then
  # Clean the arguments: remove a trailing slash and a duplicate, and accept only a
  # package of the module list above.
  for arg in "$@"; do
    pkg="$arg"
    while [[ "$pkg" == */ && "$pkg" != "./" ]]; do pkg="${pkg%/}"; done
    [[ "$pkg" == "./" ]] && pkg="."
    if [[ "$pkg" != "." && ! "$pkg" =~ ^\./[A-Za-z0-9_][A-Za-z0-9_./-]*$ ]] || [[ "$pkg" == *..* ]]; then
      echo "mutation-plan: '$arg' is not a package path (. or ./dir)" >&2
      exit 1
    fi
    if ! grep -qxF -- "$pkg" <<<"$module_packages"; then
      echo "mutation-plan: '$arg' is not a package of $module with a non-test Go file" >&2
      exit 1
    fi
    seen=0
    for have in "${packages[@]}"; do [[ "$have" == "$pkg" ]] && seen=1; done
    [[ "$seen" -eq 1 ]] || packages+=("$pkg")
  done
else
  mapfile -t packages <<<"$module_packages"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Install mutago once for all the dry runs.
mutago_bin=$(IQ_MUTATION_INSTALL_DIR="$work/bin" bash scripts/mutation-gate.sh | tail -n 1)

full="${IQ_MUTATION_FULL-}"
for pkg in "${packages[@]}"; do
  slug=$(slug_of "$pkg")
  fingerprint=$(bash scripts/mutation-fingerprint.sh "$pkg")
  state="$badges/state/$slug.json"
  reason=""
  if [[ "$full" == "1" ]]; then
    reason="full scan"
  elif [[ ! -f "$state" ]]; then
    reason="no state"
  elif [[ "$(jq -r '.fingerprint // ""' "$state")" != "$fingerprint" ]]; then
    reason="fingerprint changed"
  fi
  if [[ -z "$reason" ]]; then
    echo "mutation-plan: $pkg reuses its state" >&2
    continue
  fi
  spm=$(rate "$badges" "$pkg" "$slug")
  if ! IQ_MUTATION_MUTAGO_BIN="$mutago_bin" IQ_MUTATION_DRYRUN=1 bash scripts/mutation-gate.sh "$pkg" >"$work/$slug.dry" 2>&1; then
    cat "$work/$slug.dry" >&2
    echo "mutation-plan: the dry run of $pkg failed" >&2
    exit 1
  fi
  printf '%s\t%s\t%s\t%s\n' "$pkg" "$slug" "$spm" "$reason" >>"$work/stale.tsv"
done

if [[ ! -f "$work/stale.tsv" ]]; then
  echo "mutation-plan: every package reuses its state; nothing to scan" >&2
  echo "[]"
  exit 0
fi

pack "$work"
