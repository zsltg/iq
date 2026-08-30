# The demo recorder, its knobs, and the gate that keeps docs/docs/assets/demo.svg
# honest. Included by the root Makefile, and kept here with the scripts it drives:
# every value below changes the recorded frames, and scripts/demo/stamp.sh hashes this
# whole directory, so a knob moved here IS a re-record, and an unrelated edit to the
# root Makefile is not. See scripts/demo/README.md.
#
# expect types the commands, asciinema records the session, termsvg renders the cast
# to an animated SVG. All three are dev tools, never shipped, so none of them enter
# go.mod: expect and asciinema are expected on PATH, termsvg is a Go program that
# demo-record provisions itself, pinned, into a throwaway GOBIN (`go install
# pkg@version` reads and writes no go.mod), the way scripts/mutation-gate.sh does with
# mutago.
#
# Hold the recorder at 3 or newer: 2.x is the abandoned Python line, does not know
# --window-size, and has no `convert`. asciinema 3 records asciicast v3, and termsvg
# reads v2, so the recipe converts the cast between the two with `asciinema convert`.
ASCIINEMA_MAJOR := 3
TERMSVG_PKG     := github.com/mrmarble/termsvg/cmd/termsvg
TERMSVG_VERSION := v0.11.0

# Where the recording's sandbox lives. Short and neutral, so the paths it exports are
# not the recorder's home directory; seed.sh refuses to write here if it is a symlink
# or not owned by you, and rm -rf's it.
DEMO_ROOT       ?= /tmp/iq-demo

# The recorded terminal's geometry, one source of truth: it sizes asciinema's pty,
# expect's spawn, and (via the cast header) termsvg's canvas. Both numbers are the
# four data screens, and neither is the plan: the plan is 36 lines of one screen, and
# sizing the canvas to it shrinks the type on every other screen and makes the demo a
# tall thin band. It is paged instead, see basic.exp's step 4. So 80 columns is the
# widest typed command line (`$ iq '.[] | select(.year > 2015) | {title, price}'
# --explain -v -C | less -R`, 76 characters) with room to spare, and the widest output
# line is a gron assignment at 53, so nothing on screen wraps; 26 rows is the gron
# screen, 18 lines of output with its typed line above and the next prompt below, plus
# six rows of the screen before it.
#
# 26 is also what lands the pager's page break on a section header, which is luck
# worth keeping: 36 lines through a 25-line window means page one stops in the middle
# of the compiled filter, and less then backs the last page up far enough to fill the
# screen, which opens page two on line 12, `mongo calls:`. Move these numbers and that
# break moves with them.
#
# The render is vector, so no pixel width caps these numbers: the README and the docs
# embed the SVG at 100% of their column and the browser scales the text, crisp at any
# width. The type size a reader sees is the column width divided by DEMO_COLS, so
# fewer columns is bigger type, at the price of wrapping the explain screen, whose
# widest line is 78 characters.
DEMO_COLS       ?= 80
DEMO_ROWS       ?= 26

# One of termsvg's built-in themes. dracula, so the plan's route markers and the JSON
# colorizer sit on a dark, saturated background rather than a black one.
DEMO_THEME      ?= dracula

# The font is embedded, not named. termsvg lays every text run and cursor on a 12px
# grid (its 20px font at 0.6em) but only names a font stack (Monaco, Consolas, Courier
# New, monospace; hardcoded, not a flag), and the reader's browser resolves that
# however its fontconfig sees fit: on this box Monaco maps to the proportional Noto
# Sans, and Firefox then drew text that fell short of the grid, the cursor drifting
# away from the last character as a line was typed. So postrender.py swaps the stack
# for a ~3.5 KB WOFF2 subset of Source Code Pro (0.6em, the grid's own advance),
# committed as demo-font.woff2 with the glyphs it covers in demo-font.chars. That list
# is the contract: the embed refuses a recording that types a glyph outside it, since
# the fallback would be the browser font and the drift again. `make demo-font`
# regenerates the subset from the pinned upstream release (font.sh), only when the
# glyph list or the font version changes; it is not part of demo-record. Source Code
# Pro is OFL-1.1 (demo-font.LICENSE): a subset may be embedded, but not under the
# reserved name, hence the internal family name inside the SVG.

# The artifact and its provenance. The SVG sits under docs/docs/, which Zensical
# copies verbatim, so the site and the README serve the same file; the stamp sits
# under docs/ but outside docs_dir, so it is never published as a page.
DEMO_SVG        := docs/docs/assets/demo.svg

.PHONY: demo demo-record demo-check demo-font

# demo-font regenerates the committed font subset from the pinned upstream release.
# Needs the network and uvx (fonttools is pulled into a throwaway environment, the
# docs toolchain already depends on uv). Run it after widening the glyph list or
# bumping the font version in font.sh, then `make demo-record`.
demo-font:
	@bash scripts/demo/font.sh

# demo re-records only when the sources the SVG shows have moved. FORCE=1 records
# anyway, and so does `make demo-record`.
demo:
	@if [ -z "$(FORCE)" ] && bash scripts/demo/stamp.sh check >/dev/null 2>&1; then \
		echo "demo: $(DEMO_SVG) still matches the code it shows, nothing to record."; \
		echo "      (make demo-record, or FORCE=1 make demo, records it anyway.)"; \
	else \
		$(MAKE) demo-record; \
	fi

# demo-check fails when the SVG shows code that has since changed. Needs git and
# sha256sum only, no recorder, so it runs in `make check` and in CI.
demo-check:
	@bash scripts/demo/stamp.sh check

# demo-record records unconditionally and re-stamps. Split from `demo` because make
# runs each recipe line in its own shell: a guard on the first line cannot skip the
# rest. It is also the escape hatch, for when the sources are unchanged but the render
# is not (a termsvg upgrade, a new theme, a regenerated font subset).
demo-record: build
	@command -v expect >/dev/null    || { echo "make demo: expect is not on PATH (apt install expect)"; exit 1; }
	@command -v python3 >/dev/null   || { echo "make demo: python3 is not on PATH, postrender.py needs it"; exit 1; }
	@command -v asciinema >/dev/null || { echo "make demo: asciinema is not on PATH, the recorder:"; echo "  cargo install --locked asciinema   (or a distro package, or a static binary from https://github.com/asciinema/asciinema/releases)"; exit 1; }
	@asciinema --version | awk '{ split($$2, v, "."); exit !(v[1] >= $(ASCIINEMA_MAJOR)) }' \
		|| { echo "make demo: $$(asciinema --version) is too old, 2.x is the abandoned Python line (pipx/PyPI), which cannot record asciicast v3, does not know --window-size and has no convert. Install $(ASCIINEMA_MAJOR).x or newer:"; echo "  cargo install --locked asciinema"; exit 1; }
# One container, and only the one the demo queries: the stack is flaky under
# contention, and the recording needs a document store, not the whole compose file.
	docker compose up -d --wait mongo
# seed.sh asserts the data the recording narrates (four books, two after 2015, two
# authors matching /Martin/) and fails here if scripts/seed-mongo.sh no longer writes
# it, before a frame is recorded rather than after a plausible lie is.
	DEMO_ROOT=$(DEMO_ROOT) scripts/demo/seed.sh
# --window-size sizes the pty the shell and iq actually draw into. Without it
# asciinema falls back to 80x24 (there is no tty when this runs from a script), two
# rows short of the gron screen and unrelated to the geometry above, and every later
# stage inherits it. termsvg then takes the geometry from the cast header, never pass
# it a size the recording was not made at.
	. $(DEMO_ROOT)/env.sh && DEMO_COLS=$(DEMO_COLS) DEMO_ROWS=$(DEMO_ROWS) \
		asciinema rec --overwrite --quiet \
		--window-size $(DEMO_COLS)x$(DEMO_ROWS) \
		--command "expect -f scripts/demo/basic.exp" $(DEMO_ROOT)/demo.cast
# asciinema exits 0 even when the command it recorded died (verified: `asciinema rec
# --command 'exit 3'` returns 0). So a failed assertion in basic.exp would otherwise
# sail through, and termsvg would render the abort message straight over the SVG. The
# script touches this sentinel as its last act: no sentinel, no render.
	@test -f $(DEMO_ROOT)/recorded.ok || { echo "make demo: the recording aborted before the end, the demo asserts what it narrates and something it expected was not printed. Read the tail of $(DEMO_ROOT)/demo.cast."; exit 1; }
# termsvg reads asciicast v2; the recorder writes v3. Same events in the other
# header dialect, and asciinema's own converter is the one that knows both.
	asciinema convert --overwrite -f asciicast-v2 $(DEMO_ROOT)/demo.cast $(DEMO_ROOT)/demo-v2.cast
# The render: one text layer per frame, laid side by side and stepped through with a
# CSS keyframe animation, so it plays anywhere an <img> does, GitHub, other forges and the
# docs site included, with no script and no font to install. The chrome around it is
# termsvg's own: a rounded rect and the three traffic-light dots, with the terminal
# content translated below a 60px header, so the demo reads as a terminal window in
# the README and the docs. --minify takes ~30% off, and
# --max-idle caps any gap with no output. Every deliberate pause in the recording is
# shorter than that cap: the longest is the plan's, the 6 seconds each of its two
# pages is held for. So it changes none of them, it is a backstop so a wait that stops
# matching can never bake a dead pause into the SVG. Raise it with the holds, never
# below the longest one, or the pause the reader needs is the one termsvg cuts. The
# binary is provisioned into a throwaway GOBIN for this one run and removed after it;
# its progress bar goes to a log that is shown only when the render fails.
	@bindir=$$(mktemp -d) && trap 'rm -rf "$$bindir"' EXIT \
		&& GOBIN="$$bindir" go install $(TERMSVG_PKG)@$(TERMSVG_VERSION) \
		&& { "$$bindir"/termsvg export --minify --max-idle 8s --theme $(DEMO_THEME) \
			$(DEMO_ROOT)/demo-v2.cast -o $(DEMO_SVG) >"$$bindir"/termsvg.log 2>&1 \
			|| { tr '\r' '\n' <"$$bindir"/termsvg.log | grep -v '^IR Processing' | tail -20; exit 1; }; } \
		&& echo "make demo: rendered $(DEMO_SVG), $$(stat -c %s $(DEMO_SVG)) bytes"
# Pin the glyphs to the grid (see the font block above), add the viewBox termsvg
# omits, so `width="100%"` scales it in WebKit too, and still the cursor: termsvg
# blinks it at 1 Hz, and a cursor that already moves with the typing needs no blink.
	python3 scripts/demo/postrender.py $(DEMO_SVG)
# Stamped last, and only now: a stamp written before a failed render would vouch for
# an SVG that was never made. Commit the SVG and docs/demo.stamp together.
	@bash scripts/demo/stamp.sh write
