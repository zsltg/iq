#!/usr/bin/env bash
# Regenerates the font subset that postrender.py embeds into the demo SVG.
#
# termsvg lays the recording out on a 12px grid (20px font, 0.6em advance) and only
# names a font stack; a browser whose fontconfig resolves that stack to a font with
# another advance draws text that drifts off the grid, cursor and all. Embedding a
# subset of Source Code Pro, whose advance is the grid's, pins it in every browser.
#
# Outputs, committed next to this script and hashed by the demo stamp:
#   demo-font.woff2    the subset, ~3.5 KB, glyphs from GLYPHS below
#   demo-font.chars    the covered glyphs on one line, postrender.py's contract
#   demo-font.LICENSE  the upstream OFL-1.1 text with Adobe's notice
#
# Run via `make demo-font` after widening GLYPHS or bumping the release, then record
# again. Needs the network and uvx (fonttools with the woff2 codec, pulled into a
# throwaway environment; the docs toolchain already depends on uv). Both downloads
# are pinned by sha256, so a moved upstream fails here rather than ships.
set -euo pipefail

cd "$(dirname "$0")"

# Pinned upstream: adobe-fonts/source-code-pro, TTF release and the license text.
RELEASE='2.042R-u/1.062R-i/1.026R-vf'
ZIP='TTF-source-code-pro-2.042R-u_1.062R-i.zip'
ZIP_SHA256='0c85bac90d15c040b82939aa92bc8404420fccc02e37bbcb9c93a7f21abb52c6'
TTF_IN_ZIP='TTF/SourceCodePro-Regular.ttf'
LICENSE_URL='https://raw.githubusercontent.com/adobe-fonts/source-code-pro/release/LICENSE.md'
LICENSE_SHA256='7c940e28a5388e9bba866cf0e408edda45fe0899ba98665b8f6ab31dc5e4b8ff'

# What the subset covers: printable ASCII, and the em dash the query plan annotates
# with. Anything the recording types outside this list fails postrender.py.
GLYPHS="$(printf '%b' ' !"#$%&'"'"'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~\xe2\x80\x94')"

for tool in curl unzip sha256sum uvx; do
  command -v "$tool" >/dev/null || { echo "demo-font: $tool is not on PATH" >&2; exit 1; }
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL -o "$work/font.zip" "https://github.com/adobe-fonts/source-code-pro/releases/download/$RELEASE/$ZIP"
echo "$ZIP_SHA256  $work/font.zip" | sha256sum -c --quiet
unzip -q -j "$work/font.zip" "$TTF_IN_ZIP" -d "$work"

curl -fsSL -o "$work/LICENSE.md" "$LICENSE_URL"
echo "$LICENSE_SHA256  $work/LICENSE.md" | sha256sum -c --quiet

printf '%s' "$GLYPHS" > "$work/chars.txt"
# --no-hinting, --desubroutinize and the emptied feature/name tables are what make
# the file this small; the SVG scales the glyphs anyway, so hinting buys nothing.
uvx --quiet --from 'fonttools[woff]' pyftsubset "$work/SourceCodePro-Regular.ttf" \
  --text-file="$work/chars.txt" --flavor=woff2 --no-hinting --desubroutinize \
  --layout-features='' --name-IDs='' --output-file="$work/demo-font.woff2" 2>/dev/null

cp "$work/demo-font.woff2" demo-font.woff2
printf '%s\n' "$GLYPHS" > demo-font.chars
cp "$work/LICENSE.md" demo-font.LICENSE
echo "demo-font: wrote demo-font.woff2 ($(stat -c %s demo-font.woff2) bytes, $(( ${#GLYPHS} )) glyphs), demo-font.chars, demo-font.LICENSE"
