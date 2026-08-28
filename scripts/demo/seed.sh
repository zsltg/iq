#!/usr/bin/env bash
# Seeds the hermetic world the recording in this directory narrates: the example
# books collection in the local MongoDB, an isolated HOME, config and cache, and the
# iq binary the demo shell types `iq` at. Nothing it touches is real, not your $HOME,
# not your iq config, not your keyring.
#
#   docker compose up -d --wait mongo   # the one container the demo needs
#   scripts/demo/seed.sh                # rebuild the sandbox at /tmp/iq-demo
#   . /tmp/iq-demo/env.sh && bash --norc --noprofile -i   # drive it by hand
#
# `make demo-record` runs this with DEMO_ROOT=/tmp/iq-demo, so the paths on screen
# are neutral and identical on every machine.
#
# WARNING: rm -rf's $DEMO_ROOT (default /tmp/iq-demo). Pass a scratch path.
set -euo pipefail

# Resolved by path, not by `git rev-parse --show-toplevel`, so a worktree with a .git
# file resolves the same way a plain checkout does.
REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DEMO_ROOT=${DEMO_ROOT:-/tmp/iq-demo}
BIN=${IQ_BIN:-"$REPO_ROOT/iq"}

[ -x "$BIN" ] || {
  echo "seed: no iq binary at $BIN, run 'make build' first" >&2
  exit 1
}

# The demo root holds a config and a cache, so it is never handed a path someone else
# could have pre-created: a symlink here would redirect the rm -rf below, and a
# directory we do not own could be world-readable. Refuse both, then create it 0700
# ourselves. (`make demo-record` points this at a short, neutral path so the paths in
# the recording are not the recorder's home directory.)
if [ -e "$DEMO_ROOT" ] || [ -L "$DEMO_ROOT" ]; then
  [ -L "$DEMO_ROOT" ] && {
    echo "seed: $DEMO_ROOT is a symlink, refusing" >&2
    exit 1
  }
  [ -d "$DEMO_ROOT" ] || {
    echo "seed: $DEMO_ROOT is not a directory, refusing" >&2
    exit 1
  }
  [ -O "$DEMO_ROOT" ] || {
    echo "seed: $DEMO_ROOT is not owned by you, refusing" >&2
    exit 1
  }
  rm -rf "$DEMO_ROOT"
fi
mkdir -m 700 -p "$DEMO_ROOT/home" "$DEMO_ROOT/config" "$DEMO_ROOT/cache" "$DEMO_ROOT/bin"

# 1. the data on screen: the four books scripts/seed-mongo.sh writes. It talks to the
#    compose service, so the container has to be up already (`make demo-record` brings
#    it up, and only it, before calling this).
"$REPO_ROOT/scripts/seed-mongo.sh" >/dev/null

# 2. the binary the demo's shell resolves as `iq`. Copied in rather than put on PATH
#    from the checkout, so nothing the recording runs can reach the working tree, and
#    so `iq` is what the reader would type rather than a ./path into somebody's
#    checkout.
cp "$BIN" "$DEMO_ROOT/bin/iq"

# 3. the environment the recording (or a human) drives the demo with. IQ_CONFIG is a
#    path that does not exist yet: the recording's first command is `iq add`, and it
#    has to start from an empty config for the handle it prints to be the demo's own.
cat >"$DEMO_ROOT/env.sh" <<EOF
export HOME="$DEMO_ROOT/home"
export XDG_CONFIG_HOME="$DEMO_ROOT/config"
export XDG_CACHE_HOME="$DEMO_ROOT/cache"
export IQ_CONFIG="$DEMO_ROOT/config/iq.toml"
export PATH="$DEMO_ROOT/bin:\$PATH"
export IQ_DEMO_ROOT="$DEMO_ROOT"
export PS1='\$ '
EOF

# 4. assert the world the recording narrates. The GIF is a claim about this data: four
#    books, "2" is Kleppmann's, exactly two of them published after 2015, and exactly
#    two authors matching /Martin/ with Robert C. Martin the last of them. Reshape the
#    dataset in scripts/seed-mongo.sh and the recording still succeeds, and the GIF
#    shows different rows under narration that no longer describes them. Fail here,
#    before a frame is recorded, rather than shipping a plausible lie.
#
#    These expectations and scripts/demo/basic.exp's `await` strings are one fact,
#    change them together, never one alone.
#
#    Run through a throwaway config of its own, so the recording's `iq add` still
#    starts from nothing.
seeded() {
  HOME="$DEMO_ROOT/home" XDG_CONFIG_HOME="$DEMO_ROOT/config" XDG_CACHE_HOME="$DEMO_ROOT/cache" \
    IQ_CONFIG="$DEMO_ROOT/seed.toml" "$BIN" "$@"
}

fail() {
  echo "seed: $1" >&2
  echo "seed: the demo's narration (scripts/demo/basic.exp) no longer matches the data scripts/seed-mongo.sh writes." >&2
  printf '%s\n' "${collection:-}" >&2
  exit 1
}

seeded add -a "mongodb://localhost:27017/iq?collection=books" >/dev/null

collection=$(seeded '.[] | "\(._id)  \(.year)  \(.title), \(.author)"')
total=$(printf '%s\n' "$collection" | grep -c .)
recent=$(seeded '.[] | select(.year > 2015) | .title' | grep -c . || true)
martins=$(seeded '.[] | select(.author | test("Martin")) | .author' | grep -c . || true)
title2=$(seeded '.["2"] | .title')

[ "$total" = 4 ] || fail "the books collection now holds $total document(s), not 4"
[ "$title2" = '"Designing Data-Intensive Applications"' ] || fail "key \"2\" is now $title2, not Kleppmann's book, which is the one the recording reads by key"
[ "$recent" = 2 ] || fail "$recent book(s) now have year > 2015, not 2, which is the count the pushdown screen shows"
[ "$martins" = 2 ] || fail "$martins author(s) now match /Martin/, not 2, which is the count the gron screen ends on"
seeded '.[] | select(.author | test("Martin")) | .author' | grep -q "Robert C. Martin" \
  || fail "no author 'Robert C. Martin', which is the row the last frame rests on"

echo "seeded $DEMO_ROOT, source $DEMO_ROOT/env.sh to drive it"
printf '%s\n' "$collection"
