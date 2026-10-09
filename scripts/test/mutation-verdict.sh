#!/usr/bin/env bash
# Fixture tests for scripts/mutation-verdict.sh, for the parse, pack and rate steps of
# scripts/mutation-plan.sh, and for scripts/mutation-fingerprint.sh.
#
#   bash scripts/test/mutation-verdict.sh
#
# Each case makes shard artifacts with scripts/test/mutation-verdict-fixtures.py in a
# temporary directory, runs the verdict, and examines the exit status, the output, the
# state files and the badge. The test needs no network, no container and no mutago run,
# and it finishes in seconds. scripts/check.sh (make check) runs it.
#
# The fixtures use real package paths (./internal/numfmt, ./internal/render, ./cmd),
# because the verdict calculates their fingerprints. The accepted escape uses the id of
# the first entry of mutago-baseline.json, so the test does not depend on one entry.
#
# The verdict writes mutago-summary.json in the root of the repository. The test keeps a
# copy of a file that is already there and puts it back at the end.
# Each check is a condition in single quotes that check() evaluates later, so the
# variables in it must not expand at the call. status is read only in those conditions.
# shellcheck disable=SC2016,SC2034
set -uo pipefail

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1
here="$root/scripts/test"

work=$(mktemp -d) || exit 1
summary_backup=""
if [[ -f mutago-summary.json ]]; then
  summary_backup="$work/summary-backup.json"
  cp mutago-summary.json "$summary_backup"
fi
finish() {
  rm -f mutago-summary.json
  [[ -n "$summary_backup" ]] && cp "$summary_backup" mutago-summary.json
  rm -rf "$work"
}
trap finish EXIT

identity=$(jq -n \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg go "$(go env GOVERSION)" \
  --arg config "$(sha256sum .mutago.yml | cut -d' ' -f1)" \
  --arg baseline "$(sha256sum mutago-baseline.json | cut -d' ' -f1)" \
  '{commit: $commit, mutagoVersion: $mutago, goVersion: $go,
    configSha256: $config, baselineSha256: $baseline}')
accepted_id=$(jq -r '.mutants[0].id' mutago-baseline.json)

passed=0
failed=0
check() {
  if eval "$2"; then
    echo "PASS $1"
    passed=$((passed + 1))
  else
    echo "FAIL $1"
    echo "  condition: $2"
    [[ -f "$case_dir/out" ]] && sed 's/^/  | /' "$case_dir/out"
    failed=$((failed + 1))
  fi
}

# prepare <name> <shards-json> <package>...: makes the fixtures of one case in $case_dir.
prepare() {
  local name="$1" shards="$2"
  shift 2
  case_dir="$work/$name"
  mkdir -p "$case_dir/badges/state" "$case_dir/artifacts"
  jq -n --argjson identity "$identity" --argjson shards "$shards" \
    '{identity: $identity, shards: $shards}' >"$case_dir/spec.json"
  python3 "$here/mutation-verdict-fixtures.py" "$case_dir/spec.json" "$case_dir"
  printf '%s\n' "$@" >"$case_dir/packages.txt"
}

# verdict: runs the verdict on $case_dir and sets status.
verdict() {
  rm -f mutago-summary.json
  bash scripts/mutation-verdict.sh "$case_dir/artifacts" "$case_dir/badges" \
    "$case_dir/plan.json" "$case_dir/packages.txt" >"$case_dir/out" 2>&1
  status=$?
}

cell() { jq -nc --arg f "$1" --arg m "$2" --argjson x "$3" '{file: $f, mutator: $m, mutants: $x}'; }
shard() {
  jq -nc --arg d "$1" --arg p "$2" --arg s "$3" --argjson n "$4" --argjson t "$5" --argjson c "$6" \
    '{dir: $d, package: $p, slug: $s, shard: $n, shards: $t, cells: $c}'
}
# cmd_cell <killed> <escaped>: one ./cmd cell; each escape has an id that is not in the baseline.
cmd_cell() {
  local arr="" i
  for ((i = 0; i < $1; i++)); do arr+="[\"k$i\",\"killed\",\"\"],"; done
  for ((i = 0; i < $2; i++)); do arr+="[\"e$i\",\"escaped\",\"new$i\"],"; done
  cell cmd/driver.go statement/remove "[${arr%,}]"
}
# state_file <slug> <package> <fingerprint>: a stored state with one killed mutant.
state_file() {
  jq -n --arg s "$1" --arg p "$2" --arg f "$3" \
    '{package: $p, slug: $s, fingerprint: $f,
      summary: {totalMutantsCount: 1, killedCount: 1, notCoveredCount: 0,
                escapedCount: 0, errorCount: 0, skippedCount: 0}}' >"$case_dir/badges/state/$1.json"
}

numfmt=./internal/numfmt
nslug=internal-numfmt
file=internal/numfmt/convert.go
# Edit c3 escapes in both shards, with a different mutator and so a different id. One id
# is in the baseline, so the escape is not new, and the merge counts the edit once.
cell1=$(cell $file branch/if "[[\"c1\",\"killed\",\"\"],[\"c2\",\"killed\",\"\"],[\"c3\",\"escaped\",\"$accepted_id\"]]")
cell2=$(cell $file statement/return '[["c4","killed",""],["c5","notCovered",""],["c3","escaped","otherid"]]')
shard1=$(shard a $numfmt $nslug 1 2 "[$cell1]")
shard2=$(shard b $numfmt $nslug 2 2 "[$cell2]")

prepare two-shards "[$shard1,$shard2]" $numfmt
verdict
check "two shards: the gate passes" '[[ $status -eq 0 ]]'
check "two shards: each edit counts once" \
  '[[ "$(jq -c ".summary | [.totalMutantsCount, .killedCount, .escapedCount, .notCoveredCount]" "$case_dir/badges/state/$nslug.json")" == "[5,3,1,1]" ]]'
check "two shards: seconds per mutant is the shard time over the mutants" \
  '[[ "$(jq .secondsPerMutant "$case_dir/badges/state/$nslug.json")" == "40" ]]'
check "two shards: the badge shows killed / (killed + escaped)" \
  '[[ "$(jq -r .message "$case_dir/badges/mutation.json")" == "75% covered" ]]'
check "two shards: the module summary has the same score" \
  '[[ "$(jq .coveredCodeMsi mutago-summary.json)" == "0.75" ]]'

prepare missing-shard "[$shard1]" $numfmt
jq --argjson c "[$cell2]" \
  '. + [{package: "./internal/numfmt", slug: "internal-numfmt", shard: 2, shards: 2, cells: ($c | map({file, mutator, mutants: (.mutants | length)}))}]' \
  "$case_dir/plan.json" >"$case_dir/plan.tmp" && mv "$case_dir/plan.tmp" "$case_dir/plan.json"
verdict
check "missing shard: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "./internal/numfmt: no report for shard 2" "$case_dir/out"'
check "missing shard: only a failure marker is written" '[[ "$(jq -r .status "$case_dir/badges/state/$nslug.json")" == failed ]]'

# A timed-out shard uploads its finished cells but no shard.json (the runner writes it last).
prepare partial-shard "[$shard1,$shard2]" $numfmt
find "$case_dir/artifacts" -name shard.json | sort | tail -n 1 | xargs rm -f
verdict
check "partial shard (cells, no shard.json): the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "FAILED: ./internal/numfmt: no report for shard" "$case_dir/out"'
check "partial shard: only a failure marker is written" '[[ "$(jq -r .status "$case_dir/badges/state/$nslug.json")" == failed ]]'

copy=$(shard a2 $numfmt $nslug 1 2 "[$cell1]" | jq -c '. + {not_in_matrix: true}')
prepare duplicate-shard "[$shard1,$copy,$shard2]" $numfmt
verdict
check "duplicate shard: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "./internal/numfmt: 2 reports for shard 1" "$case_dir/out"'

prepare identity-mismatch "[$shard1,$(jq -c '. + {identity_patch: {commit: "0000000"}}' <<<"$shard2")]" $numfmt
verdict
check "other commit: the verdict fails" '[[ $status -eq 1 ]] && grep -q "has the identity" "$case_dir/out"'

prepare mutago-mismatch "[$shard1,$(jq -c '. + {identity_patch: {mutagoVersion: "v0.0.0"}}' <<<"$shard2")]" $numfmt
verdict
check "other mutago version: the verdict fails" '[[ $status -eq 1 ]] && grep -q "has the identity" "$case_dir/out"'

extra=$(shard extra $numfmt $nslug 3 2 "[$cell2]" | jq -c '. + {not_in_matrix: true}')
prepare extra-shard "[$shard1,$shard2,$extra]" $numfmt
verdict
check "shard not in the plan: the verdict fails" '[[ $status -eq 1 ]] && grep -q "not in the plan" "$case_dir/out"'

prepare new-escape "[$(shard a $numfmt $nslug 1 1 "[$(cell $file statement/remove '[["n1","escaped","notinbaseline"]]')]")]" $numfmt
verdict
check "new escape: the verdict fails" '[[ $status -eq 1 ]] && grep -q "1 new escaped" "$case_dir/out"'

prepare errored "[$(shard a $numfmt $nslug 1 1 "[$(cell $file statement/remove '[["e1","errored",""],["e2","killed",""]]')]")]" $numfmt
verdict
check "errored mutant: the verdict fails" '[[ $status -eq 1 ]] && grep -q "errored or timed out" "$case_dir/out"'

killed=$(cell $file statement/remove '[["k1","killed",""]]')
escaped=$(cell $file branch/if "[[\"k1\",\"escaped\",\"$accepted_id\"]]")
prepare conflict "[$(shard a $numfmt $nslug 1 2 "[$killed]"),$(shard b $numfmt $nslug 2 2 "[$escaped]")]" $numfmt
verdict
check "kill in one shard, escape in another: a warning, and the escape stays" \
  '[[ $status -eq 0 ]] && grep -q "WARNING: ./internal/numfmt: 1 edit" "$case_dir/out" &&
   [[ "$(jq .summary.escapedCount "$case_dir/badges/state/$nslug.json")" == 1 ]]'

prepare cmd-under "[$(shard a ./cmd cmd 1 1 "[$(cmd_cell 8 2)]")]" ./cmd
verdict
check "./cmd under the floor (80%): the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "under the floor of 90" "$case_dir/out"'

prepare cmd-over "[$(shard a ./cmd cmd 1 1 "[$(cmd_cell 19 1)]")]" ./cmd
verdict
check "./cmd over the floor (95%) with a new escape: the verdict passes" \
  '[[ $status -eq 0 && -f "$case_dir/badges/state/cmd.json" ]]'

prepare cmd-equal "[$(shard a ./cmd cmd 1 1 "[$(cmd_cell 9 1)]")]" ./cmd
verdict
check "./cmd at the floor (90%): the verdict passes" '[[ $status -eq 0 ]]'

prepare removed-package "[$shard1,$shard2]" $numfmt
state_file drivers-gone ./drivers/gone x
verdict
check "state of a removed package: the verdict removes it" \
  '[[ $status -eq 0 && ! -f "$case_dir/badges/state/drivers-gone.json" ]] &&
   grep -q "remove the state of .\./drivers/gone" "$case_dir/out"'

prepare partial "[$shard1,$shard2]" $numfmt ./internal/render
echo '{"old":true}' >"$case_dir/badges/mutation.json"
verdict
check "partial state: the gate passes" '[[ $status -eq 0 ]]'
check "partial state: no new badge, the previous badge stays" \
  '[[ "$(jq -c . "$case_dir/badges/mutation.json")" == "{\"old\":true}" && ! -f mutago-summary.json ]] &&
   grep -q "internal/render (no state)" "$case_dir/out"'

prepare stale-reused "[$shard1,$shard2]" $numfmt ./internal/render
state_file internal-render ./internal/render stale
verdict
check "reused state with an old fingerprint: no badge" \
  '[[ $status -eq 0 && ! -f "$case_dir/badges/mutation.json" ]] && grep -q "state is not current" "$case_dir/out"'

prepare current-reused "[$shard1,$shard2]" $numfmt ./internal/render
state_file internal-render ./internal/render "$(bash scripts/mutation-fingerprint.sh ./internal/render)"
verdict
check "reused current state: the badge covers both packages (4 of 5)" \
  '[[ $status -eq 0 && "$(jq -r .message "$case_dir/badges/mutation.json")" == "80% covered" ]]'

prepare empty-matrix "[]" $numfmt
verdict
check "nothing to scan and no state: the gate passes, no badge" \
  '[[ $status -eq 0 && ! -f "$case_dir/badges/mutation.json" ]]'

# F1: the mutants of each cell against the plan.
cell_of() { cell $file statement/remove "$1" | jq -c ". + $2"; }
prepare cell-more "[$(shard a $numfmt $nslug 1 1 "[$(cell_of '[["m1","killed",""],["m2","killed",""]]' '{planned: 1}')]")]" $numfmt
verdict
check "cell with more mutants than the plan: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "more than the 1 of the plan" "$case_dir/out"'
prepare cell-empty "[$(shard a $numfmt $nslug 1 1 "[$(cell_of '[]' '{planned: 3}')]")]" $numfmt
verdict
check "cell with no mutants where the plan has some: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "the plan has 3 mutants, the report none" "$case_dir/out"'
prepare cell-fewer "[$(shard a $numfmt $nslug 1 1 "[$(cell_of '[["f1","killed",""]]' '{planned: 2}')]")]" $numfmt
verdict
check "cell with fewer mutants than the plan (merged edits): the verdict passes" \
  '[[ $status -eq 0 ]] && grep -q "fewer mutants than the dry run" "$case_dir/out"'
prepare cell-stated "[$(shard a $numfmt $nslug 1 1 "[$(cell_of '[["s1","killed",""]]' '{stated: 5}')]")]" $numfmt
verdict
check "report that states another total than it lists: the verdict fails" \
  '[[ $status -eq 1 ]] && grep -q "lists 1 mutants but states 5" "$case_dir/out"'
prepare cell-no-report "[$shard1,$shard2]" $numfmt
rm -f "$case_dir/artifacts/b/cells/001/report.json"
verdict
check "cell with no report: the verdict fails" '[[ $status -eq 1 ]] && grep -q "has no usable report" "$case_dir/out"'

# F3: a merge result that is not valid fails the package.
bad_merge="$work/bad-merge.sh"
cat >"$bad_merge" <<'EOF'
#!/usr/bin/env bash
echo '{"totalMutantsCount":"x","killedCount":1,"escapedCount":0,"notCoveredCount":0,"skippedCount":0,"errorCount":0,"newEscapes":[],"conflicts":0,"score":1}'
EOF
prepare corrupt-merge "[$shard1,$shard2]" $numfmt
IQ_MUTATION_MERGE_SCRIPT="$bad_merge" verdict
check "merge result with a count that is not a number: the verdict fails" \
  '[[ $status -eq 1 ]] && [[ "$(jq -r .status "$case_dir/badges/state/$nslug.json")" == failed ]] && grep -q "merge result is not valid" "$case_dir/out"'
printf '#!/usr/bin/env bash\necho "not json"\n' >"$bad_merge"
prepare garbage-merge "[$shard1,$shard2]" $numfmt
IQ_MUTATION_MERGE_SCRIPT="$bad_merge" verdict
check "merge result that is not JSON: the verdict fails" '[[ $status -eq 1 ]] && grep -q "merge result is not valid" "$case_dir/out"'

# E2: one red package does not discard the others.
render=./internal/render
rslug=internal-render
rcell=$(cell internal/render/json.go statement/remove '[["r1","killed",""]]')
prepare one-red "[$shard1,$(shard r $render $rslug 1 1 "[$rcell]")]" $numfmt $render
jq --argjson c "[$cell2]" \
  'map(if .package == "./internal/numfmt" then .shards = 2 else . end)
   + [{package: "./internal/numfmt", slug: "internal-numfmt", shard: 2, shards: 2, cells: ($c | map({file, mutator, mutants: (.mutants | length)}))}]' \
  "$case_dir/plan.json" >"$case_dir/plan.tmp" && mv "$case_dir/plan.tmp" "$case_dir/plan.json"
verdict
check "one red package: the verdict fails" '[[ $status -eq 1 ]] && grep -q "FAILED: ./internal/numfmt: no report for shard 2" "$case_dir/out"'
check "one red package: the other package gets its state, the red one a marker" \
  '[[ "$(jq -r .status "$case_dir/badges/state/$rslug.json")" == passed ]] && [[ "$(jq -r .status "$case_dir/badges/state/$nslug.json")" == failed ]]'
check "one red package: no badge" '[[ ! -f "$case_dir/badges/mutation.json" && ! -f mutago-summary.json ]]'

# N1: a failed package gets a failure marker in place of its state.
prepare marker "[$(shard a $numfmt $nslug 1 1 "[$(cell $file statement/remove '[["n1","escaped","notinbaseline"]]')]")]" $numfmt
state_file $nslug $numfmt "$(bash scripts/mutation-fingerprint.sh $numfmt)"
verdict
check "failed package: a failure marker replaces the old passing state" \
  '[[ $status -eq 1 ]] && [[ "$(jq -c "[.status, has(\"summary\")]" "$case_dir/badges/state/$nslug.json")" == "[\"failed\",false]" ]]'
check "failed package: the next plan scans it again" \
  '[[ "$(bash scripts/mutation-plan.sh --stale "$case_dir/badges" $numfmt)" == "the last scan failed" ]]'
marked="$case_dir/badges"
prepare marker-badge "[$(shard r $render $rslug 1 1 "[$rcell]")]" $render $numfmt
cp "$marked/state/$nslug.json" "$case_dir/badges/state/"
echo '{"old":true}' >"$case_dir/badges/mutation.json"
verdict
check "failure marker: no badge while it exists" \
  '[[ $status -eq 0 && "$(jq -c . "$case_dir/badges/mutation.json")" == "{\"old\":true}" ]] && grep -q "internal/numfmt (the last scan failed)" "$case_dir/out"'
prepare marker-pass "[$shard1,$shard2]" $numfmt
cp "$marked/state/$nslug.json" "$case_dir/badges/state/"
verdict
check "failure marker: a later pass replaces it" \
  '[[ $status -eq 0 && "$(jq -r .status "$case_dir/badges/state/$nslug.json")" == passed ]] &&
   [[ "$(bash scripts/mutation-plan.sh --stale "$case_dir/badges" $numfmt)" == reuse ]]'

# N3: an unreadable report fails only the package of its artifact directory.
prepare unreadable "[$(shard mutation-internal-numfmt-1 $numfmt $nslug 1 1 "[$cell1]"),$(shard r $render $rslug 1 1 "[$rcell]")]" $numfmt $render
echo 'not json' >"$case_dir/artifacts/mutation-internal-numfmt-1/shard.json"
verdict
check "unreadable report: only its package fails" \
  '[[ $status -eq 1 && -f "$case_dir/badges/state/$rslug.json" ]] && grep -q "FAILED: ./internal/numfmt: cannot read" "$case_dir/out" &&
   [[ "$(jq -r .status "$case_dir/badges/state/$nslug.json")" == failed ]]'
prepare unreadable-anonymous "[$(shard a $numfmt $nslug 1 1 "[$cell1]")]" $numfmt
echo 'not json' >"$case_dir/artifacts/a/shard.json"
verdict
check "unreadable report with no package in its directory name: the verdict stops" \
  '[[ $status -eq 2 && ! -f "$case_dir/badges/state/$nslug.json" ]]'

prepare foreign-report "[$(shard a $numfmt $nslug 1 1 "[$cell1]"),$(shard x ./internal/shape internal-shape 1 1 "[]" | jq -c '. + {not_in_matrix: true}')]" $numfmt
verdict
check "report of a package that is not in the plan: the verdict stops, nothing written" \
  '[[ $status -eq 2 && ! -f "$case_dir/badges/state/$nslug.json" ]]'

# F2 and E2: the parse and pack steps of the plan, on prepared dry runs. The plan holds one
# dry run for each mutator, with only that mutator enabled. pack_case <name> <seconds per
# mutant> <mutator> <dry run text> [<mutator> <dry run text>]...
pack_case() {
  case_dir="$work/pack-$1"
  mkdir -p "$case_dir/internal-numfmt.dry"
  printf './internal/numfmt\tinternal-numfmt\t%s\ttest\n' "$2" >"$case_dir/stale.tsv"
  shift 2
  while [[ $# -ge 2 ]]; do
    printf '%b' "$2" >"$case_dir/internal-numfmt.dry/${1/\//+}.dry"
    shift 2
  done
  bash scripts/mutation-plan.sh --pack "$case_dir" >"$case_dir/plan.json" 2>"$case_dir/out"
  status=$?
}
pack_case good 15 \
  branch/if 'internal/numfmt/a.go:\n\tbranch/if: 3\ninternal/numfmt/b.go:\n\tbranch/if: 1\n\nPer-mutator totals across all files:\n  branch/if 4\n\nTotal: 4 mutation(s) would be generated.\n' \
  statement/return 'internal/numfmt/a.go:\n\tstatement/return: 2\n\nPer-mutator totals across all files:\n  statement/return 2\n\nTotal: 2 mutation(s) would be generated.\n'
check "pack: cells sum to the Total of each dry run" \
  '[[ $status -eq 0 && "$(jq "[.[].cells[].mutants] | add" "$case_dir/plan.json")" == 6 ]]'
# mutago merges identical edits of different mutators. With all mutators enabled, a merged
# edit counts for one mutator only (branch/case: 1 statement/return: 1). A shard runs
# statement/return alone, so it tests 4 edits. The plan must use the count of that dry run
# (statement/return: 4), because the verdict refuses a report with more mutants than planned.
pack_case single-mutator 15 \
  branch/case 'internal/numfmt/a.go:\n\tbranch/case: 4\n\nTotal: 4 mutation(s) would be generated.\n' \
  statement/return 'internal/numfmt/a.go:\n\tstatement/return: 4\n\nTotal: 4 mutation(s) would be generated.\n'
check "pack: a cell holds the count of the dry run with only its mutator enabled" \
  '[[ $status -eq 0 && "$(jq -c "[.[].cells[] | select(.mutator == \"statement/return\") | .mutants]" "$case_dir/plan.json")" == "[4]" ]]'
pack_case wrong-mutator 15 \
  statement/return 'internal/numfmt/a.go:\n\tbranch/case: 1\n\nTotal: 1 mutation(s) would be generated.\n'
check "pack: a dry run that counts another mutator stops the plan" \
  '[[ $status -ne 0 ]] && grep -q "for statement/return counts the mutator branch/case" "$case_dir/out"'
pack_case mismatch 15 branch/if 'internal/numfmt/a.go:\n\tbranch/if: 3\n\nTotal: 7 mutation(s) would be generated.\n'
check "pack: cells that do not sum to the Total stop the plan" \
  '[[ $status -ne 0 ]] && grep -q "hold 3 mutants, but the dry run Total is 7" "$case_dir/out"'
pack_case no-cells 15 branch/if 'something new\n\tbranch/if 3\n\nTotal: 3 mutation(s) would be generated.\n'
check "pack: a Total with no parsed cells stops the plan" \
  '[[ $status -ne 0 ]] && grep -q "has 3 mutants but no per-file cells" "$case_dir/out"'
pack_case no-total 15 branch/if 'internal/numfmt/a.go:\n\tbranch/if: 3\n'
check "pack: a dry run with no Total line stops the plan" '[[ $status -ne 0 ]] && grep -q "has no Total line" "$case_dir/out"'
pack_case no-dry 15
check "pack: a package with no dry run stops the plan" '[[ $status -ne 0 ]] && grep -q "has no dry run" "$case_dir/out"'
pack_case zero 15 branch/if '\nTotal: 0 mutation(s) would be generated.\n'
check "pack: no mutants gives one empty shard" '[[ $status -eq 0 && "$(jq -c "map(.cells | length)" "$case_dir/plan.json")" == "[0]" ]]'
# (99 + 1) x 95 s is about 158 min: over the budget, under the job limit.
pack_case over-budget 95 branch/if 'internal/numfmt/a.go:\n\tbranch/if: 99\n\nTotal: 99 mutation(s) would be generated.\n'
check "pack: a cell over the 120 min budget gets its own shard and a warning" \
  '[[ $status -eq 0 ]] && grep -q "WARNING" "$case_dir/out"'
# (99 + 1) x 175 s is about 292 min: over the job limit.
pack_case over-limit 175 branch/if 'internal/numfmt/a.go:\n\tbranch/if: 99\n\nTotal: 99 mutation(s) would be generated.\n'
check "pack: a cell over the 285 min job limit stops the plan" \
  '[[ $status -ne 0 ]] && grep -q "more than the job limit of 285 min" "$case_dir/out"'

# F4: the fingerprint, in a small copy of the repository.
case_dir="$work/fingerprint"
mkdir -p "$case_dir"
git ls-files -z -- go.mod go.sum .mutago.yml mutago-baseline.json scripts internal/numfmt internal/render |
  tar --null -T - -cf - | tar -xf - -C "$case_dir"
git -C "$case_dir" init -q
git -C "$case_dir" add -A
fp() { (cd "$case_dir" && bash scripts/mutation-fingerprint.sh ./internal/numfmt); }
touch_file() { printf '\n' >>"$case_dir/$1"; }
base=$(fp)
check "fingerprint: the same value two times" '[[ "$(fp)" == "$base" ]]'
for path in internal/numfmt/decimal.go internal/numfmt/decimal_test.go go.sum .mutago.yml \
  scripts/mutation-gate.sh scripts/mutation-plan.sh scripts/mutation-shard.sh \
  scripts/mutation-merge.sh scripts/mutation-verdict.sh scripts/mutation-summary.sh; do
  before=$(fp)
  touch_file "$path"
  check "fingerprint: a change of $path changes it" '[[ "$(fp)" != "$before" ]]'
done
before=$(fp)
touch_file internal/render/json.go
check "fingerprint: a change of another package does not change it" '[[ "$(fp)" == "$before" ]]'
before=$(fp)
jq '.mutants |= map(if (.file | startswith("internal/numfmt/")) then .line += 1 else . end)' \
  "$case_dir/mutago-baseline.json" >"$case_dir/b.tmp" && mv "$case_dir/b.tmp" "$case_dir/mutago-baseline.json"
check "fingerprint: a baseline entry of the package changes it" '[[ "$(fp)" != "$before" ]]'

# The rate table of the plan. A package with no state uses its starting rate, any other
# package 15, and the state always has priority. Each backend package of the module has a
# starting rate, so no package reaches the 180 s backend default today.
case_dir="$work/rates"
mkdir -p "$case_dir/state"
rate() { bash scripts/mutation-plan.sh --rate "$case_dir" "$1"; }
check "rate: no state, table rate (./drivers/couchbase 156)" '[[ "$(rate ./drivers/couchbase)" == 156 ]]'
check "rate: no state, measured table rate (./drivers/hbase 26)" '[[ "$(rate ./drivers/hbase)" == 26 ]]'
check "rate: no state, other default (./internal/numfmt 15)" '[[ "$(rate ./internal/numfmt)" == 15 ]]'
echo '{"secondsPerMutant": 42.5}' >"$case_dir/state/drivers-couchbase.json"
echo '{"secondsPerMutant": null}' >"$case_dir/state/cmd.json"
check "rate: the state has priority over the table (42.5)" '[[ "$(rate ./drivers/couchbase)" == 42.5 ]]'
check "rate: a state with no rate uses the table (./cmd 25)" '[[ "$(rate ./cmd)" == 25 ]]'

echo "mutation-verdict tests: $passed passed, $failed failed"
[[ "$failed" -eq 0 ]]
