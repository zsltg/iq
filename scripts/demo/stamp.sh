#!/usr/bin/env bash
# Keeps docs/docs/assets/demo.svg honest about the code it shows.
#
# The SVG is a recording of iq driven against a seeded MongoDB. Nothing in a build
# tells you when the code it depicts has moved on, so the recording rots silently:
# the README keeps showing output that iq no longer prints. This script writes
# docs/demo.stamp, the hash of every source the recording was made from, and
# re-checks it, so a diverged SVG is a failed gate rather than a thing somebody
# eventually notices.
#
#   scripts/demo/stamp.sh write   hash the sources and write docs/demo.stamp (make demo-record)
#   scripts/demo/stamp.sh check   re-hash and compare; exit 1, naming the files, when they differ
#
# The check deliberately needs nothing but git and sha256sum: expect, asciinema
# and termsvg are needed to record the SVG, and CI has none of them. Hashing what a recording was made from needs none of it.
set -euo pipefail

# C collation, so the sort order the stamp is written in is the order it is read
# back in on every machine.
export LC_ALL=C

cd "$(git rev-parse --show-toplevel)" || exit 1

# The manifest lives beside the artifact it vouches for, under docs/ but outside
# the site's docs_dir, so it is never published as a page.
STAMP=docs/demo.stamp

# The sources docs/docs/assets/demo.svg is recorded FROM: the harness that drives
# the recording, the world it fabricates, and the code that paints the frames.
# Change one of these and the pixels change, so the SVG must be recorded again.
#
# Deliberately absent: drivers/mongo, internal/pushdown, internal/query,
# internal/selector. They decide WHICH rows come back rather than how one is drawn,
# and hashing them would fire this gate on most commits in the repo, a gate that
# cries wolf is a gate people learn to re-run blindly. seed.sh asserts the world it
# records instead, which is the stronger check: it fails when the demo would start
# telling a lie, not when a neighbouring file moved.
INPUTS=(
  scripts/demo        # basic.exp, seed.sh, demo.mk: the recorder and every knob it reads
  scripts/seed-mongo.sh # the four books on screen
  cmd/explain.go      # the query plan the recording pauses on
  cmd/output.go       # the JSON writer
  cmd/gron.go         # the gron writer the recording ends on
  cmd/color.go        # every color in every frame
  cmd/errrender.go    # what an error would look like if one appeared
  cmd/source.go       # `iq add`, and the handle it echoes
  internal/render     # the JSON colorizer the writers above hand their values to
  internal/jqfmt      # the pretty-printed filter inside the plan
)

# Trim the input roots to what can actually reach the screen. Tests never render a
# pixel, and the prose beside the scripts does not either; hashing them would fire
# the gate on commits that cannot change the recording. (git pathspec globs match at
# any depth: these are fnmatch without FNM_PATHNAME.)
EXCLUDES=(
  ':(exclude)*_test.go'
  ':(exclude)*.md'
)

# The stamp explains itself to whoever opens it, and to whoever is about to "fix" a
# failing gate by editing it.
header() {
  cat <<'EOF'
# docs/docs/assets/demo.svg's provenance: the sources it was recorded from, and their hashes.
# Written by `make demo-record` (scripts/demo/stamp.sh), never hand-edit it.
#
# `make demo-check` re-hashes these files and fails when they no longer match,
# because a SVG recorded from sources that have since moved shows output that iq no
# longer prints. Re-record with `make demo`; commit the SVG and this file together.
#
# sha256 of each file's bytes (CR stripped), sorted by path.
EOF
}

# manifest lists the input set and hashes it from the working tree, one
# "sum  path" line per source, sorted by path.
#
# The paths come from git (cached AND untracked-but-not-ignored, so a new file that
# nobody has `git add`ed still counts, it paints pixels either way), but the CONTENT
# is read from the working tree, so an uncommitted edit diverges the stamp too. Both
# halves are needed: git alone would hash the index, and the index is not what got
# recorded.
#
# CR is stripped before hashing: the repo carries no .gitattributes, so a Windows
# checkout with core.autocrlf=true holds the same sources with fatter line endings,
# and a stamp that disagreed across platforms would fail the gate for everyone but
# its author.
manifest() {
  local paths path sum
  paths=$(git ls-files -z --cached --others --exclude-standard -- "${INPUTS[@]}" "${EXCLUDES[@]}" | tr '\0' '\n' | sort -u)
  if [ -z "$paths" ]; then
    echo "demo-stamp: no demo sources found under ${INPUTS[*]}, is this an iq checkout?" >&2
    return 1
  fi
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    # A tracked file git lists but the tree does not hold is a deletion in progress;
    # drop it here, and let `check` report it as removed.
    [ -f "$path" ] || continue
    sum=$(tr -d '\r' <"$path" | sha256sum | cut -d' ' -f1)
    printf '%s  %s\n' "$sum" "$path"
  done <<<"$paths"
}

# write hashes the current sources and writes the stamp. `make demo-record` runs it
# after the SVG renders, never before: a stamp written ahead of a failed render would
# vouch for a recording that was never made.
write() {
  local body
  body=$(manifest)
  { header; printf '%s\n' "$body"; } >"$STAMP"
  printf 'demo-stamp: stamped %s from %d source(s)\n' "$STAMP" "$(printf '%s\n' "$body" | wc -l)"
}

# group prints one labelled block of the report, the label right-aligned so the paths
# line up in a column. An empty group prints nothing: the common case is a handful of
# changed files, and empty "added:"/"removed:" headings would bury it.
group() {
  local label=$1 lines=$2 first=1 kind path
  while IFS=$'\t' read -r kind path; do
    [ "$kind" = "$label" ] || continue
    if [ "$first" = 1 ]; then
      printf '%10s %s\n' "$label:" "$path"
      first=0
    else
      printf '%10s %s\n' "" "$path"
    fi
  done <<<"$lines"
}

# check re-hashes the sources and compares them with the committed stamp. It reports
# the divergence rather than just its existence: "run make demo" is only actionable
# when you can see what moved. Added and removed are tracked apart from changed
# because they read differently to a human: a changed file means the SVG is stale, an
# added one means the input set grew a file nobody has recorded yet, and a removed one
# is usually a rename the stamp has not seen.
check() {
  if [ ! -f "$STAMP" ]; then
    echo "demo-check: no $STAMP, record the demo with \`make demo\`" >&2
    return 1
  fi

  local current recorded diffs
  current=$(manifest)
  recorded=$(grep -vE '^[[:space:]]*(#|$)' "$STAMP" || true)

  diffs=$(awk '
    NR == FNR { want[$2] = $1; next }
    {
      got[$2] = 1
      if (!($2 in want))            { print "added\t"   $2 }
      else if (want[$2] != $1)      { print "changed\t" $2 }
    }
    END { for (p in want) if (!(p in got)) print "removed\t" p }
  ' <(printf '%s\n' "$recorded") <(printf '%s\n' "$current") | sort)

  if [ -n "$diffs" ]; then
    {
      echo "demo-check: docs/docs/assets/demo.svg was recorded against different sources, re-record with \`make demo\`"
      group changed "$diffs"
      group added "$diffs"
      group removed "$diffs"
    } >&2
    return 1
  fi
  printf 'demo-stamp: docs/docs/assets/demo.svg matches its %d source(s)\n' "$(printf '%s\n' "$current" | wc -l)"
}

case "${1:-}" in
  write) write ;;
  check) check ;;
  *)
    echo "usage: scripts/demo/stamp.sh <write|check>" >&2
    exit 2
    ;;
esac
