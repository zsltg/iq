#!/usr/bin/env bash
# Make sure that a commit message holds no email address except the allowed ones.
#
# An address in a message stays in history once it is pushed, and a hand-typed
# Signed-off-by line can carry an address that no other guard sees. The hooks in
# .githooks check the author and committer fields. This script checks the
# message text.
#
# Every address in a message must be one of these:
#   1. The author email or the committer email of the same commit.
#   2. An address that matches the allowlist below.
#
# Rule 1 lets an outside contributor sign off with their own public address.
# Addresses are found with a simple pattern. Only ASCII letters change case, so
# the comparison is byte-exact for all other bytes.
#
#   bash scripts/commit-emails.sh --message <file>
#   bash scripts/commit-emails.sh <rev-list arguments>
#
# The first form checks a message file before the commit exists (the commit-msg
# hook uses it). The identities come from git var, so a --author or a -c override
# counts. Everything after a scissors line is ignored, as git removes it in the
# verbose modes. Lines that start with # are checked. Git runs the hook before
# it cleans up the message, and it keeps those lines when no editor runs, for
# example with git commit -m. This choice fails closed: an address in a comment
# line is rejected even when git would strip the line. The template comment of
# git holds no address except the author address, which the rule allows. The
# second form checks each commit of a range,
# merge commits included, for example origin/main..HEAD (the pre-push hook and
# the dco job use it).
#
# Exit status: 0 when clean, 1 when an address is not allowed, 2 on a usage
# error or a range that does not resolve.
set -euo pipefail

# The allowlist: shell glob patterns, in lower case. This is the only place
# that lists the allowed addresses.
allowlist=(
  '*@users.noreply.github.com'
  'noreply@github.com'
  'noreply@anthropic.com'
  'noreply@openai.com'
)

# The address pattern. It is simple on purpose. It finds the usual forms and
# can miss an unusual one. grep runs with LC_ALL=C, so it reads bytes. The
# class u holds ASCII characters and all bytes of 0x80 and above, so a UTF-8
# character counts as part of a name. The top-level domain is either 2 or more
# ASCII letters, or a label that holds at least one byte of 0x80 or above.
hi=$'\x80-\xff'
address_pattern="[A-Za-z0-9._%+${hi}-]+@[A-Za-z0-9.${hi}-]+\\.([A-Za-z]{2,}|[A-Za-z0-9${hi}-]*[${hi}][A-Za-z0-9${hi}-]*)"

usage() {
  echo "usage: bash scripts/commit-emails.sh --message <file>" >&2
  echo "       bash scripts/commit-emails.sh <rev-list arguments>" >&2
  exit 2
}

lower() { LC_ALL=C tr '[:upper:]' '[:lower:]'; }

# allowed <address> <author> <committer>: the address is the author or the
# committer address, or it matches the allowlist. All values are in lower case.
allowed() {
  local pattern
  if [ "$1" = "$2" ] || [ "$1" = "$3" ]; then
    return 0
  fi
  for pattern in "${allowlist[@]}"; do
    # shellcheck disable=SC2053 # the pattern is a glob on purpose.
    if [[ "$1" == $pattern ]]; then
      return 0
    fi
  done
  return 1
}

# check_text <label> <author> <committer> <text file>: print each address in
# the text that is not allowed. Return 1 when there is one.
check_text() {
  local label=$1 author=$2 committer=$3 file=$4 address bad=""
  while IFS= read -r address; do
    if ! allowed "$address" "$author" "$committer"; then
      bad+="$address"$'\n'
    fi
  done < <(LC_ALL=C grep -oE "$address_pattern" "$file" | lower | LC_ALL=C sort -u || true)
  if [ -z "$bad" ]; then
    return 0
  fi
  while IFS= read -r address; do
    echo "commit-emails: $label has the address <$address>," >&2
    echo "commit-emails:   which is not the author, the committer or an allowed address" >&2
  done <<<"${bad%$'\n'}"
  return 1
}

if [ "$#" -eq 0 ]; then
  usage
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [ "$1" = "--message" ]; then
  if [ "$#" -ne 2 ]; then
    usage
  fi
  if [ ! -r "$2" ]; then
    echo "commit-emails: cannot read the message file $2" >&2
    exit 2
  fi
  # git var fails when git has no identity for the commit.
  if ! author=$(git var GIT_AUTHOR_IDENT) || ! committer=$(git var GIT_COMMITTER_IDENT); then
    echo "commit-emails: cannot resolve the author or committer identity" >&2
    exit 2
  fi
  author=$(sed -n 's/.*<\(.*\)>.*/\1/p' <<<"$author" | lower)
  committer=$(sed -n 's/.*<\(.*\)>.*/\1/p' <<<"$committer" | lower)
  # Cut the text at the scissors line. Keep the comment lines (see the header).
  awk '/^# -+ >8 -+$/ { exit } { print }' "$2" >"$work/message"
  if ! check_text "the message" "$author" "$committer" "$work/message"; then
    cat >&2 <<EOF
commit-emails: do not type an address into a commit message.
commit-emails: git commit -s adds the sign-off with your configured address.
EOF
    exit 1
  fi
  exit 0
fi

# The range must resolve. A failure inside the loop's input would otherwise
# look like a range with no commits, which passes.
if ! shas=$(git rev-list "$@"); then
  echo "commit-emails: cannot read the commit range $*" >&2
  exit 2
fi

fail=0
count=0
for sha in $shas; do
  count=$((count + 1))
  read -r author committer < <(git log -1 --format='%ae %ce' "$sha" | lower)
  git log -1 --format='%B' "$sha" >"$work/message"
  label=$(git log -1 --format='commit %h (%s)' "$sha")
  if ! check_text "$label" "$author" "$committer" "$work/message"; then
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  cat >&2 <<EOF
commit-emails: do not type an address into a commit message.
commit-emails: git commit -s adds the sign-off with your configured address.
commit-emails: a later commit does not remove the address from history.
commit-emails: rewrite the message of each commit named above before you push:
commit-emails:   git commit --amend -s for the commit at the tip of the branch,
commit-emails:   or a rebase that edits the message of an older commit.
commit-emails: if the commit is already pushed, the rewrite needs a force-push.
EOF
  exit 1
fi
echo "commit-emails: $count commit(s) checked"
