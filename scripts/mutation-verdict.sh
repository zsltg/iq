#!/usr/bin/env bash
# Gives the verdict of the weekly mutation scan and updates the stored state.
#
#   scripts/mutation-verdict.sh <artifacts-dir> <badges-dir> <plan.json> <packages.txt>
#
# <artifacts-dir>  the shard artifacts of scripts/mutation-shard.sh (any depth); it can be
#                  empty when the plan found nothing to scan
# <badges-dir>     the tree of the `badges` branch: state/<slug>.json and mutation.json;
#                  the script updates it in place
# <plan.json>      the full output of scripts/mutation-plan.sh for this run (with cells)
# <packages.txt>   every package of the module, one for each line
#
# The CI `deep-badge` job runs it. The verdict judges each scanned package on its own, so
# one red package does not discard the result of the others. The steps:
#
# 1. Shard checks, for each package. Each plan entry must have exactly one shard report,
#    with the same cells, no failed cell, and the identity of this checkout (the commit,
#    the mutago version, the Go version, and the sha256 of .mutago.yml and
#    mutago-baseline.json). Each cell must have a report. When the plan expects mutants in
#    a cell, the report must hold at least 1 and at most the planned number. The plan comes
#    from a dry run, which counts each mutation before mutago removes byte-identical edits
#    (engine.go: recordOneMutation counts, and processMutation then drops a checksum that
#    it saw before), so a real run can hold fewer mutants, never more. A missing, failed or
#    timed-out shard fails its package. An unreadable report, or a report of a package
#    that is not in the plan, fails the package of its artifact directory
#    (mutation-<slug>-<shard>); only when no package can be found does the verdict stop.
# 2. The gate, for each package with good shards. scripts/mutation-merge.sh merges the
#    cells of all its shards (each edit counts once; an escape is new only if none of its
#    ids is in the baseline). An errored mutant fails the package. A new escape fails every
#    package except ./cmd. ./cmd passes on the covered-code floor of
#    scripts/mutation-gate.sh (cmd_covered_msi_floor, the one source of that value), which
#    applies to the merged ./cmd result only. A kill in one shard and an escape in another
#    for the same edit is a sign of a flaky test; the merge keeps the escape, and the
#    verdict prints a warning. The verdict examines each value that it reads from the
#    merge; a value that is not a count fails the package.
# 3. State. The script writes state/<slug>.json for each package that passed: package,
#    slug, fingerprint, commit, scannedAt, secondsPerMutant (the wall seconds of its
#    shards over its mutants), status "passed", and summary. A failed package gets a
#    failure marker in place of its state: status "failed", the fingerprint and the commit,
#    no summary. The plan always scans a package with a marker again, and the badge treats
#    it as missing, so an old passing summary cannot hide a failure.
# 4. Pruning. A state file of a package that is not in <packages.txt> is removed.
# 5. Module summary. If no package failed and each package has passing state with its
#    current fingerprint, the script sums the summaries with scripts/mutation-summary.sh
#    (mutago-summary.json in the current directory) and writes <badges-dir>/mutation.json,
#    the shields.io endpoint of the README badge. Otherwise it removes mutago-summary.json
#    and keeps the previous mutation.json, so a partial or red scan never publishes a score.
#
# IQ_MUTATION_MERGE_SCRIPT replaces scripts/mutation-merge.sh; the fixture tests use it to
# give a corrupt merge result.
#
# Exit status: 0 when every scanned package passed, 1 when one or more packages failed (the
# state of the other packages is written), 2 when the input cannot be used (nothing is
# written).
set -uo pipefail

export LC_ALL=C

usage="usage: mutation-verdict.sh <artifacts-dir> <badges-dir> <plan.json> <packages.txt>"
if [[ $# -ne 4 ]]; then
  echo "$usage" >&2
  exit 2
fi
artifacts="$1"
badges="$2"
plan="$3"
packages_file="$4"

for path in "$artifacts" "$badges"; do
  [[ -d "$path" ]] || {
    echo "mutation-verdict: '$path' is not a directory" >&2
    exit 2
  }
done
for path in "$plan" "$packages_file"; do
  [[ -f "$path" ]] || {
    echo "mutation-verdict: '$path' is not a file" >&2
    exit 2
  }
done
artifacts=$(cd "$artifacts" && pwd -P)
badges=$(cd "$badges" && pwd -P)
plan=$(cd "$(dirname "$plan")" && pwd -P)/$(basename "$plan")
packages_file=$(cd "$(dirname "$packages_file")" && pwd -P)/$(basename "$packages_file")

root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2
merge_script="${IQ_MUTATION_MERGE_SCRIPT:-scripts/mutation-merge.sh}"

floor=$(sed -n 's/^cmd_covered_msi_floor=//p' scripts/mutation-gate.sh)
if [[ ! "$floor" =~ ^[0-9]+$ ]]; then
  echo "mutation-verdict: cannot read cmd_covered_msi_floor from scripts/mutation-gate.sh" >&2
  exit 2
fi

mapfile -t module_packages < <(grep -v '^[[:space:]]*$' "$packages_file")
if [[ ${#module_packages[@]} -eq 0 ]]; then
  echo "mutation-verdict: $packages_file lists no package; stop, so that no state is pruned" >&2
  exit 2
fi

work=$(mktemp -d) || exit 2
trap 'rm -rf "$work"' EXIT

sha() { sha256sum "$1" | cut -d' ' -f1; }
if ! jq -n \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg go "$(go env GOVERSION)" \
  --arg config "$(sha .mutago.yml)" \
  --arg baseline "$(sha mutago-baseline.json)" \
  '{commit: $commit, mutagoVersion: $mutago, goVersion: $go,
    configSha256: $config, baselineSha256: $baseline}' >"$work/identity.json"; then
  echo "mutation-verdict: cannot record the identity of this checkout" >&2
  exit 2
fi

# Step 1: the shard checks. The output is one line for each package of the plan:
# package, slug, seconds, ok or fail, and the cell directories.
python3 - "$artifacts" "$plan" "$work/identity.json" >"$work/scanned.tsv" <<'PY'
import json
import os
import re
import sys

artifacts, plan_path, identity_path = sys.argv[1:4]


def load(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def stop(message):
    print("mutation-verdict: " + message, file=sys.stderr)
    sys.exit(2)


def is_count(value):
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


try:
    identity = load(identity_path)
    plan = load(plan_path)
except (OSError, ValueError) as err:
    stop("cannot read the plan: {}".format(err))
if not isinstance(plan, list):
    stop("the plan is not a JSON array")

expected = {}
packages = {}
for entry in plan:
    try:
        key = (entry["package"], entry["shard"])
        cells = [(c["file"], c["mutator"], c["mutants"]) for c in entry["cells"]]
        slug, shards = entry["slug"], entry["shards"]
    except (KeyError, TypeError):
        stop("a plan entry is not complete: {}".format(json.dumps(entry)[:200]))
    if not all(is_count(c[2]) for c in cells):
        stop("a plan cell of {} shard {} has no mutant count".format(*key))
    if key in expected:
        stop("the plan holds {} shard {} two times".format(*key))
    expected[key] = {"slug": slug, "shards": shards, "cells": cells}
    packages.setdefault(key[0], {"slug": slug, "seconds": 0, "cells": [], "problems": []})

by_slug = {item["slug"]: package for package, item in packages.items()}


def owner(folder):
    """The package of an artifact directory named mutation-<slug>-<shard>, or None."""
    match = re.match(r"^mutation-(.+)-([1-9][0-9]*)$", os.path.basename(folder))
    return by_slug.get(match.group(1)) if match else None


reports = {}
for folder, _, files in sorted(os.walk(artifacts)):
    if "shard.json" not in files:
        continue
    try:
        report = load(os.path.join(folder, "shard.json"))
        key = (report["package"], report["shard"])
    except (OSError, ValueError, KeyError, TypeError) as err:
        package = owner(folder)
        if package is None:
            stop("cannot read {}/shard.json ({}), and its directory name gives no package of the plan".format(folder, err))
        packages[package]["problems"].append("cannot read {}/shard.json: {}".format(folder, err))
        continue
    if key[0] not in packages:
        package = owner(folder)
        if package is None:
            stop("{} holds a report of {}, which is not in the plan".format(folder, key[0]))
        packages[package]["problems"].append("{} holds a report of {}, which is not in the plan".format(folder, key[0]))
        continue
    reports.setdefault(key, []).append((folder, report))

fewer = 0
for key, found in sorted(reports.items(), key=lambda item: str(item[0])):
    item = packages[key[0]]
    problems = item["problems"]
    if len(found) > 1:
        problems.append("{} reports for shard {}: {}".format(len(found), key[1], ", ".join(f for f, _ in found)))
        continue
    folder, report = found[0]
    entry = expected.get(key)
    if entry is None:
        problems.append("{} holds shard {}, which is not in the plan".format(folder, key[1]))
        continue
    if report.get("identity") != identity:
        problems.append("shard {} has the identity {}, not {}".format(key[1], json.dumps(report.get("identity")), json.dumps(identity)))
    if report.get("shards") != entry["shards"] or report.get("slug") != entry["slug"]:
        problems.append("shard {} has shards={} slug={}, the plan has shards={} slug={}".format(
            key[1], report.get("shards"), report.get("slug"), entry["shards"], entry["slug"]))
    want = [[c[0], c[1]] for c in entry["cells"]]
    got = [[c.get("file"), c.get("mutator")] for c in report.get("cells") or []]
    if got != want:
        problems.append("shard {} did not run the cells of the plan".format(key[1]))
        continue
    if report.get("failedCells") != 0:
        problems.append("shard {} has {} failed cell(s)".format(key[1], report.get("failedCells")))
    seconds = report.get("seconds")
    if not is_count(seconds):
        problems.append("shard {} has no wall seconds".format(key[1]))
    else:
        item["seconds"] += seconds
    for index, (file, mutator, planned) in enumerate(entry["cells"], start=1):
        cell = os.path.join(folder, "cells", "{:03d}".format(index))
        try:
            data = load(os.path.join(cell, "report.json"))
            listed = sum(len(data.get(s) or []) for s in ("killed", "escaped", "notCovered", "skipped", "errored"))
            stated = data["stats"]["totalMutantsCount"]
        except (OSError, ValueError, KeyError, TypeError) as err:
            problems.append("shard {} cell {} ({} x {}) has no usable report: {}".format(key[1], index, file, mutator, err))
            continue
        if stated != listed:
            problems.append("shard {} cell {} ({} x {}): the report lists {} mutants but states {}".format(
                key[1], index, file, mutator, listed, stated))
        elif planned > 0 and listed == 0:
            problems.append("shard {} cell {} ({} x {}): the plan has {} mutants, the report none".format(
                key[1], index, file, mutator, planned))
        elif listed > planned:
            problems.append("shard {} cell {} ({} x {}): the report has {} mutants, more than the {} of the plan".format(
                key[1], index, file, mutator, listed, planned))
        else:
            if listed < planned:
                fewer += 1
            item["cells"].append(cell)

for key in sorted(expected, key=str):
    if key not in reports:
        packages[key[0]]["problems"].append("no report for shard {} (missing, failed or timed out)".format(key[1]))

if fewer:
    print("mutation-verdict: {} cell(s) hold fewer mutants than the dry run; mutago removed byte-identical edits".format(fewer), file=sys.stderr)
for package in sorted(packages):
    item = packages[package]
    for problem in item["problems"]:
        print("mutation-verdict: FAILED: {}: {}".format(package, problem), file=sys.stderr)
    status = "fail" if item["problems"] else "ok"
    print("\t".join([package, item["slug"], str(item["seconds"]), status] + sorted(item["cells"])))
PY
check_status=$?
if [[ "$check_status" -ne 0 ]]; then
  echo "mutation-verdict: the shard reports or the plan cannot be used; nothing is written" >&2
  exit 2
fi

# Step 2: the gate, one merge for each package with good shards.
failed_packages=()
passed_rows=()
declare -A slug_of_package=()
mkdir -p "$work/merged"
while IFS=$'\t' read -r -a row; do
  [[ ${#row[@]} -ge 4 ]] || continue
  pkg="${row[0]}"
  slug="${row[1]}"
  slug_of_package[$pkg]="$slug"
  if [[ "${row[3]}" != "ok" ]]; then
    failed_packages+=("$pkg")
    continue
  fi
  cells=("${row[@]:4}")
  merged="$work/merged/$slug.json"
  if [[ ${#cells[@]} -eq 0 ]]; then
    echo '{"totalMutantsCount":0,"killedCount":0,"notCoveredCount":0,"escapedCount":0,"errorCount":0,"skippedCount":0,"score":0,"escapedChecksums":[],"newEscapes":[],"conflicts":0}' >"$merged"
  else
    bash "$merge_script" mutago-baseline.json "${cells[@]}" >"$merged"
    status=$?
    if [[ "$status" -ne 0 && "$status" -ne 4 ]]; then
      echo "mutation-verdict: FAILED: $pkg: the merge could not read the shard reports" >&2
      failed_packages+=("$pkg")
      continue
    fi
  fi
  # Each count must be a non-negative integer and the score a number from 0 to 1; any
  # other value fails the package.
  if ! values=$(jq -er '
      [.totalMutantsCount, .killedCount, .escapedCount, .notCoveredCount, .skippedCount,
       .errorCount, (.newEscapes | length), .conflicts] as $counts
      | if ($counts | all(type == "number" and . >= 0 and . == floor))
           and (.score | type == "number" and . >= 0 and . <= 1)
        then $counts + [.score] | @tsv
        else error("not a count") end' "$merged" 2>/dev/null); then
    echo "mutation-verdict: FAILED: $pkg: the merge result is not valid" >&2
    failed_packages+=("$pkg")
    continue
  fi
  IFS=$'\t' read -r total killed escaped _ _ errors new conflicts score <<<"$values"
  echo "mutation-verdict: $pkg: $total mutants, $killed killed, $escaped escaped, $new new, $errors errored, score $score"
  if [[ "$conflicts" -ne 0 ]]; then
    echo "mutation-verdict: WARNING: $pkg: $conflicts edit(s) have different results in different shards (a flaky test can cause a false kill); see the merge lines above" >&2
  fi
  if [[ "$errors" -ne 0 ]]; then
    echo "mutation-verdict: FAILED: $pkg: $errors mutant(s) errored or timed out; an errored mutant is not verified" >&2
    failed_packages+=("$pkg")
    continue
  fi
  if [[ "$pkg" == "./cmd" ]]; then
    if [[ $((killed + escaped)) -gt 0 ]] && python3 -c 'import sys; sys.exit(0 if float(sys.argv[1]) * 100 < float(sys.argv[2]) else 1)' "$score" "$floor"; then
      echo "mutation-verdict: FAILED: ./cmd scores $score, under the floor of $floor% (killed / (killed + escaped)); kill escaped mutants, never lower the floor" >&2
      failed_packages+=("$pkg")
      continue
    fi
  elif [[ "$new" -ne 0 ]]; then
    echo "mutation-verdict: FAILED: $pkg: $new new escaped mutant(s); kill them or accept a genuine equivalent into mutago-baseline.json" >&2
    failed_packages+=("$pkg")
    continue
  fi
  passed_rows+=("$pkg"$'\t'"$slug"$'\t'"${row[2]}")
done <"$work/scanned.tsv"

# Step 3: the state of each package that passed.
mkdir -p "$badges/state"
scanned_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
commit=$(jq -r .commit "$work/identity.json")
for line in "${passed_rows[@]}"; do
  IFS=$'\t' read -r pkg slug seconds <<<"$line"
  if ! fingerprint=$(bash scripts/mutation-fingerprint.sh "$pkg"); then
    echo "mutation-verdict: FAILED: $pkg: cannot calculate the fingerprint" >&2
    failed_packages+=("$pkg")
    continue
  fi
  if ! jq --arg package "$pkg" --arg slug "$slug" --arg fingerprint "$fingerprint" \
    --arg commit "$commit" --arg scannedAt "$scanned_at" --argjson seconds "$seconds" \
    '{package: $package, slug: $slug, status: "passed", fingerprint: $fingerprint, commit: $commit,
      scannedAt: $scannedAt,
      secondsPerMutant: (if .totalMutantsCount > 0 then ($seconds / .totalMutantsCount * 10 | round / 10) else null end),
      summary: {totalMutantsCount, killedCount, notCoveredCount, escapedCount, errorCount,
                skippedCount, score}}' \
    "$work/merged/$slug.json" >"$work/state.json"; then
    echo "mutation-verdict: FAILED: $pkg: cannot write the state" >&2
    failed_packages+=("$pkg")
    continue
  fi
  mv "$work/state.json" "$badges/state/$slug.json"
  echo "mutation-verdict: $pkg passed; state written"
done

# Step 3b: a failure marker for each package that failed. The marker replaces a previous
# passing result, so the plan scans the package again (a failed state is always stale) and
# the badge treats it as missing. The marker has no summary, so an old passing summary
# cannot hide the failure. It keeps the previous secondsPerMutant for the plan.
for pkg in "${failed_packages[@]}"; do
  slug="${slug_of_package[$pkg]-}"
  [[ -n "$slug" ]] || continue
  fingerprint=$(bash scripts/mutation-fingerprint.sh "$pkg" 2>/dev/null) || fingerprint=""
  previous="$badges/state/$slug.json"
  rate=null
  if [[ -f "$previous" ]]; then
    rate=$(jq -c '.secondsPerMutant | if type == "number" and . > 0 then . else null end' "$previous" 2>/dev/null) || rate=null
  fi
  if jq -n --arg package "$pkg" --arg slug "$slug" --arg fingerprint "$fingerprint" \
    --arg commit "$commit" --arg scannedAt "$scanned_at" --argjson rate "${rate:-null}" \
    '{package: $package, slug: $slug, status: "failed", fingerprint: $fingerprint,
      commit: $commit, scannedAt: $scannedAt, secondsPerMutant: $rate}' >"$work/state.json"; then
    mv "$work/state.json" "$previous"
    echo "mutation-verdict: $pkg failed; failure marker written"
  else
    echo "mutation-verdict: $pkg failed; cannot write the failure marker" >&2
  fi
done

# Step 4: remove the state of a package that the module no longer has.
for state in "$badges"/state/*.json; do
  [[ -f "$state" ]] || continue
  pkg=$(jq -r '.package // ""' "$state" 2>/dev/null) || pkg=""
  keep=0
  for want in "${module_packages[@]}"; do
    [[ "$pkg" == "$want" ]] && keep=1 && break
  done
  if [[ "$keep" -eq 0 ]]; then
    echo "mutation-verdict: remove the state of '$pkg' ($(basename "$state")), which is not a package of the module"
    rm -f "$state"
  fi
done

if [[ ${#failed_packages[@]} -gt 0 ]]; then
  rm -f mutago-summary.json
  echo "mutation-verdict: FAILED: ${#failed_packages[@]} package(s) failed and got a failure marker; the badge keeps its previous value:" >&2
  printf '  %s\n' "${failed_packages[@]}" >&2
  exit 1
fi

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
  if [[ "$(jq -r '.status // "passed"' "$state" 2>/dev/null)" != "passed" ]]; then
    missing+=("$pkg (the last scan failed)")
    continue
  fi
  stored=$(jq -r '.fingerprint // ""' "$state" 2>/dev/null) || stored=""
  if ! current=$(bash scripts/mutation-fingerprint.sh "$pkg") || [[ "$stored" != "$current" ]]; then
    missing+=("$pkg (state is not current)")
    continue
  fi
  mkdir -p "$work/summaries/$slug"
  if ! jq -e '.summary | objects' "$state" >"$work/summaries/$slug/mutago-summary.json" 2>/dev/null; then
    missing+=("$pkg (state has no summary)")
    rm -rf "${work:?}/summaries/$slug"
  fi
done
if [[ ${#missing[@]} -gt 0 ]]; then
  rm -f mutago-summary.json
  echo "mutation-verdict: gate passed; ${#missing[@]} package(s) have no current state, so the badge keeps its previous value:"
  printf '  %s\n' "${missing[@]}"
  exit 0
fi
if ! bash scripts/mutation-summary.sh "$work/summaries"; then
  echo "mutation-verdict: the module summary failed; the badge keeps its previous value" >&2
  rm -f mutago-summary.json
  exit 1
fi
if ! jq '
  (.coveredCodeMsi * 1000 | round / 10) as $p
  | {schemaVersion: 1, label: "mutation", message: "\($p)% covered",
     color: (if $p < 90 then "red" elif $p < 95 then "yellow" else "brightgreen" end)}
' mutago-summary.json >"$work/mutation.json"; then
  echo "mutation-verdict: cannot write the badge; the badge keeps its previous value" >&2
  exit 1
fi
mv "$work/mutation.json" "$badges/mutation.json"
echo "mutation-verdict: gate passed; badge: $(jq -c . "$badges/mutation.json")"
