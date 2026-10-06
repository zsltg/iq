#!/usr/bin/env bash
# Collect the license and notice texts of every third-party module that the
# iq binary links, plus the Go runtime, into third-party-licenses/. The release
# archives and the Linux packages ship that tree. MIT, BSD, and Apache-2.0
# require these texts in a binary distribution, and Apache-2.0 also requires
# each NOTICE file. This script only collects texts. The license policy is
# scripts/license-allowlist.txt, which osv-scanner enforces. The script needs
# no network beyond what `go list` needs to fill the module cache.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# Keep this list in sync with `builds` in .goreleaser.yaml. A module can reach
# the binary on one target only, so the output is the union of all targets.
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)

out=third-party-licenses
work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null || true; rm -rf "$work"' EXIT

# One line per package: package directory, module path, module directory.
# The module directory already follows any replace directive.
for t in "${targets[@]}"; do
  CGO_ENABLED=0 GOOS="${t%/*}" GOARCH="${t#*/}" go list -deps \
    -f '{{if .Module}}{{if not .Module.Main}}{{.Dir}}|{{.Module.Path}}|{{.Module.Dir}}{{end}}{{end}}' .
done | LC_ALL=C sort -u >"$work/packages.txt"

[[ -s "$work/packages.txt" ]] || { echo "third-party-licenses: go list found no third-party package" >&2; exit 1; }

# A license or notice file starts with one of these words, in any case, with any
# suffix. Source files such as copyright_test.go do not match. Only the first
# group of words names a license text. NOTICE and PATENTS files go with it.
is_license_file() {
  local name="${1,,}"
  [[ "$name" != *.go ]] && [[ "$name" =~ ^(licen[cs]e|copying|unlicense|notice|patents) ]]
}
is_license_text() {
  [[ "${1,,}" =~ ^(licen[cs]e|copying|unlicense) ]]
}

# For each package, look in its directory and in every parent directory up to
# the module root. A package can carry its own license file below the root.
mkdir -p "$work/tree"
declare -A seen_module=()
declare -A has_license=()
while IFS='|' read -r pkgdir modpath moddir; do
  seen_module["$modpath"]=1
  dir="$pkgdir"
  while :; do
    for f in "$dir"/*; do
      [[ -f "$f" ]] || continue
      is_license_file "${f##*/}" || continue
      rel="${f#"$moddir"/}"
      install -m 0644 -D "$f" "$work/tree/$modpath/$rel"
      is_license_text "${f##*/}" && has_license["$modpath"]=1
    done
    [[ "$dir" == "$moddir" ]] && break
    dir="$(dirname "$dir")"
    # Stop if the package directory is not below the module directory.
    [[ "$dir" == "$moddir"* ]] || { echo "third-party-licenses: $pkgdir is outside $moddir" >&2; exit 1; }
  done
done <"$work/packages.txt"

# Every shipped module must carry a license text. A NOTICE or PATENTS file
# alone is not enough.
for modpath in "${!seen_module[@]}"; do
  [[ -n "${has_license[$modpath]:-}" ]] || {
    echo "third-party-licenses: $modpath has no license file" >&2
    exit 1
  }
done

# The Go runtime and standard library are linked into the binary too.
goroot="$(go env GOROOT)"
install -m 0644 -D "$goroot/LICENSE" "$work/tree/go/LICENSE"
[[ -f "$goroot/PATENTS" ]] && install -m 0644 -D "$goroot/PATENTS" "$work/tree/go/PATENTS"

# The archives and the Linux packages keep these modes. Set them here so that
# they do not depend on the umask: every user must be able to read the tree.
find "$work/tree" -type d -exec chmod 0755 {} +

# Replace the old output only now, so a failed run leaves no partial tree.
[[ -e "$out" ]] && { chmod -R u+w "$out"; rm -rf "$out"; }
mv "$work/tree" "$out"

echo "third-party-licenses: ${#seen_module[@]} modules, $(find "$out" -type f | wc -l) files in $out/"
