#!/usr/bin/env bash
# Runs one shard of the CI `mutate-diff` job: the diff-scoped mutation gate for the changed
# files of the shard.
#
#   scripts/mutation-diff-shard.sh <out-dir> <plan.json> <shard>
#
# <plan.json> is the output of `scripts/mutation-plan.sh --diff`. The runner reads the entry
# of <shard> from it: the backend package (or an empty string), the merge-base commit, and
# the groups, [{"package": "./cmd", "files": [{"file": "cmd/a.go", "lines": 40,
# "codeLines": 31}]}]. The CI `mutate-diff` job downloads the plan as an artifact.
#
# For each group the runner makes one gate run (scripts/mutation-gate.sh) with
# IQ_MUTATION_DIFF=1, IQ_MUTATION_BASE set to the merge-base commit of the plan, and the
# absolute paths of the files of the group. mutago then mutates the changed lines of those
# files only, and the gate fails on an escape that is not in mutago-baseline.json, as the
# local diff gate does. One run for each package means one coverage pass for each package.
# The merge-base commit is the same in all shards, so all shards see the same changed lines
# even if the base branch moves during the CI run.
#
# One worker. A backend shard uses one shared set of compose services (scripts/ci-backend.sh
# set the IQ_*_URL variables). Parallel workers on a shared backend give false kills, so the
# runner stops when IQ_MUTATION_WORKERS is set to more than 1. It also stops when the plan
# names a backend but no IQ_*_URL variable is set.
#
# Output, the artifact of the shard:
#   <out-dir>/shard.json   shard, shards, backend, seconds, the identity of the run (commit,
#                          merge-base, mutago version, Go version, sha256 of .mutago.yml and
#                          mutago-baseline.json), for each group the package, the exit
#                          status of the gate and the counts, for each file the number of
#                          mutants, the files that are suspect (20 or more changed code
#                          lines and no mutant), and the number of failed groups. It is
#                          written last, so a shard that timed out has none.
#   <out-dir>/groups/NNN/  report.json (without the source copies and the test output),
#                          mutago-agentic.json, mutago-summary.json,
#                          mutago-baseline.candidate.json and log for each group
#
# Connection strings. The test output in a log can hold the value of an IQ_*_URL variable
# (a URI with a password). Before the runner prints or keeps a log, it replaces each such
# value with the name of its variable. The report keeps no test output.
#
# The runner does all groups, also after a failed group, so that the artifact holds as much
# data as possible. It exits 1 when a group failed.
set -uo pipefail

export LC_ALL=C

usage="usage: mutation-diff-shard.sh <out-dir> <plan.json> <shard>"
out="${1:?$usage}"
plan="${2:?$usage}"
shard="${3:?$usage}"

# A suspect file has at least this many changed code lines and no mutant.
suspect_lines=20

[[ -f "$plan" ]] || {
  echo "mutation-diff-shard: '$plan' is not a file" >&2
  exit 1
}
plan=$(cd "$(dirname "$plan")" && pwd -P)/$(basename "$plan")
out_parent=$(dirname "$out")
mkdir -p "$out_parent" || exit 1
out=$(cd "$out_parent" && pwd -P)/$(basename "$out")

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1

if [[ ! "$shard" =~ ^[1-9][0-9]*$ ]]; then
  echo "mutation-diff-shard: shard '$shard' is not a positive integer" >&2
  exit 1
fi
if [[ ! "${IQ_MUTATION_WORKERS-1}" =~ ^1$ ]]; then
  echo "mutation-diff-shard: IQ_MUTATION_WORKERS='${IQ_MUTATION_WORKERS-}', but a shard uses one worker: parallel workers on a shared backend give false kills" >&2
  exit 1
fi
if ! entry=$(jq -ce --argjson s "$shard" \
  '[.[] | select(.shard == $s)] | if length == 1 then .[0] else error("not one entry") end' "$plan"); then
  echo "mutation-diff-shard: the plan has no single entry for shard $shard" >&2
  exit 1
fi
shards=$(jq -r .shards <<<"$entry")
backend=$(jq -r '.backend // ""' <<<"$entry")
merge_base=$(jq -r '.mergeBase // ""' <<<"$entry")
if [[ ! "$shards" =~ ^[1-9][0-9]*$ ]] || [[ "$shard" -gt "$shards" ]]; then
  echo "mutation-diff-shard: shard '$shard' of '$shards' is not valid" >&2
  exit 1
fi
if [[ ! "$merge_base" =~ ^[0-9a-f]{40}$ ]] || ! git cat-file -e "${merge_base}^{commit}" 2>/dev/null; then
  echo "mutation-diff-shard: the merge-base '$merge_base' of the plan is not a commit of this checkout" >&2
  exit 1
fi
if [[ -n "$backend" ]]; then
  if [[ ! "$backend" =~ ^\./[A-Za-z0-9_][A-Za-z0-9_./-]*$ ]] || [[ "$backend" == *..* ]]; then
    echo "mutation-diff-shard: '$backend' is not a package path" >&2
    exit 1
  fi
  if ! env | grep -qE '^IQ_[A-Z0-9_]+_URL=.'; then
    echo "mutation-diff-shard: the plan names the backend $backend, but no IQ_*_URL variable is set (scripts/ci-backend.sh sets them)" >&2
    exit 1
  fi
fi

# Each group must name a package of the module and files directly in its directory.
if ! groups=$(jq -r '
    .groups
    | if type != "array" or length == 0 then error("groups is not a list") else . end
    | .[]
    | if (.package | type) != "string" or (.package | test("^(\\.|\\./[A-Za-z0-9_][A-Za-z0-9_./-]*)$") | not) or (.package | contains("..")) then error("bad package") else . end
    | (.package | if . == "." then "." else ltrimstr("./") end) as $d
    | .files
    | if type != "array" or length == 0 then error("files is not a list") else . end
    | map(
        if (.file | type) != "string" or (.file | test("^[A-Za-z0-9_./-]+\\.go$") | not) or (.file | contains("..")) or (.file | endswith("_test.go")) then error("bad file") else . end
        | if ((.file | split("/") | .[:-1] | join("/")) | if . == "" then "." else . end) != $d then error("\(.file) is not in \($d)") else . end
        | .file)
    | "\($d)\t\(join(" "))"' <<<"$entry"); then
  echo "mutation-diff-shard: the groups of the plan entry are not valid" >&2
  exit 1
fi

# redact <file>: replace the value of each IQ_*_URL variable in <file> with its name.
redact() {
  python3 - "$1" <<'PY'
import os
import sys

path = sys.argv[1]
secrets = sorted(
    ((name, value) for name, value in os.environ.items()
     if name.startswith("IQ_") and name.endswith("_URL") and value),
    key=lambda item: -len(item[1]))
with open(path, encoding="utf-8", errors="replace") as handle:
    text = handle.read()
for name, value in secrets:
    text = text.replace(value, "<" + name + ">")
with open(path, "w", encoding="utf-8") as handle:
    handle.write(text)
PY
}

start=$(date +%s)
mkdir -p "$out/groups" || exit 1
work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

if ! mutago_bin=$(IQ_MUTATION_INSTALL_DIR="$work/bin" bash scripts/mutation-gate.sh | tail -n 1); then
  echo "mutation-diff-shard: could not install mutago" >&2
  exit 1
fi

failed=0
index=0
group_results="$work/groups.jsonl"
: >"$group_results"
while IFS=$'\t' read -r dir files; do
  [[ -n "$dir" ]] || continue
  index=$((index + 1))
  group="$out/groups/$(printf '%03d' "$index")"
  mkdir -p "$group"
  pkg="./$dir"
  [[ "$dir" == "." ]] && pkg="."
  targets=()
  for file in $files; do
    [[ -f "$file" ]] || {
      echo "mutation-diff-shard: $file does not exist in this checkout" >&2
      exit 1
    }
    targets+=("$root/$file")
  done
  echo "mutation-diff-shard: group $index: $pkg, ${#targets[@]} file(s)"
  rm -f report.json mutago-agentic.json mutago-summary.json mutago-baseline.candidate.json
  # The limits are the same as in scripts/mutation-shard.sh: a mutant can write without end
  # or allocate without end, and the runner must live to upload the artifact.
  (
    ulimit -f 2097152 || exit 1
    ulimit -v 8388608 || exit 1
    IQ_MUTATION_MUTAGO_BIN="$mutago_bin" IQ_MUTATION_BASE="$merge_base" IQ_MUTATION_DIFF=1 \
      exec bash scripts/mutation-gate.sh "${targets[@]}" 2>&1
  ) | cat >"$group/log"
  status=$?
  group_failed=0
  if ! redact "$group/log"; then
    echo "mutation-diff-shard: group $index: could not redact the log; the log is removed" >&2
    rm -f "$group/log"
    group_failed=1
  fi
  [[ -f "$group/log" ]] && tail -n 5 "$group/log"
  if [[ "$status" -ne 0 ]]; then
    echo "mutation-diff-shard: group $index failed (exit $status); see $group/log" >&2
    group_failed=1
  fi
  has_report=false
  if [[ -f report.json ]]; then
    has_report=true
    if ! jq 'del(.sources) | walk(if type == "object" then del(.processOutput) else . end)' report.json >"$group/report.json"; then
      echo "mutation-diff-shard: group $index: could not trim report.json" >&2
      rm -f "$group/report.json"
      group_failed=1
      has_report=false
    fi
  fi
  for name in mutago-agentic.json mutago-summary.json mutago-baseline.candidate.json; do
    [[ -f "$name" ]] && mv "$name" "$group/"
  done
  rm -f report.json
  failed=$((failed + group_failed))

  # The mutants of each file, from the trimmed report. The path in a report can be absolute.
  counts='{}'
  if [[ "$has_report" == true ]]; then
    counts=$(jq -c --arg root "$root/" '
      [.. | objects | select(has("mutator")) | .mutator.originalFilePath | ltrimstr($root)]
      | group_by(.) | map({key: .[0], value: length}) | from_entries' "$group/report.json") || counts='{}'
  fi
  totals=$(jq -c '
    def n(k): (.[k] // []) | length;
    {mutants: (n("killed") + n("escaped") + n("notCovered") + n("skipped") + n("errored")),
     killed: n("killed"), escaped: n("escaped"), notCovered: n("notCovered"),
     skipped: n("skipped"), errored: n("errored")}' "$group/report.json" 2>/dev/null) ||
    totals='{"mutants":0,"killed":0,"escaped":0,"notCovered":0,"skipped":0,"errored":0}'
  jq -cn --arg package "$pkg" --argjson status "$status" --argjson report "$has_report" \
    --argjson counts "$counts" --argjson totals "$totals" \
    --argjson planned "$(jq -c --arg d "$dir" '[.groups[] | select((.package | if . == "." then "." else ltrimstr("./") end) == $d) | .files[]]' <<<"$entry")" '
    {package: $package, status: $status, report: $report, totals: $totals,
     files: ($planned | map(. + {mutants: ($counts[.file] // 0)}))}' >>"$group_results"
done <<<"$groups"

# A suspect file has many changed code lines and no mutant. A comment, a type or a constant
# gives no mutant, so this is a warning, not a failure. The warning is loud because a diff that
# mutago does not read also gives no mutant.
suspect=$(jq -sc --argjson n "$suspect_lines" '[.[].files[] | select(.mutants == 0 and .codeLines >= $n) | .file]' "$group_results")
for file in $(jq -r '.[]' <<<"$suspect"); do
  echo "::warning::mutation-diff-shard: $file has $suspect_lines or more changed code lines but no mutant; make sure that mutago read the diff"
done

sha() { sha256sum "$1" | cut -d' ' -f1; }
jq -n \
  --argjson shard "$shard" --argjson shards "$shards" --arg backend "$backend" \
  --argjson seconds "$(($(date +%s) - start))" \
  --arg commit "$(git rev-parse HEAD)" --arg mergeBase "$merge_base" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg go "$(go env GOVERSION)" \
  --arg config "$(sha .mutago.yml)" \
  --arg baseline "$(sha mutago-baseline.json)" \
  --slurpfile groups "$group_results" \
  --argjson suspect "$suspect" --argjson failed "$failed" \
  '{shard: $shard, shards: $shards, backend: $backend, seconds: $seconds,
    identity: {commit: $commit, mergeBase: $mergeBase, mutagoVersion: $mutago, goVersion: $go,
               configSha256: $config, baselineSha256: $baseline},
    groups: $groups, suspectZero: $suspect, failedGroups: $failed}' >"$out/shard.json"

echo "mutation-diff-shard: shard $shard/$shards, $index group(s), $failed failed, $(jq .seconds "$out/shard.json") s"
[[ "$failed" -eq 0 ]]
