#!/usr/bin/env bash
# Runs one shard of the weekly mutation scan: each (file, mutator) cell of the shard.
#
#   scripts/mutation-shard.sh <out-dir> <package> <slug> <shard> <shards> <cells-json>
#
# <cells-json> is the `cells` array of one entry of the scripts/mutation-plan.sh matrix,
# [{"file": "internal/numfmt/decimal.go", "mutator": "branch/case", ...}, ...]. The CI
# `deep-mutate` job gives each value through an environment variable.
#
# The runner installs the pinned mutago one time (IQ_MUTATION_INSTALL_DIR, with a bounded
# retry) and gives it to each gate run in IQ_MUTATION_MUTAGO_BIN. Each cell is one gate run
# with IQ_MUTATION_MUTATORS set to the mutator and the absolute path of the file, which is
# the form that gives the mutant ids of a package run (mutago#248). A shard run has no gate
# flag. The verdict of the package comes later, from scripts/mutation-verdict.sh, which
# merges all shards.
#
# Output, the artifact of the shard:
#   <out-dir>/shard.json           package, slug, shard, shards, seconds, the identity of
#                                  the run (commit, mutago version, Go version, sha256 of
#                                  .mutago.yml and mutago-baseline.json), the cells, and
#                                  the number of failed cells
#   <out-dir>/cells/NNN/           report.json (without the source copies), mutago-agentic.json,
#                                  mutago-summary.json, cell.json and log for each cell
#
# The runner does all cells, also after a failed cell, so that the artifact holds as much
# data as possible. It exits 1 when a cell failed (a run error or an errored mutant).
set -uo pipefail

export LC_ALL=C

usage="usage: mutation-shard.sh <out-dir> <package> <slug> <shard> <shards> <cells-json>"
out="${1:?$usage}"
pkg="${2:?$usage}"
slug="${3:?$usage}"
shard="${4:?$usage}"
shards="${5:?$usage}"
cells_json="${6:?$usage}"

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1

if [[ "$pkg" != "." && ! "$pkg" =~ ^\./[A-Za-z0-9_][A-Za-z0-9_./-]*$ ]] || [[ "$pkg" == *..* ]]; then
  echo "mutation-shard: '$pkg' is not a package path (. or ./dir)" >&2
  exit 1
fi
if [[ ! "$slug" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
  echo "mutation-shard: '$slug' is not a slug" >&2
  exit 1
fi
if [[ ! "$shard" =~ ^[1-9][0-9]*$ || ! "$shards" =~ ^[1-9][0-9]*$ ]] || [[ "$shard" -gt "$shards" ]]; then
  echo "mutation-shard: shard '$shard' of '$shards' is not valid" >&2
  exit 1
fi

# Each cell must name a file directly in the package directory and a mutator name. The
# gate examines the mutator again against the list of the pinned mutago.
dir="${pkg#./}"
[[ "$pkg" == "." ]] && dir="."
if ! cells=$(jq -r --arg d "$dir" '
    if type != "array" then error("cells is not an array") else . end
    | .[]
    | if (.file | type) != "string" or (.mutator | type) != "string" then error("a cell needs file and mutator") else . end
    | if ((.file | split("/") | .[:-1] | join("/")) | if . == "" then "." else . end) != $d
        then error("\(.file) is not in the package directory \($d)") else . end
    | if (.mutator | test("^[a-z_]+/[a-z_-]+$") | not) then error("\(.mutator) is not a mutator name") else . end
    | if (.file | test("^[A-Za-z0-9_./-]+\\.go$") | not) or (.file | contains("..")) then error("\(.file) is not a Go file path") else . end
    | "\(.file)\t\(.mutator)"' <<<"$cells_json"); then
  echo "mutation-shard: the cells JSON is not valid" >&2
  exit 1
fi

start=$(date +%s)
mkdir -p "$out/cells" || exit 1
work=$(mktemp -d) || exit 1
trap 'rm -rf "$work"' EXIT

failed=0
if ! mutago_bin=$(IQ_MUTATION_INSTALL_DIR="$work/bin" bash scripts/mutation-gate.sh | tail -n 1); then
  echo "mutation-shard: could not install mutago" >&2
  exit 1
fi

index=0
while IFS=$'\t' read -r file mutator; do
  [[ -n "$file" ]] || continue
  index=$((index + 1))
  cell="$out/cells/$(printf '%03d' "$index")"
  mkdir -p "$cell"
  jq -n --arg file "$file" --arg mutator "$mutator" '{file: $file, mutator: $mutator}' >"$cell/cell.json"
  echo "mutation-shard: cell $index: $file x $mutator"
  rm -f report.json mutago-agentic.json mutago-summary.json
  IQ_MUTATION_MUTAGO_BIN="$mutago_bin" IQ_MUTATION_MUTATORS="$mutator" \
    bash scripts/mutation-gate.sh "$root/$file" >"$cell/log" 2>&1
  status=$?
  tail -n 5 "$cell/log"
  if [[ "$status" -ne 0 || ! -f report.json ]]; then
    echo "mutation-shard: cell $index failed (exit $status); see $cell/log" >&2
    failed=$((failed + 1))
  fi
  # The source copies and the test output of each mutant make the report large, and the
  # merge does not read them. The diffs and the ids stay.
  [[ -f report.json ]] && jq 'del(.sources) | walk(if type == "object" then del(.processOutput) else . end)' report.json >"$cell/report.json"
  [[ -f mutago-agentic.json ]] && mv mutago-agentic.json "$cell/"
  [[ -f mutago-summary.json ]] && mv mutago-summary.json "$cell/"
  rm -f report.json
done <<<"$cells"

sha() { sha256sum "$1" | cut -d' ' -f1; }
jq -n \
  --arg package "$pkg" --arg slug "$slug" \
  --argjson shard "$shard" --argjson shards "$shards" \
  --argjson seconds "$(($(date +%s) - start))" \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg go "$(go env GOVERSION)" \
  --arg config "$(sha .mutago.yml)" \
  --arg baseline "$(sha mutago-baseline.json)" \
  --argjson cells "$(jq -c '[.[] | {file, mutator}]' <<<"$cells_json")" \
  --argjson failed "$failed" \
  '{package: $package, slug: $slug, shard: $shard, shards: $shards, seconds: $seconds,
    identity: {commit: $commit, mutagoVersion: $mutago, goVersion: $go,
               configSha256: $config, baselineSha256: $baseline},
    cells: $cells, failedCells: $failed}' >"$out/shard.json"

echo "mutation-shard: $pkg shard $shard/$shards, $index cell(s), $failed failed, $(jq .seconds "$out/shard.json") s"
[[ "$failed" -eq 0 ]]
