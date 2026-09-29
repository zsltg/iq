#!/usr/bin/env bash
# Developer Certificate of Origin check: every non-merge commit in BASE..HEAD
# must carry a Signed-off-by trailer with the author's email address
# (git commit -s adds it). Merge commits are skipped: they add no new work.
#
#   bash scripts/dco.sh <base> <head>
#
# CI runs it on each pull request (the dco job); run it locally the same way,
# for example bash scripts/dco.sh origin/main HEAD.
#
# A commit that has no sign-off passes when a later commit in the range
# remediates it, in the format of the DCO GitHub App. The remediation commit is
# signed off itself, has the same author email, and has this line in its message:
#
#   I, Name <email>, hereby add my Signed-off-by to this commit: <sha>
#
# The email in the line is the author email of both commits, and <sha> is the
# full hash or a prefix of at least 7 characters. So a contributor can fix a
# missing sign-off without a rewrite of the branch history.
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

lower() { tr '[:upper:]' '[:lower:]'; }

# signed_off <sha> <email>: the commit has a Signed-off-by value whose email
# field, as a whole, is <email>. The value is "Name <email>", so the email is the
# text between the last < and the > that ends the value. A substring search would
# accept a value that only mentions the address somewhere.
signed_off() {
  local value email
  while IFS= read -r value; do
    email="$(sed -n 's/^.*<\([^<>]*\)>[[:space:]]*$/\1/p' <<<"$value" | lower)"
    if [ -n "$email" ] && [ "$email" = "$2" ]; then
      return 0
    fi
  done < <(git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$1")
  return 1
}

# Remediations: "<email> <sha prefix>" for each remediation line in a signed-off
# commit whose author email is the email in the line.
remediations=""
for sha in $shas; do
  author="$(git log -1 --format='%ae' "$sha" | lower)"
  signed_off "$sha" "$author" || continue
  while IFS= read -r line; do
    email="$(sed -n 's/^I, .* <\([^<>]*\)>, hereby add my Signed-off-by to this commit: \([0-9a-fA-F]\{7,40\}\)[[:space:]]*$/\1/p' <<<"$line" | lower)"
    target="$(sed -n 's/^I, .* <\([^<>]*\)>, hereby add my Signed-off-by to this commit: \([0-9a-fA-F]\{7,40\}\)[[:space:]]*$/\2/p' <<<"$line" | lower)"
    if [ -n "$email" ] && [ "$email" = "$author" ]; then
      remediations+="$email $target"$'\n'
    fi
  done < <(git log -1 --format='%B' "$sha")
done

# remediated <sha> <email>: a remediation line with <email> names <sha>.
remediated() {
  local email target
  while read -r email target; do
    if [ "$email" = "$2" ] && [[ "$1" == "$target"* ]]; then
      return 0
    fi
  done <<<"$remediations"
  return 1
}

fail=0
count=0
for sha in $shas; do
  count=$((count + 1))
  author="$(git log -1 --format='%ae' "$sha" | lower)"
  if signed_off "$sha" "$author" || remediated "$sha" "$author"; then
    continue
  fi
  echo "dco: $(git log -1 --format='%h %s' "$sha")" >&2
  echo "dco:   no Signed-off-by for the author <$author>" >&2
  fail=1
done

if [ "$fail" -ne 0 ]; then
  cat >&2 <<EOF
dco: add the sign-off in one of two ways (see CONTRIBUTING.md, Developer Certificate of Origin):
dco:   1. when you are the author of every commit named above (your git user.email is
dco:      their author email), sign off every commit again, then force-push the branch:
dco:        git rebase --signoff $base
dco:        git push --force-with-lease
dco:   2. or add one empty commit for each commit named above, made and signed off by
dco:      the author of that commit, then push:
dco:        git commit -s --allow-empty -m "DCO remediation" -m "I, <name> <author email>, hereby add my Signed-off-by to this commit: <sha>"
EOF
  exit 1
fi
echo "dco: $count commit(s) signed off"
