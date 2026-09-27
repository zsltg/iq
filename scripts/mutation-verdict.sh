#!/usr/bin/env bash
# Gives the verdict of the weekly mutation scan and updates the stored state.
#
#   scripts/mutation-verdict.sh <artifacts-dir> <badges-dir> <matrix.json> <packages.txt>
#
# <artifacts-dir>  the shard artifacts of scripts/mutation-shard.sh (any depth); it can be
#                  empty when the plan found nothing to scan
# <badges-dir>     the tree of the `badges` branch: state/<slug>.json and mutation.json;
#                  the script updates it in place
# <matrix.json>    the matrix of scripts/mutation-plan.sh for this run
# <packages.txt>   every package of the module, one for each line
#
# The CI `deep-badge` job runs it. The steps:
#
# 1. Shard checks. Each matrix entry must have exactly one shard report, with the same
#    cells, no failed cell, and a report for each cell. A shard report that is not in the
#    matrix, or that occurs two times, stops the verdict. Each shard must have the identity
#    of this checkout: the commit, the mutago version, the Go version, and the sha256 of
#    .mutago.yml and mutago-baseline.json.
# 2. The gate. For each scanned package, scripts/mutation-merge.sh merges the cells of all
#    its shards (each edit counts once; an escape is new only if none of its ids is in the
#    baseline). An errored mutant fails every package. A new escape fails every package
#    except ./cmd. ./cmd passes on the covered-code floor of scripts/mutation-gate.sh
#    (cmd_covered_msi_floor, the one source of that value), which applies to the merged
#    ./cmd result only. A kill in one shard and an escape in another for the same edit is
#    a sign of a flaky test; the merge keeps the escape, and the verdict prints a warning.
# 3. State. When the gate passes, the script writes state/<slug>.json for each scanned
#    package: package, slug, fingerprint, commit, scannedAt, secondsPerMutant (the wall
#    seconds of its shards over its mutants), and summary. A failed gate writes nothing,
#    so the previous state and badge stay.
# 4. Pruning. A state file of a package that is not in <packages.txt> is removed.
# 5. Module summary. If each package has state with its current fingerprint, the script
#    sums the summaries with scripts/mutation-summary.sh (mutago-summary.json in the
#    current directory) and writes <badges-dir>/mutation.json, the shields.io endpoint of
#    the README badge. If a package has no current state, it removes mutago-summary.json
#    and keeps the previous mutation.json, so a partial scan never publishes a score.
#
# Exit status: 0 when the gate passes (with or without a new badge), 1 when it fails.
set -uo pipefail

export LC_ALL=C

usage="usage: mutation-verdict.sh <artifacts-dir> <badges-dir> <matrix.json> <packages.txt>"
artifacts="${1:?$usage}"
badges="${2:?$usage}"
matrix="${3:?$usage}"
packages_file="${4:?$usage}"

for path in "$artifacts" "$badges"; do
  [[ -d "$path" ]] || {
    echo "mutation-verdict: '$path' is not a directory" >&2
    exit 1
  }
done
for path in "$matrix" "$packages_file"; do
  [[ -f "$path" ]] || {
    echo "mutation-verdict: '$path' is not a file" >&2
    exit 1
  }
done
artifacts=$(cd "$artifacts" && pwd -P)
badges=$(cd "$badges" && pwd -P)
matrix=$(cd "$(dirname "$matrix")" && pwd -P)/$(basename "$matrix")
packages_file=$(cd "$(dirname "$packages_file")" && pwd -P)/$(basename "$packages_file")

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1

floor=$(sed -n 's/^cmd_covered_msi_floor=//p' scripts/mutation-gate.sh)
if [[ ! "$floor" =~ ^[0-9]+$ ]]; then
  echo "mutation-verdict: cannot read cmd_covered_msi_floor from scripts/mutation-gate.sh" >&2
  exit 1
fi

work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

sha() { sha256sum "$1" | cut -d' ' -f1; }
jq -n \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg go "$(go env GOVERSION)" \
  --arg config "$(sha .mutago.yml)" \
  --arg baseline "$(sha mutago-baseline.json)" \
  '{commit: $commit, mutagoVersion: $mutago, goVersion: $go,
    configSha256: $config, baselineSha256: $baseline}' >"$work/identity.json"

# Step 1: the shard checks. The output is one line for each scanned package:
# package, slug, seconds, and the cell directories.
if ! python3 - "$artifacts" "$matrix" "$work/identity.json" >"$work/scanned.tsv" <<'PY'; then
import json
import os
import sys

artifacts, matrix_path, identity_path = sys.argv[1:4]
problems = []


def load(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, ValueError) as err:
        problems.append("cannot read {}: {}".format(path, err))
        return None


identity = load(identity_path)
matrix = load(matrix_path)
if not isinstance(matrix, list):
    print("mutation-verdict: the matrix is not a JSON array", file=sys.stderr)
    sys.exit(1)

expected = {}
for entry in matrix:
    key = (entry["package"], entry["shard"])
    if key in expected:
        problems.append("the matrix holds {} shard {} two times".format(*key))
    expected[key] = entry

reports = {}
for folder, _, files in sorted(os.walk(artifacts)):
    if "shard.json" not in files:
        continue
    report = load(os.path.join(folder, "shard.json"))
    if report is None:
        continue
    key = (report.get("package"), report.get("shard"))
    if key in reports:
        problems.append("two reports for {} shard {}: {} and {}".format(key[0], key[1], reports[key][0], folder))
        continue
    reports[key] = (folder, report)

for key, (folder, report) in sorted(reports.items(), key=lambda item: str(item[0])):
    entry = expected.get(key)
    if entry is None:
        problems.append("{} holds {} shard {}, which is not in the matrix".format(folder, key[0], key[1]))
        continue
    if report.get("identity") != identity:
        problems.append("{} shard {} has the identity {}, not {}".format(key[0], key[1], json.dumps(report.get("identity")), json.dumps(identity)))
    if report.get("shards") != entry["shards"] or report.get("slug") != entry["slug"]:
        problems.append("{} shard {} has shards={} slug={}, the matrix has shards={} slug={}".format(
            key[0], key[1], report.get("shards"), report.get("slug"), entry["shards"], entry["slug"]))
    want = [[c["file"], c["mutator"]] for c in entry["cells"]]
    got = [[c.get("file"), c.get("mutator")] for c in report.get("cells") or []]
    if got != want:
        problems.append("{} shard {} did not run the cells of the matrix".format(*key))
    if report.get("failedCells") != 0:
        problems.append("{} shard {} has {} failed cell(s)".format(key[0], key[1], report.get("failedCells")))
    for index in range(1, len(want) + 1):
        if not os.path.isfile(os.path.join(folder, "cells", "{:03d}".format(index), "report.json")):
            problems.append("{} shard {} has no report for cell {}".format(key[0], key[1], index))

for key in sorted(expected, key=str):
    if key not in reports:
        problems.append("no report for {} shard {}".format(*key))

if problems:
    for problem in problems:
        print("mutation-verdict: " + problem, file=sys.stderr)
    sys.exit(1)

packages = {}
for key, (folder, report) in reports.items():
    item = packages.setdefault(key[0], {"slug": report["slug"], "seconds": 0, "cells": []})
    item["seconds"] += int(report.get("seconds") or 0)
    for index in range(1, len(report["cells"]) + 1):
        item["cells"].append(os.path.join(folder, "cells", "{:03d}".format(index)))
for package in sorted(packages):
    item = packages[package]
    print("\t".join([package, item["slug"], str(item["seconds"])] + sorted(item["cells"])))
PY
  echo "mutation-verdict: FAILED: the shard reports are missing, extra, or do not match this run" >&2
  exit 1
fi

# Step 2: the gate, one merge for each scanned package.
failed=0
mkdir -p "$work/merged"
while IFS=$'\t' read -r -a row; do
  [[ ${#row[@]} -ge 3 ]] || continue
  pkg="${row[0]}"
  slug="${row[1]}"
  cells=("${row[@]:3}")
  merged="$work/merged/$slug.json"
  if [[ ${#cells[@]} -eq 0 ]]; then
    echo '{"totalMutantsCount":0,"killedCount":0,"notCoveredCount":0,"escapedCount":0,"errorCount":0,"skippedCount":0,"score":0,"escapedChecksums":[],"newEscapes":[],"conflicts":0}' >"$merged"
  else
    bash scripts/mutation-merge.sh mutago-baseline.json "${cells[@]}" >"$merged"
    status=$?
    if [[ "$status" -ne 0 && "$status" -ne 4 ]]; then
      echo "mutation-verdict: FAILED: $pkg: the merge could not read the shard reports" >&2
      failed=1
      continue
    fi
  fi
  read -r total killed escaped errors new conflicts score < <(jq -r \
    '[.totalMutantsCount, .killedCount, .escapedCount, .errorCount, (.newEscapes | length), .conflicts, .score] | @tsv' "$merged")
  echo "mutation-verdict: $pkg: $total mutants, $killed killed, $escaped escaped, $new new, $errors errored, score $score"
  if [[ "$conflicts" -ne 0 ]]; then
    echo "mutation-verdict: WARNING: $pkg: $conflicts edit(s) have different results in different shards (a flaky test can cause a false kill); see the merge lines above" >&2
  fi
  if [[ "$errors" -ne 0 ]]; then
    echo "mutation-verdict: FAILED: $pkg: $errors mutant(s) errored or timed out; an errored mutant is not verified" >&2
    failed=1
  elif [[ "$pkg" == "./cmd" ]]; then
    if [[ $((killed + escaped)) -gt 0 ]] && python3 -c 'import sys; sys.exit(0 if float(sys.argv[1]) * 100 < float(sys.argv[2]) else 1)' "$score" "$floor"; then
      echo "mutation-verdict: FAILED: ./cmd scores $score, under the floor of $floor% (killed / (killed + escaped)); kill escaped mutants, never lower the floor" >&2
      failed=1
    fi
  elif [[ "$new" -ne 0 ]]; then
    echo "mutation-verdict: FAILED: $pkg: $new new escaped mutant(s); kill them or accept a genuine equivalent into mutago-baseline.json" >&2
    failed=1
  fi
done <"$work/scanned.tsv"

if [[ "$failed" -ne 0 ]]; then
  echo "mutation-verdict: FAILED; the state and the badge do not change" >&2
  exit 1
fi

# Step 3: the state of each scanned package.
mkdir -p "$badges/state"
scanned_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
commit=$(jq -r .commit "$work/identity.json")
while IFS=$'\t' read -r -a row; do
  [[ ${#row[@]} -ge 3 ]] || continue
  pkg="${row[0]}"
  slug="${row[1]}"
  seconds="${row[2]}"
  jq --arg package "$pkg" --arg slug "$slug" \
    --arg fingerprint "$(bash scripts/mutation-fingerprint.sh "$pkg")" \
    --arg commit "$commit" --arg scannedAt "$scanned_at" --argjson seconds "$seconds" \
    '{package: $package, slug: $slug, fingerprint: $fingerprint, commit: $commit,
      scannedAt: $scannedAt,
      secondsPerMutant: (if .totalMutantsCount > 0 then ($seconds / .totalMutantsCount * 10 | round / 10) else null end),
      summary: {totalMutantsCount, killedCount, notCoveredCount, escapedCount, errorCount,
                skippedCount, score}}' \
    "$work/merged/$slug.json" >"$badges/state/$slug.json"
done <"$work/scanned.tsv"

# Step 4: remove the state of a package that the module no longer has.
mapfile -t module_packages < <(grep -v '^[[:space:]]*$' "$packages_file")
for state in "$badges"/state/*.json; do
  [[ -f "$state" ]] || continue
  pkg=$(jq -r '.package // ""' "$state" 2>/dev/null)
  keep=0
  for want in "${module_packages[@]}"; do
    [[ "$pkg" == "$want" ]] && keep=1 && break
  done
  if [[ "$keep" -eq 0 ]]; then
    echo "mutation-verdict: remove the state of '$pkg' ($(basename "$state")), which is not a package of the module"
    rm -f "$state"
  fi
done

# Step 5: the module summary and the badge, only when each package has current state.
missing=()
mkdir -p "$work/summaries"
for pkg in "${module_packages[@]}"; do
  slug=$(printf '%s' "$pkg" | tr '/.' '--' | sed 's/^-*//')
  [[ -n "$slug" ]] || slug=root
  state="$badges/state/$slug.json"
  if [[ ! -f "$state" ]]; then
    missing+=("$pkg (no state)")
    continue
  fi
  if [[ "$(jq -r '.fingerprint // ""' "$state")" != "$(bash scripts/mutation-fingerprint.sh "$pkg")" ]]; then
    missing+=("$pkg (state is not current)")
    continue
  fi
  mkdir -p "$work/summaries/$slug"
  jq '.summary' "$state" >"$work/summaries/$slug/mutago-summary.json"
done
if [[ ${#missing[@]} -gt 0 ]]; then
  rm -f mutago-summary.json
  echo "mutation-verdict: gate passed; ${#missing[@]} package(s) have no current state, so the badge keeps its previous value:"
  printf '  %s\n' "${missing[@]}"
  exit 0
fi
if ! bash scripts/mutation-summary.sh "$work/summaries"; then
  echo "mutation-verdict: FAILED: the module summary failed" >&2
  exit 1
fi
jq '
  (.coveredCodeMsi * 1000 | round / 10) as $p
  | {schemaVersion: 1, label: "mutation", message: "\($p)% covered",
     color: (if $p < 90 then "red" elif $p < 95 then "yellow" else "brightgreen" end)}
' mutago-summary.json >"$badges/mutation.json"
echo "mutation-verdict: gate passed; badge: $(jq -c . "$badges/mutation.json")"
