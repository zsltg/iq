#!/usr/bin/env bash
# Merges per-package mutago summaries into one module-wide mutago-summary.json.
#
#   scripts/mutation-summary.sh <dir>   # every */mutago-summary.json under <dir>
#
# The CI `deep` job runs the mutation gate one package per runner and each run
# writes its own mutago-summary.json (--logger-summary-json); the README badge
# wants one covered-code MSI for the module, which is the sum of kills over the
# sum of covered mutants, not a mean of per-package ratios (a two-mutant
# package must not weigh as much as cmd). Counts are summed; the two MSI
# fields are recomputed from the sums. Prints a per-package table, then writes
# mutago-summary.json in the current directory.
set -euo pipefail

dir="${1:?usage: mutation-summary.sh <dir>}"
mapfile -t files < <(find "$dir" -name mutago-summary.json | sort)
if [[ ${#files[@]} -eq 0 ]]; then
  echo "mutation-summary: no mutago-summary.json under $dir" >&2
  exit 1
fi

printf '%-40s %7s %7s %7s %7s %8s\n' package total killed escaped uncov msi
for f in "${files[@]}"; do
  pkg="$(basename "$(dirname "$f")")"
  jq -r --arg p "$pkg" \
    '[$p, .totalMutantsCount, .killedCount, .escapedCount, .notCoveredCount, (.coveredCodeMsi * 100 | round / 100 | tostring)] | @tsv' "$f" \
    | awk -F'\t' '{printf "%-40s %7s %7s %7s %7s %8s\n", $1, $2, $3, $4, $5, $6}'
done

jq -s '
  def sum(f): map(f) | add;
  {
    totalMutantsCount: sum(.totalMutantsCount),
    killedCount:       sum(.killedCount),
    notCoveredCount:   sum(.notCoveredCount),
    escapedCount:      sum(.escapedCount),
    errorCount:        sum(.errorCount),
    skippedCount:      sum(.skippedCount)
  }
  | .msi = (if .totalMutantsCount > 0 then .killedCount / .totalMutantsCount else 0 end)
  | .coveredCodeMsi = (
      (.totalMutantsCount - .notCoveredCount) as $covered
      | if $covered > 0 then .killedCount / $covered else 0 end)
' "${files[@]}" > mutago-summary.json
echo "module: $(jq -c . mutago-summary.json)"
