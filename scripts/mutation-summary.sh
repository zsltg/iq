#!/usr/bin/env bash
# Merges per-package mutation summaries into one module-wide mutago-summary.json.
#
#   scripts/mutation-summary.sh <dir>   # every */mutago-summary.json under <dir>
#
# The CI `deep-badge` job calls this through scripts/mutation-verdict.sh. The verdict
# writes one summary for each package, from the stored state of that package, into a
# directory named after the package, and this script sums them. The README badge wants
# one covered-code score for the module. That score is the sum of kills over the sum of
# scored mutants, not a mean of per-package ratios (a two-mutant package must not weigh
# as much as cmd). Counts are summed; the two score fields are calculated again from the
# sums.
#
# The score formula differs from mutago on purpose: coveredCodeMsi = killed / (killed +
# escaped). A skipped mutant (it does not compile) and a mutant that no test covers count
# in neither term. mutago counts a skipped and an errored mutant as a kill. An errored
# mutant is not verified, so this script stops when the sum of errorCount is not zero,
# before it calculates a score. msi stays killed / total.
#
# Prints a per-package table, then writes mutago-summary.json in the current directory.
set -euo pipefail

dir="${1:?usage: mutation-summary.sh <dir>}"
mapfile -t files < <(find "$dir" -name mutago-summary.json | sort)
if [[ ${#files[@]} -eq 0 ]]; then
  echo "mutation-summary: no mutago-summary.json under $dir" >&2
  exit 1
fi

errors=$(jq -s 'map(.errorCount // 0) | add' "${files[@]}")
if [[ "$errors" -ne 0 ]]; then
  echo "mutation-summary: $errors mutant(s) errored; an errored mutant is not verified, so there is no score" >&2
  exit 1
fi

printf '%-40s %7s %7s %7s %7s %8s\n' package total killed escaped uncov score
for f in "${files[@]}"; do
  pkg="$(basename "$(dirname "$f")")"
  jq -r --arg p "$pkg" '
    (.killedCount + .escapedCount) as $scored
    | [$p, .totalMutantsCount, .killedCount, .escapedCount, .notCoveredCount,
       (if $scored > 0 then (.killedCount / $scored * 10000 | round / 100 | tostring) else "-" end)]
    | @tsv' "$f" |
    awk -F'\t' '{printf "%-40s %7s %7s %7s %7s %8s\n", $1, $2, $3, $4, $5, $6}'
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
      (.killedCount + .escapedCount) as $scored
      | if $scored > 0 then .killedCount / $scored else 0 end)
' "${files[@]}" >mutago-summary.json
echo "module: $(jq -c . mutago-summary.json)"
