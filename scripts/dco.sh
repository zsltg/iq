#!/usr/bin/env bash
# Developer Certificate of Origin check: every non-merge commit in BASE..HEAD
# must carry a Signed-off-by trailer with the author's email address
# (git commit -s adds it). Merge commits are skipped: they add no new work.
#
#   bash scripts/dco.sh <base> <head>
#
# CI runs it on each pull request (the dco job); run it locally the same way,
# for example bash scripts/dco.sh origin/main HEAD.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: bash scripts/dco.sh <base> <head>" >&2
  exit 2
fi
base=$1
head=$2

fail=0
count=0
while IFS= read -r sha; do
  [ -n "$sha" ] || continue
  count=$((count + 1))
  author="$(git log -1 --format='%ae' "$sha" | tr '[:upper:]' '[:lower:]')"
  signoffs="$(git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha" | tr '[:upper:]' '[:lower:]')"
  if ! grep -qF "<$author>" <<<"$signoffs"; then
    echo "dco: $(git log -1 --format='%h %s' "$sha")" >&2
    echo "dco:   no Signed-off-by for the author <$author>" >&2
    fail=1
  fi
done < <(git rev-list --no-merges "$base..$head")

if [ "$fail" -ne 0 ]; then
  echo "dco: sign off each commit with git commit -s (see CONTRIBUTING.md, Developer Certificate of Origin)" >&2
  exit 1
fi
echo "dco: $count commit(s) signed off"
