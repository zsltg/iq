#!/usr/bin/env bash
# Merges the shard reports of ONE package into one verdict.
#
#   scripts/mutation-merge.sh <baseline.json> <shard-dir>...
#
# Each <shard-dir> holds the report.json and the mutago-agentic.json of one shard run
# (scripts/mutation-gate.sh with IQ_MUTATION_MUTATORS and a file or package target). The
# script writes one JSON object to stdout: the summed counts, the score, the checksums of
# the escaped edits, and the new escapes. It writes a short report to stderr.
#
# Why a merge is necessary. mutago merges identical edits of different mutators into one
# mutant, and the mutator that it keeps depends on the enabled mutators. A shard enables a
# small set of mutators, so the same edit can occur in two shards, each time with a
# different mutator. The stable id of a mutant includes the mutator name, so the two shards
# give two ids for one edit. A sum of the shard counts thus counts that edit two times.
#
# The merge rule. The key of an edit is its mutago checksum (stableMutationEditKey: the
# file, the source and the edit, with no mutator name), which is the same in every shard.
# Each checksum counts once. If two shards give different results for one checksum, the
# worst result applies, in this order: errored, escaped, killed, skipped, not covered. The
# script reports each such conflict. An escaped edit has one candidate id for each shard in
# which it escaped. The id comes from mutago-agentic.json, which lists every escaped mutant
# with its id and its checksum, so no id is computed here. An escaped edit is new only
# if none of its candidate ids is in the baseline. The baseline ids come from package runs,
# and a package run keeps one of the mutators that make the edit, so its id is always one
# of the candidates.
#
# The score is killed / (killed + escaped). A skipped mutant (it does not compile) and a
# mutant that no test covers count in neither term. An errored mutant is not scored: it
# fails the verdict.
#
# Exit status: 0 when no mutant errored and no escape is new, 4 when a mutant errored or an
# escape is new, 2 when the input is not usable.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: mutation-merge.sh <baseline.json> <shard-dir>..." >&2
  exit 2
fi

python3 - "$@" <<'PY'
import json
import os
import sys

baseline_path, shard_dirs = sys.argv[1], sys.argv[2:]


def fail(message):
    print("mutation merge: " + message, file=sys.stderr)
    sys.exit(2)


def load(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, ValueError) as err:
        fail("cannot read {}: {}".format(path, err))


baseline_ids = set()
if os.path.exists(baseline_path):
    baseline_ids = {entry.get("id") for entry in (load(baseline_path).get("mutants") or [])}

# File paths print relative to the current directory (a file target makes them absolute).
root = os.getcwd().rstrip("/") + "/"

# Result order for a conflict: a larger rank is worse and wins.
rank = {"notCovered": 0, "skipped": 1, "killed": 2, "escaped": 3, "errored": 4}
edits = {}
conflicts = []
for shard in shard_dirs:
    report = load(os.path.join(shard, "report.json"))
    agentic_path = os.path.join(shard, "mutago-agentic.json")
    escaped_list = report.get("escaped") or []
    ids = {}
    if escaped_list:
        if not os.path.exists(agentic_path):
            fail("{} has escaped mutants but no mutago-agentic.json".format(shard))
        for mutant in load(agentic_path).get("mutants") or []:
            ids[mutant.get("checksum")] = mutant.get("id")
    seen = set()
    for status in rank:
        for mutant in report.get(status) or []:
            checksum = mutant.get("checksum")
            if not checksum:
                fail("{}: a {} mutant has no checksum".format(shard, status))
            if checksum in seen:
                fail("{}: checksum {} occurs two times in one report".format(shard, checksum))
            seen.add(checksum)
            mutator = mutant.get("mutator") or {}
            path = mutator.get("originalFilePath", "?")
            if path.startswith(root):
                path = path[len(root):]
            edit = edits.setdefault(checksum, {
                "status": status,
                "file": path,
                "line": mutator.get("originalStartLine", 0),
                "mutators": set(),
                "ids": set(),
            })
            edit["mutators"].add(mutator.get("mutatorName", "?"))
            if status != edit["status"]:
                conflicts.append((checksum, edit["status"], status, shard))
                if rank[status] > rank[edit["status"]]:
                    edit["status"] = status
            if status == "escaped":
                if checksum not in ids:
                    fail("{}: escaped checksum {} is not in mutago-agentic.json".format(shard, checksum))
                edit["ids"].add(ids[checksum])

counts = {status: 0 for status in rank}
for edit in edits.values():
    counts[edit["status"]] += 1
killed, escaped = counts["killed"], counts["escaped"]
scored = killed + escaped

new_escapes = []
escaped_checksums = []
for checksum, edit in sorted(edits.items(), key=lambda item: (item[1]["file"], item[1]["line"], item[0])):
    if edit["status"] != "escaped":
        continue
    escaped_checksums.append(checksum)
    if not edit["ids"] & baseline_ids:
        new_escapes.append({
            "checksum": checksum,
            "file": edit["file"],
            "line": edit["line"],
            "mutators": sorted(edit["mutators"]),
            "ids": sorted(edit["ids"]),
        })

summary = {
    "totalMutantsCount": len(edits),
    "killedCount": killed,
    "notCoveredCount": counts["notCovered"],
    "escapedCount": escaped,
    "errorCount": counts["errored"],
    "skippedCount": counts["skipped"],
    "score": (killed / scored) if scored else 0,
    "escapedChecksums": escaped_checksums,
    "newEscapes": new_escapes,
    "conflicts": len(conflicts),
}
json.dump(summary, sys.stdout, indent=1)
sys.stdout.write("\n")

for checksum, first, other, shard in conflicts:
    print("mutation merge: checksum {} is {} in one shard and {} in {}".format(checksum, first, other, shard), file=sys.stderr)
for edit in new_escapes:
    print("mutation merge: NEW escape {}:{} ({}) ids {}".format(
        edit["file"], edit["line"], ",".join(edit["mutators"]), " ".join(edit["ids"])), file=sys.stderr)
if counts["errored"]:
    print("mutation merge: {} mutant(s) errored or timed out".format(counts["errored"]), file=sys.stderr)
sys.exit(4 if new_escapes or counts["errored"] else 0)
PY
