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

Usage: scripts/demo/postrender.py docs/docs/assets/demo.svg
"""

import base64
import html
import re
import sys
from pathlib import Path

FAMILY = "iq-demo-mono"
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

    svg_path.write_text(svg, encoding="utf-8")
    print(f"postrender: embedded {FONT.name} ({FONT.stat().st_size} bytes) into {svg_path}, cursor blink removed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
