#!/usr/bin/env bash
# Gives the verdict of the CI `mutate-diff` job and merges the reports of its shards.
#
#   scripts/mutation-diff-verdict.sh <artifacts-dir> <plan.json> [out-dir]
#
# <artifacts-dir>  the shard artifacts of scripts/mutation-diff-shard.sh (any depth); it can
#                  be empty when the plan is empty
# <plan.json>      the output of `scripts/mutation-plan.sh --diff` for this run
# [out-dir]        where the merged files go (default: the current directory)
#
# The environment variables PLAN_RESULT and SHARDS_RESULT hold the results of the CI jobs
# `mutate-plan` and `mutate-diff` (success, failure, cancelled or skipped). The verdict fails
# when the plan did not succeed, or when the shards neither succeeded nor were skipped.
# Without the variables, only the artifacts decide.
#
# The verdict is the second line of defense after the shards. A shard already fails on an
# escape or an errored mutant. This script also fails when:
#   - a shard of the plan has no shard.json (a timeout or a cancel kills the runner before
#     it writes the file), or has two
#   - the identity of a shard is not the identity of this checkout (the commit, the merge-base
#     of the plan, the mutago version, the sha256 of .mutago.yml and mutago-baseline.json),
#     or two shards used different Go versions
#   - a group failed, a mutant errored, or an escape is new (its id is not in the baseline)
# A file with many changed code lines and no mutant only gives a warning (::warning:: and a
# line in summary.md): see scripts/mutation-diff-shard.sh.
#
# Output in [out-dir]:
#   mutago-agentic.json                the escaped mutants of all shards
#   mutago-baseline.candidate.json     the committed baseline plus the new escapes, only when
#                                      there is a new escape. Copy it over the baseline only
#                                      for genuine equivalents, each with a note in
#                                      mutago-baseline.notes.md
#   summary.md                         one table row for each shard (the step summary)
# The ids of an escape are the ids of a package run, because the shards use absolute file
# paths (mutago#248). There is no mutator split, so no merge of ids is needed here.
#
# Exit status: 0 pass, 1 fail, 2 the input cannot be used.
set -uo pipefail

export LC_ALL=C

usage="usage: mutation-diff-verdict.sh <artifacts-dir> <plan.json> [out-dir]"
if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "$usage" >&2
  exit 2
fi
artifacts="$1"
plan="$2"
out_dir="${3:-.}"
[[ -d "$artifacts" ]] || {
  echo "mutation-diff-verdict: '$artifacts' is not a directory" >&2
  exit 2
}
[[ -f "$plan" ]] || {
  echo "mutation-diff-verdict: '$plan' is not a file" >&2
  exit 2
}
mkdir -p "$out_dir" || exit 2
artifacts=$(cd "$artifacts" && pwd -P)
plan=$(cd "$(dirname "$plan")" && pwd -P)/$(basename "$plan")
out_dir=$(cd "$out_dir" && pwd -P)

root=$(git rev-parse --show-toplevel) || exit 2
cd "$root" || exit 2

sha() { sha256sum "$1" | cut -d' ' -f1; }
identity=$(jq -n \
  --arg commit "$(git rev-parse HEAD)" \
  --arg mutago "$(sed -n 's/^MUTAGO_VERSION=//p' scripts/mutation-gate.sh)" \
  --arg config "$(sha .mutago.yml)" \
  --arg baseline "$(sha mutago-baseline.json)" \
  '{commit: $commit, mutagoVersion: $mutago, configSha256: $config, baselineSha256: $baseline}') || exit 2

python3 - "$artifacts" "$plan" "$out_dir" "$root/mutago-baseline.json" "$identity" \
  "${PLAN_RESULT-}" "${SHARDS_RESULT-}" <<'PY'
import json
import os
import sys

artifacts, plan_path, out_dir, baseline_path, identity_json, plan_result, shards_result = sys.argv[1:8]
identity = json.loads(identity_json)
errors = []
warnings = []


def stop(message):
    print("mutation-diff-verdict: " + message, file=sys.stderr)
    sys.exit(2)


def load(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, ValueError) as err:
        stop("cannot read {}: {}".format(path, err))


plan = load(plan_path)
if not isinstance(plan, list):
    stop("the plan is not a list")
baseline = load(baseline_path)
baseline_ids = {m.get("id") for m in baseline.get("mutants") or []}

if plan_result and plan_result != "success":
    errors.append("the plan job ended as {}, so no shard ran".format(plan_result))
if shards_result and shards_result not in ("success", "skipped"):
    errors.append("the shard jobs ended as {}".format(shards_result))

# Find the shard.json files, at any depth.
found = {}
for folder, _, names in os.walk(artifacts):
    if "shard.json" not in names:
        continue
    try:
        with open(os.path.join(folder, "shard.json"), encoding="utf-8") as handle:
            data = json.load(handle)
        number = data["shard"]
    except (OSError, ValueError, KeyError, TypeError) as err:
        errors.append("{}: cannot read shard.json: {}".format(os.path.relpath(folder, artifacts), err))
        continue
    found.setdefault(number, []).append((folder, data))

go_versions = set()
new_escapes = {}
all_escaped = []
rows = []
total_mutants = 0
suspect = []

for entry in plan:
    number = entry.get("shard")
    label = "shard {}/{}".format(number, entry.get("shards"))
    reports = found.pop(number, [])
    if not reports:
        errors.append("{} has no shard.json: it timed out, was cancelled or stopped early".format(label))
        rows.append((label, "-", "-", "-", "-", "-", "no report"))
        continue
    if len(reports) > 1:
        errors.append("{} has {} shard.json files".format(label, len(reports)))
        continue
    folder, data = reports[0]
    ident = data.get("identity") or {}
    go_versions.add(ident.get("goVersion"))
    if ident.get("mergeBase") != entry.get("mergeBase"):
        errors.append("{}: merge-base {} differs from the plan {}".format(label, ident.get("mergeBase"), entry.get("mergeBase")))
    for key in identity:
        if ident.get(key) != identity[key]:
            errors.append("{}: identity {} is {}, but this checkout has {}".format(label, key, ident.get(key), identity[key]))
    if data.get("failedGroups") != 0:
        errors.append("{}: {} group(s) failed (see its log)".format(label, data.get("failedGroups")))
    killed = escaped = errored = mutants = 0
    files = 0
    for group in data.get("groups") or []:
        totals = group.get("totals") or {}
        mutants += totals.get("mutants", 0)
        killed += totals.get("killed", 0)
        escaped += totals.get("escaped", 0)
        errored += totals.get("errored", 0)
        files += len(group.get("files") or [])
    if errored:
        errors.append("{}: {} mutant(s) errored or timed out; an errored mutant is not verified".format(label, errored))
    suspect.extend(data.get("suspectZero") or [])
    group_dirs = sorted(d for d in os.listdir(os.path.join(folder, "groups"))) if os.path.isdir(os.path.join(folder, "groups")) else []
    for name in group_dirs:
        path = os.path.join(folder, "groups", name, "mutago-agentic.json")
        if not os.path.isfile(path):
            continue
        for mutant in load(path).get("mutants") or []:
            all_escaped.append(mutant)
            mutant_id = mutant.get("id")
            if mutant_id not in baseline_ids and mutant_id not in new_escapes:
                new_escapes[mutant_id] = {key: mutant.get(key) for key in ("id", "file", "mutator", "line")}
    total_mutants += mutants
    rows.append((label, data.get("backend") or "-", str(files), str(mutants), str(killed), str(escaped),
                 "{} s".format(data.get("seconds"))))

for number in sorted(found):
    errors.append("shard.json of shard {} is not in the plan".format(number))
if len({v for v in go_versions}) > 1:
    errors.append("the shards used different Go versions: {}".format(sorted(str(v) for v in go_versions)))

if new_escapes:
    errors.append("{} new escape(s) (id file:line mutator):".format(len(new_escapes)))
    for item in new_escapes.values():
        errors.append("  {} {}:{} {}".format(item["id"], item["file"], item["line"], item["mutator"]))
    candidate = dict(baseline)
    candidate["mutants"] = list(baseline.get("mutants") or []) + list(new_escapes.values())
    with open(os.path.join(out_dir, "mutago-baseline.candidate.json"), "w", encoding="utf-8") as handle:
        handle.write(json.dumps(candidate, indent=1) + "\n")
with open(os.path.join(out_dir, "mutago-agentic.json"), "w", encoding="utf-8") as handle:
    json.dump({"mutants": all_escaped}, handle, indent=1)
    handle.write("\n")

for path in suspect:
    warnings.append("{} has many changed code lines but no mutant; make sure that mutago read the diff".format(path))
if plan and total_mutants == 0 and not errors:
    warnings.append("the plan has {} shard(s) but the run found no mutant at all".format(len(plan)))

lines = ["## mutate-diff", ""]
if not plan:
    lines.append("No non-test Go file changed, so there was nothing to mutate.")
else:
    lines += ["| shard | backend | files | mutants | killed | escaped | time |", "| --- | --- | --- | --- | --- | --- | --- |"]
    lines += ["| " + " | ".join(row) + " |" for row in rows]
    lines += ["", "Total mutants: {}".format(total_mutants)]
for message in warnings:
    lines += ["", "Warning: " + message]
for message in errors:
    lines += ["", "FAILED: " + message] if not message.startswith("  ") else ["    " + message.strip()]
with open(os.path.join(out_dir, "summary.md"), "w", encoding="utf-8") as handle:
    handle.write("\n".join(lines) + "\n")

for message in warnings:
    print("::warning::mutation-diff-verdict: " + message)
for message in errors:
    print(("FAILED: " + message) if not message.startswith("  ") else message, file=sys.stderr)
sys.exit(1 if errors else 0)
PY
