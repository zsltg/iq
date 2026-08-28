# The recorded demo

`docs/docs/assets/demo.svg`, the animated terminal at the top of the README and
of the docs site's home page, generated rather than hand-captured, so it can be
re-rendered whenever iq's output changes. It is an SVG rather than a GIF because
the README column is ~900px on GitHub and ~1200px on other forges, and the docs site's
center column is 696, 766 or 835px depending on the viewport: a bitmap is sharp at
one of those widths and resampled at the rest, vector text is crisp at all of
them, and the SVG plays anywhere an `<img>` does, no script, no player.

```sh
make demo          # re-record the demo if the code it shows has changed
make demo-record   # record unconditionally (FORCE=1 make demo does the same)
make demo-check    # fail when the demo shows code that has since moved
```

The knobs and the recipes are in [`demo.mk`](demo.mk), next to the scripts they
drive: every value in there changes the recorded pixels, so it is hashed along
with this directory, and an unrelated edit to the root `Makefile` is not a
re-record.

Off-the-shelf tools, no daemon and no browser in the loop:

| | |
|---|---|
| **expect** | types the command lines. iq is a CLI, so the demo types shell lines and asserts what they print. `apt install expect` |
| **asciinema** | records the session as a `.cast`, **3.x or newer**. `cargo install --locked asciinema`, or a distro package, or a static binary from [releases](https://github.com/asciinema/asciinema/releases) |
| **termsvg** | renders the cast to an animated SVG, pinned in `demo.mk` (`TERMSVG_VERSION`). Nothing to install: `make demo-record` runs `go install github.com/mrmarble/termsvg/cmd/termsvg@<version>` into a throwaway GOBIN for the one run, the way the mutation gate provisions mutago, so it needs only the Go toolchain and reads or writes no `go.mod` |
| **python3** | runs `postrender.py`, which embeds the font subset into the render, adds the viewBox termsvg omits, and removes the cursor blink. Stdlib only |
| **uvx** (only for `make demo-font`) | regenerates the committed font subset `demo-font.woff2` from the pinned upstream release via fonttools in a throwaway environment; the docs toolchain already depends on uv |

`make demo-record` checks for the first two and names whichever is missing or too
old. **Recording** is manual: CI has neither expect nor asciinema. Noticing that
the recording has gone stale is not, see "The stamp" below.

asciinema 3 (the Rust rewrite) records **asciicast v3** and termsvg reads v2, so
the recipe runs `asciinema convert -f asciicast-v2` between the two. Do not
install the recorder from PyPI/pipx, that line ended at 2.4.0, has no
`--window-size` and no `convert`. termsvg is GPL-3.0; it stays a build-time tool
invoked as a subprocess, never enters `go.mod`, and renders an SVG that is not a
derivative work, the Tech Stack's permissive-license rule governs the shipped
binary's dependencies, which this is not.

## The font

termsvg lays every text run and every cursor on a 12px grid (its 20px font at
0.6em) but names no font of its own: a hardcoded `Monaco,Consolas,'Courier
New',monospace` stack, not a flag. The reader's browser resolves that however its
fontconfig sees fit, and on this box `fc-match Monaco` is the proportional Noto
Sans: Firefox drew text that fell short of the grid, and the cursor drifted away
from the last character as a line was typed, a defect headless browsers hid by
picking Liberation Mono. So `postrender.py` swaps the stack for an embedded
`@font-face`, a ~3.5 KB WOFF2 subset of Source Code Pro (0.6em, the grid's own
advance), and every browser draws the same glyphs at the same advance, README
and docs alike.

`demo-font.chars` lists the glyphs the subset covers (printable ASCII and the
em dash the plan uses) and is the contract: the embed fails, naming the glyphs,
when a recording types one outside it, because the fallback would be the browser
font and the drift again. Widen the list in `font.sh`, run `make demo-font` (network
and `uvx`), and record again. Source Code Pro is OFL-1.1 (`demo-font.LICENSE`): a
subset may be embedded, but not under the reserved font name, hence the internal
family name `iq-demo-mono` inside the SVG.

## What it records

[`basic.exp`](basic.exp), one pass over what iq is for, against the books
collection in the local MongoDB:

1. `iq add -a 'mongodb://…?collection=books'`, the URI is the whole
   configuration and `add` echoes the handle it derived from `?collection=`.
2. `iq '.["2"]'`, a bounded read: the collection is a jq object keyed by `_id`,
   so a key lookup is a point read rather than a scan.
3. `iq '.[] | select(.year > 2015) | {title, price}'`, a streaming scan with a
   range predicate and a projection.
4. the same command again, recalled from history with the up arrow rather than
   retyped, with `--explain -v -C | less -R` appended to it: the annotated jq
   stages with the projection among them, the mongo calls they become, and the
   pushdown section with the compiled server filter. This is the screen the whole
   recording exists for, and the one screen too tall for the terminal, so it goes
   where a person sends output taller than the screen, into a pager. Page one, a
   Space, page two, `q`. `-C` is what keeps iq's colors across the pipe, `-R` is
   what makes `less` pass them through.
5. `iq '.[] | select(.author | test("Martin"))' -g`, a regex predicate pushed
   down the same way, printed as gron: one assignment per leaf, greppable,
   reversible with `ungron`.

## A recording is a claim, so it is asserted

Reshape the seeded books, rename a section of the plan, drop a color, and the
recording still succeeds while the SVG shows output iq no longer prints. That is
the failure worth designing against: a stale SVG merely looks dated, a lying one
misinforms.

So the demo checks its own story, in two places:

- [`seed.sh`](seed.sh) asserts the data before a frame is recorded: four books,
  `"2"` is Kleppmann's, exactly two published after 2015, exactly two authors
  matching `/Martin/` with Robert C. Martin among them. A reshaped dataset in
  `scripts/seed-mongo.sh` fails here, with the actual collection printed beside
  the expectation.
- `basic.exp` asserts every screen before moving on: `added` then `books` from
  `add`, `Kleppmann`, `Philosophy`, `calls:` on the pager's first page and then
  `object` and `END` on its second, `Robert` then `2008`. Every one of them is a
  token the screen it names actually paints, read off a real recording rather than
  guessed at, and a token that never comes aborts the run instead of being waited
  out.

Those expectations and `seed.sh`'s numbers are one fact. Change them together,
never one alone.

A third guard runs in the unit suite: `cmd/demo_test.go` parses every
`type {iq …}` and `recall {…}` line out of `basic.exp` and resolves it against
the real cobra tree, so a renamed flag or subcommand fails `go test` before
anybody records an SVG that shows it. A recall resolves as the previous typed line
with its suffix appended, which is what the shell actually runs. That is why both
shapes are literals on one line each, braces on the same line, nothing computed.

An aborted recording must not be able to overwrite the SVG, and asciinema will
not tell you: it exits 0 even when the command it recorded died (`asciinema rec
--command 'exit 3'` returns 0). `basic.exp` therefore touches
`$IQ_DEMO_ROOT/recorded.ok` as its last act, and `demo.mk` renders only if that
sentinel exists.

## The stamp, and what it does not cover

The SVG is a recording of code, and nothing in a build tells you when the code
has moved on, so the recording rots silently while the README keeps showing
output iq no longer prints. `docs/demo.stamp` is the hash of every source the SVG
was recorded from; `make demo-check` re-hashes them and fails, naming the files
that moved, when they no longer agree.

`make demo-record` writes the stamp after the render, never before, since a stamp
written ahead of a failed render would vouch for an SVG that was never made.
**Commit `docs/docs/assets/demo.svg` and `docs/demo.stamp` together, and never
hand-edit the stamp.**

Hashed, everything that paints a frame or fabricates the world in it:

| | |
|---|---|
| `scripts/demo/` | the recorder, its scripts and every knob in `demo.mk` |
| `scripts/seed-mongo.sh` | the four books on screen |
| `cmd/` `explain.go`, `output.go`, `gron.go`, `color.go`, `errrender.go`, `source.go` | the plan, the two writers, every color, and the handle `add` echoes |
| `internal/render`, `internal/jqfmt` | the JSON colorizer and the pretty-printed filter inside the plan |

Excluded from those roots: `*_test.go` and `*.md` (this file included). Neither
can reach the screen, and hashing them would fire the gate on commits that cannot
change the recording, a gate that cries wolf is one people learn to satisfy
blindly.

Deliberately **not** hashed: `drivers/mongo`, `internal/pushdown`,
`internal/query`, `internal/selector`. They decide *which* rows come back rather
than how one is drawn, and hashing them would fire this gate on most commits in
the repo. The assertions above are the compensating control, and they are the
stronger check: they fail when the demo would start telling a lie, not when a
neighbouring file moved. The cost is honest, a pushdown change that leaves the
result set and the plan's wording intact will not be caught by anything here.

Nor is the toolchain hashed (a new termsvg or a different theme both move a
frame). `make demo-record`, or `FORCE=1 make demo`, is the escape hatch for
exactly that: record even though the sources say there is nothing to record.

Checking the stamp needs **git and a hash, nothing else**, which is why it can run
in `make check` and in CI, where expect, asciinema and termsvg are all absent.

## Driving a CLI with expect, without recording a lie

expect only reads the session **while an `expect` command is running**, and what
it has not read is not in the recording. Nothing it does not write reaches the
recording either. The rules below follow, and every one was learned by getting it
wrong:

- **Type one character per `send`, each followed by a drain.** `send -h` on the
  whole line is the obvious spelling and wrong twice over: nothing reads the echo
  while it types, so the line lands in the recording as one instant paste after a
  dead pause, and `send_human`'s delays sit *between* the characters of one
  `send`, so feeding it one character at a time types at no pace at all. The
  sleep in `keystrokes` is the pace; the `-timeout 0` drain after it is what puts
  the echoed character into the recording.
- **Page a screen that is taller than the terminal, do not scroll it.** The plan is
  36 lines into a 26-row terminal, and pacing it into the recording line by line was
  the first answer and the wrong one: ~35 full-canvas frames the render cannot diff away, and
  a reader chasing a moving screen. `| less -R` is what a person does with output that
  does not fit, and it is two redraws, `await` on each. `-C` goes with it, since iq
  turns its color writer off when stdout is not a terminal and this stdout is a pipe;
  `less -R` then passes the escapes through. `less` is a prerequisite the way `bash`
  is, every Linux box has it, so `demo-record` does not check for it.
- **Assert what the second page *paints*, not what it shows.** `less` scrolls rather
  than repainting: page two here is one screen from the end, so it stops with the last
  line at the bottom, and the only bytes it writes are the eleven rows that close the
  compiled filter and its own `(END)`. `pushed` is on that screen and is not in that
  write, it was painted on page one, so an `await` for it would be answered by the
  buffer and assert nothing. `forget` before the Space, then two tokens the page
  really writes (`object`, then `END`), is the whole of the rule.
- **`-v` is on stderr, and the pipe does not carry it.** Its resolved source line goes
  to the terminal, where `less` already has the alternate screen up, so it lands at the
  top of that screen and the plan painting under it scrolls it off. Nothing else moves,
  and after `q` the alternate screen takes it away with everything else the pager drew.
- **Pause with `hold`, not `sleep`.** A bare `sleep` leaves output sitting unread
  in the pty, and the recording captures a half-drawn screen. `beat` (the pause
  before Enter) is the one place a bare `sleep` is safe: `await` has just emptied
  the pty and the screen is sitting still.
- **`await` space-free tokens, and the last thing a screen prints.** Two reasons,
  and the second is the one that bites. The color writer wraps each JSON token in
  its own escape sequence, so a pattern that straddles two tokens never matches,
  and an `expect` that quietly waits out its 20s timeout bakes a dead pause into
  the SVG; `await` aborts loudly instead, and termsvg's `--max-idle` caps
  whatever slips through. And expect substitutes `$what` into `await`'s pattern
  block and only then splits the result into a pattern list, so the value has to
  survive as one Tcl list element: a space in it becomes two patterns, and an
  unbalanced brace (`{title, price}'`, the natural spelling of the recalled line's
  tail) makes the split fail outright, at which point expect matches the whole
  block as one literal pattern, the `timeout` arm is gone with it, and the
  recording hangs rather than aborting. `recall` waits for `price}'` instead.
- **The `$ ` prompt cannot be awaited, and a bare `$` is a trap.** Its space makes it
  two patterns by the rule above, and expect reads a lone `$` as an end-of-buffer
  anchor rather than as a dollar sign: measured with `exp_internal 1`, it matches the
  **empty** buffer, so the `await` returns before the shell has printed anything and
  asserts nothing, and once a prompt is in the buffer it stops matching at all. The
  prompt is asserted as `2004h` instead, the tail of the bracketed-paste escape
  readline writes immediately before drawing it: one token, no space, and printed
  exactly when the shell is ready for the next line. It is what proves `q` got out of
  the pager, too.
- **`recall` is `type`'s other half, not a copy of it.** Both put characters on
  the command line through `keystrokes`, one per `send` and each drained; `recall`
  only reaches the line differently, by sending the up arrow and waiting for
  readline to redraw the previous command before it appends anything. bash `-i`
  under a pty keeps its history in memory, so no `HISTFILE` and no shell option is
  needed.
- **`forget` before asserting what a command printed.** expect's buffer is
  cumulative and the demo's own typing is echoed into it, so `await "books"`
  right after typing a URI with `?collection=books` in it would be answered by the
  echo and assert nothing at all. Two ordered tokens (`added`, then `books`) is
  the other half of that fix: the first cannot be answered by the echo, and the
  second is then matched in what follows it.

**Measure, do not guess.** Whether a string is emitted in one piece is a fact
about the recorded byte stream, so read it off a real cast rather than reasoning
about it:

```sh
# How many times does iq emit each of these contiguously? 0 means an `await` for it
# would silently time out and bake a dead pause into the SVG.
python3 -c 'import json,sys
d="".join(json.loads(l)[2] for l in open(sys.argv[1]) if l.startswith("[") and json.loads(l)[1]=="o")
[print(f"{d.count(t):4d}  {t!r}") for t in sys.argv[2:]]' \
  /tmp/iq-demo/demo.cast added books Kleppmann Philosophy calls: object END Robert 2008
```

## Geometry, and where the numbers come from

`DEMO_COLS`/`DEMO_ROWS` (80x26) size asciinema's pty (as `--window-size 80x26`),
expect's spawn, and, via the cast header, termsvg's canvas. One source of truth on
purpose: rendering a cast at a size it was not recorded at paints a torn screen
over stale content.

**Both numbers are the four data screens, and neither is the plan.** The plan is 36
lines of one screen; a canvas built to hold it whole shrinks the type on every other
screen and renders a tall thin band nobody can read at README width. So the plan is
paged and the canvas is sized to the rest:

- **80 columns** clears the widest typed command line, the recalled `$ iq '.[] |
  select(.year > 2015) | {title, price}' --explain -v -C | less -R` at 76 characters,
  and the widest output line, a gron assignment at 53. Nothing on screen wraps.
- **26 rows** is the gron screen, the tallest of the four: 18 lines of output with
  its typed line above and the next prompt below, plus six rows of the screen before
  it.

26 is also where the pager's page break falls well, which is luck rather than design:
36 lines through a 25-line window stops page one in the middle of the compiled filter,
and `less` then backs the last page up far enough to fill the screen, which opens page
two on line 12, the `mongo calls:` header. Move either number and that break moves.

The render is vector, so no pixel width caps these numbers: the README and the
docs embed the SVG with `width="100%"` and the browser scales the text to the
column, crisp at any width. The type size a reader sees is therefore the column
width divided by `DEMO_COLS`, so fewer columns is bigger type everywhere, at the
price of wrapping the explain screen (its widest line, the `find(<filter>)`
annotation, is 78 characters, and 62 columns was tried: readable, but the plan
reads better whole).

`DEMO_THEME` is one of termsvg's built-in themes. It is `dracula`, so iq's own colors, the plan's route markers and the JSON colorizer, sit
on a dark, saturated background.

## The sandbox

[`seed.sh`](seed.sh) builds a hermetic world under `$DEMO_ROOT` (`/tmp/iq-demo`)
so a recording never touches your real `$HOME`, your real config, or your keyring:

| | |
|---|---|
| the data | `scripts/seed-mongo.sh` against the `mongo` compose service, the only container the demo needs |
| the binary | a copy of the built `iq` under `$DEMO_ROOT/bin`, so the shell resolves `iq` without a path into anyone's checkout |
| the config | `IQ_CONFIG=$DEMO_ROOT/config/iq.toml`, a path that does not exist yet, so the recording's own `iq add` starts from nothing |
| the rest | `HOME`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` all inside `$DEMO_ROOT` |
| `env.sh` | the exports that point the recording's shell at all of the above, `PS1` included, so no path is on screen |

The demo root holds a config and a cache, so `seed.sh` refuses a `$DEMO_ROOT`
that is a symlink or that someone else owns, and creates it `0700` itself, it is
a predictable path and `rm -rf` follows what it is given.

Drive it by hand with:

```sh
docker compose up -d --wait mongo
scripts/demo/seed.sh
. /tmp/iq-demo/env.sh && bash --norc --noprofile -i
```

## Adding a recording

A second example is a new `.exp` file, not a refactor: source the same
`$DEMO_ROOT/env.sh`, spawn a shell, type the commands, and give it its own `termsvg`
invocation. Reuse `type`/`recall`/`await`/`hold`/`beat`/`forget` rather than reaching
for `sleep`, and add its command lines in the same `type {iq …}` / `recall {…}` shapes
so `cmd/demo_test.go` keeps resolving them.

End on the output you want the reader to see: the last frame is the one the render
holds longest and the one GitHub shows before the SVG loops, so quitting the shell at
the end would leave the recording sitting on an empty terminal.

Keep the recorded world seeded and local. **A recording must never show a real
database, a real credential or a real home path**, which is the whole point of
`seed.sh`.
