"""Makes the fixture artifacts for scripts/test/mutation-verdict.sh.

Usage: mutation-verdict-fixtures.py <spec.json> <case-dir>

The spec gives the identity of the run and a list of shards:

    {"identity": {...},
     "shards": [{"dir": "a", "package": "./internal/numfmt", "slug": "internal-numfmt",
                 "shard": 1, "shards": 2, "seconds": 100, "failedCells": 0,
                 "identity_patch": {"commit": "0000000"}, "not_in_matrix": false,
                 "cells": [{"file": "...", "mutator": "...", "planned": 3, "stated": 3,
                            "mutants": [["<checksum>", "<status>", "<id>"], ...]}]}]}

For each shard the script writes <case-dir>/artifacts/<dir>/shard.json and, for each
cell, cells/NNN/report.json and cells/NNN/mutago-agentic.json, in the shape that
scripts/mutation-shard.sh writes. A status is killed, escaped, notCovered, skipped or
errored. An escaped mutant also goes into mutago-agentic.json with its id. The report
states the number of its mutants in stats.totalMutantsCount; "stated" replaces that
number. The script also writes <case-dir>/plan.json, in the format of
scripts/mutation-plan.sh, from the shards that have no not_in_matrix flag. The planned
mutants of a cell are the number of its mutants, or "planned" when it is given.
"""

import json
import os
import sys

STATUSES = ("killed", "escaped", "notCovered", "skipped", "errored")


def main():
    spec_path, out = sys.argv[1], sys.argv[2]
    with open(spec_path, encoding="utf-8") as handle:
        spec = json.load(handle)
    matrix = []
    for shard in spec["shards"]:
        folder = os.path.join(out, "artifacts", shard["dir"])
        cells = []
        for index, cell in enumerate(shard["cells"], start=1):
            cell_dir = os.path.join(folder, "cells", "{:03d}".format(index))
            os.makedirs(cell_dir, exist_ok=True)
            report = {status: [] for status in STATUSES}
            report["stats"] = {"totalMutantsCount": cell.get("stated", len(cell["mutants"]))}
            agentic = []
            for checksum, status, mutant_id in cell["mutants"]:
                report[status].append({
                    "checksum": checksum,
                    "diff": "",
                    "mutator": {
                        "mutatorName": cell["mutator"],
                        "originalFilePath": cell["file"],
                        "originalStartLine": 1,
                    },
                })
                if status == "escaped":
                    agentic.append({"id": mutant_id, "checksum": checksum,
                                    "file": cell["file"], "mutator": cell["mutator"]})
            with open(os.path.join(cell_dir, "report.json"), "w", encoding="utf-8") as handle:
                json.dump(report, handle)
            with open(os.path.join(cell_dir, "mutago-agentic.json"), "w", encoding="utf-8") as handle:
                json.dump({"mutants": agentic}, handle)
            cells.append({"file": cell["file"], "mutator": cell["mutator"],
                          "mutants": cell.get("planned", len(cell["mutants"]))})
        identity = dict(spec["identity"])
        identity.update(shard.get("identity_patch", {}))
        os.makedirs(folder, exist_ok=True)
        with open(os.path.join(folder, "shard.json"), "w", encoding="utf-8") as handle:
            json.dump({
                "package": shard["package"],
                "slug": shard["slug"],
                "shard": shard["shard"],
                "shards": shard["shards"],
                "seconds": shard.get("seconds", 100),
                "identity": identity,
                "cells": [{"file": c["file"], "mutator": c["mutator"]} for c in cells],
                "failedCells": shard.get("failedCells", 0),
            }, handle)
        if not shard.get("not_in_matrix"):
            matrix.append({"package": shard["package"], "slug": shard["slug"],
                           "shard": shard["shard"], "shards": shard["shards"], "cells": cells})
    with open(os.path.join(out, "plan.json"), "w", encoding="utf-8") as handle:
        json.dump(matrix, handle)


if __name__ == "__main__":
    main()
