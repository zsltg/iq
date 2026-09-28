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

# The range must resolve. A failure inside the loop's input would otherwise
# look like a range with no commits, which passes.
if ! shas="$(git rev-list --no-merges "$base..$head")"; then
  echo "dco: cannot read the commit range $base..$head" >&2
  exit 1
fi

fail=0
count=0
for sha in $shas; do
  count=$((count + 1))
  author="$(git log -1 --format='%ae' "$sha" | tr '[:upper:]' '[:lower:]')"
  # Compare the email field of each Signed-off-by value as a whole: the value is
  # "Name <email>", so the email is the text between the last < and the >
  # that ends the value. A substring search would accept a value that only
  # mentions the author's address somewhere.
  signed=0
  while IFS= read -r value; do
    email="$(sed -n 's/^.*<\([^<>]*\)>[[:space:]]*$/\1/p' <<<"$value" | tr '[:upper:]' '[:lower:]')"
    if [ -n "$email" ] && [ "$email" = "$author" ]; then
      signed=1
    fi
  done < <(git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha")
  if [ "$signed" -ne 1 ]; then
    echo "dco: $(git log -1 --format='%h %s' "$sha")" >&2
    echo "dco:   no Signed-off-by for the author <$author>" >&2
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "dco: sign off each commit with git commit -s (see CONTRIBUTING.md, Developer Certificate of Origin)" >&2
  exit 1
fi
echo "dco: $count commit(s) signed off"
