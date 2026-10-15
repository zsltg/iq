#!/usr/bin/env python3
"""Finish the rendered SVG in place: embed the demo font, add the viewBox, still the cursor.

termsvg lays every text run and every cursor on a fixed grid (its 20px font at
0.6em, 12px per cell) but names no font of its own: a hardcoded
`Monaco,Consolas,'Courier New',monospace` stack that the reader's browser resolves
however its fontconfig sees fit. Resolve it to a font with a different advance
(this box maps Monaco to the proportional Noto Sans) and the drawn text falls short
of the grid, so the cursor drifts away from the last character as a line is typed.
Embedding a subset of Source Code Pro (0.6em, the grid's own advance) pins the
glyphs to the grid in every browser, at ~5 KB.

The subset covers the glyphs listed in demo-font.chars, and this script refuses a
recording that types anything outside it: a missing glyph would fall back to the
browser font silently and drift again. Regenerate the subset with `make demo-font`
after widening the list.

It also adds a viewBox, which termsvg omits: Chrome, Firefox and some forges scale a
viewBox-less SVG to `width="100%"` anyway, WebKit treats it as fixed-size, and the
README and the docs both embed it at the column's width.

And it stills the cursor: termsvg blinks it at 1 Hz with an opacity animation on
every frame's cursor rect (208 of them), a distraction the reader does not need
while the cursor is already moving with the typing, so the keyframes and the rule
go and the cursor stays solid.

Last, it freezes the demo for a reader who asks for less motion. The animation loops
for over half a minute, and an <img> gives the reader no pause button. Under
`prefers-reduced-motion: reduce` the frames stop on one still frame: the first frame
after the second query's output that ends in a bare prompt. That frame shows the
source, the read by key, and the filtered scan. The frame is found by its text, not
by its index, so a new recording still freezes on the same moment. A recording with
no such frame fails the render. Browsers apply an operating-system or browser-wide
reduced-motion setting to an SVG inside <img>. Playwright's per-page `reducedMotion`
emulation does not reach it, so a test needs the browser-wide setting.

Usage: scripts/demo/postrender.py docs/docs/assets/demo.svg
"""

import base64
import html
import re
import sys
from pathlib import Path

FAMILY = "iq-demo-mono"
# A query line, as opposed to `$ iq add`: the freeze frame is the first frame that
# shows two of them and ends in a bare prompt.
QUERY_PROMPT = "$ iq '"
FREEZE_QUERIES = 2
HERE = Path(__file__).resolve().parent
FONT = HERE / "demo-font.woff2"
CHARS = HERE / "demo-font.chars"


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: postrender.py <svg>", file=sys.stderr)
        return 2
    svg_path = Path(sys.argv[1])
    svg = svg_path.read_text(encoding="utf-8")
    if FAMILY in svg:
        print(f"postrender: {svg_path} already embeds {FAMILY}", file=sys.stderr)
        return 1

    covered = set(CHARS.read_text(encoding="utf-8").rstrip("\n"))
    typed = set()
    for m in re.finditer(r"<text[^>]*>([^<]*)</text>", svg):
        typed.update(html.unescape(m.group(1)))
    missing = sorted(typed - covered)
    if missing:
        shown = " ".join(f"U+{ord(c):04X} {c!r}" for c in missing)
        print(
            f"postrender: the recording uses glyphs the font subset lacks: {shown}\n"
            "  add them to scripts/demo/font.sh's glyph list, run `make demo-font`, "
            "and record again",
            file=sys.stderr,
        )
        return 1

    stack = re.search(r"font-family:([^;}]+)", svg)
    if not stack:
        print("postrender: no font-family in the SVG, is this a termsvg render?", file=sys.stderr)
        return 1
    svg = svg.replace(stack.group(0), f"font-family:'{FAMILY}',monospace", 1)

    woff2 = base64.b64encode(FONT.read_bytes()).decode("ascii")
    face = f"@font-face{{font-family:'{FAMILY}';src:url(data:font/woff2;base64,{woff2}) format('woff2')}}"
    if "<style>" not in svg:
        print("postrender: no <style> element in the SVG", file=sys.stderr)
        return 1
    svg = svg.replace("<style>", "<style>" + face, 1)

    root = re.match(r'<svg\b[^>]*\bwidth="([0-9.e]+)"[^>]*\bheight="([0-9.e]+)"', svg)
    if not root:
        print("postrender: root <svg> lacks width/height", file=sys.stderr)
        return 1
    if "viewBox=" not in root.group(0):
        w, h = (int(float(v)) for v in root.groups())
        svg = svg.replace("<svg ", f'<svg viewBox="0 0 {w} {h}" ', 1)

    blink = re.search(r"@keyframes blink\{[^}]*\}[^}]*\}\}?", svg)
    if blink:
        svg = svg.replace(blink.group(0), "", 1)
    svg, n = re.subn(r";?animation:blink[^;}]*", "", svg, count=1)
    if not blink or not n:
        print("postrender: no cursor blink to remove, has termsvg changed its output?", file=sys.stderr)
        return 1

    return freeze_and_write(svg_path, svg)


def freeze_and_write(svg_path: Path, svg: str) -> int:
    """Add the reduced-motion freeze, then write the finished SVG. It returns the exit code."""
    svg, err = freeze_for_reduced_motion(svg)
    if err:
        print(f"postrender: {err}", file=sys.stderr)
        return 1
    svg_path.write_text(svg, encoding="utf-8")
    print(f"postrender: embedded {FONT.name} ({FONT.stat().st_size} bytes) into {svg_path}, cursor blink removed, reduced-motion freeze added")
    return 0


def frame_lines(body: str) -> list[str]:
    """Return the text rows of one frame, top to bottom, one string per row."""
    rows: dict[float, list[str]] = {}
    for y, text in re.findall(r'<text[^>]*\by="([0-9.]+)"[^>]*>(.*?)</text>', body):
        rows.setdefault(float(y), []).append(html.unescape(re.sub(r"<[^>]+>", "", text)))
    return ["".join(parts).rstrip() for _, parts in sorted(rows.items())]


def is_freeze_frame(lines: list[str]) -> bool:
    """Report whether a frame shows FREEZE_QUERIES queries and ends in a bare prompt."""
    queries = sum(line.startswith(QUERY_PROMPT) for line in lines)
    ends_in_prompt = bool(lines) and lines[-1] == "$"
    return queries == FREEZE_QUERIES and ends_in_prompt


def freeze_delay(steps: list[tuple[float, int]], frames: dict[int, list[str]], duration: float) -> float | None:
    """Return the time in seconds at the middle of the first freeze frame, or None.

    steps lists the keyframe stops in order, as (percent, frame offset). frames maps a
    frame offset to the text rows of that frame.
    """
    for (pct, offset), (next_pct, _) in zip(steps, steps[1:]):
        lines = [line for line in frames.get(offset, []) if line]
        if is_freeze_frame(lines):
            return (pct + next_pct) / 2 / 100 * duration
    return None


def freeze_for_reduced_motion(svg: str) -> tuple[str, str]:
    """Add a rule that holds the freeze frame under prefers-reduced-motion: reduce.

    The frames lie side by side, and the keyframes step a translateX over them. A
    negative animation-delay picks the middle of the freeze frame's step, and a paused
    play state holds it. Both need !important, because the animation is an inline
    style on the frames group. It returns the new SVG, or an error message.
    """
    anim = re.search(r'style="animation:([\w-]+) ([0-9.]+)s ', svg)
    if not anim:
        return svg, "no frame animation found, has termsvg changed its output?"
    name, duration = anim.group(1), float(anim.group(2))
    keyframes = re.search(r"@keyframes " + re.escape(name) + r"\{(.*?)\}\}", svg, re.S)
    if not keyframes:
        return svg, f"no @keyframes {name} found"
    steps = [
        (float(pct), -int(x))
        for pct, x in re.findall(r"([0-9.]+)%\{transform:translateX\((-?\d+)px\)", keyframes.group(1))
    ]
    frames = {
        int(x): frame_lines(body)
        for x, body in re.findall(r'<g transform="translate\((\d+),0\)">(.*?)</g>', svg)
    }
    delay = freeze_delay(steps, frames, duration)
    if delay is None:
        return svg, (
            f"no frame shows {FREEZE_QUERIES} queries and ends in a bare prompt, so the "
            "reduced-motion freeze has no frame to hold. Has scripts/demo/basic.exp changed?"
        )
    rule = (
        "@media (prefers-reduced-motion: reduce){"
        f'g[style*="animation:{name} "]'
        "{animation-play-state:paused!important;"
        f"animation-delay:-{delay:.3f}s!important}}}}"
    )
    return svg.replace("</style>", rule + "</style>", 1), ""

if __name__ == "__main__":
    sys.exit(main())
