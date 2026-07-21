#!/bin/sh
# iq installer.
#
# Downloads the latest (or $IQ_VERSION) release for your OS/arch from GitHub
# Releases, verifies its SHA-256 against the release checksums, and installs the
# binary. POSIX sh; needs curl-or-wget, tar, and sha256sum-or-shasum.
#
#   curl -fsSL https://raw.githubusercontent.com/zsltg/iq/main/install.sh | sh
#   IQ_VERSION=v1.2.3 IQ_INSTALL_DIR="$HOME/.local/bin" sh install.sh
#
# Windows: use Scoop (see the README) — this script targets Linux and macOS.
set -eu

REPO="zsltg/iq"
BINARY="iq"
: "${IQ_VERSION:=latest}"
: "${IQ_INSTALL_DIR:=}"

info() { printf 'iq-install: %s\n' "$*" >&2; }
err() {
	printf 'iq-install: error: %s\n' "$*" >&2
	exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

# HTTP to stdout / to a file, curl preferred, wget fallback, TLS enforced.
fetch() {
	if have curl; then
		curl -fsSL --proto '=https' --tlsv1.2 "$1"
	elif have wget; then
		wget -qO- "$1"
	else
		err "need curl or wget"
	fi
}
download() { # url dest
	if have curl; then
		curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
	elif have wget; then
		wget -qO "$2" "$1"
	else
		err "need curl or wget"
	fi
}

# Detect OS and architecture, mapped onto the release archive naming.
os=$(uname -s)
case "$os" in
linux | Linux) os=linux ;;
darwin | Darwin) os=darwin ;;
*) err "unsupported OS: $os (Windows: install with Scoop — see the README)" ;;
esac

arch=$(uname -m)
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) err "unsupported architecture: $arch" ;;
esac

# Resolve the version (a git tag like v1.2.3). "latest" asks the GitHub API.
tag=$IQ_VERSION
if [ "$tag" = latest ]; then
	info "resolving latest release…"
	tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" |
		grep '"tag_name"' | head -1 |
		sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/')
	[ -n "$tag" ] || err "could not resolve the latest release (is $REPO public and released?)"
fi
ver=${tag#v} # goreleaser omits the leading v in artifact names

archive="${BINARY}_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t iq)
trap 'rm -rf "$tmp"' EXIT INT TERM

info "downloading $archive ($tag)…"
download "$base/$archive" "$tmp/$archive"
download "$base/checksums.txt" "$tmp/checksums.txt"

# Verify SHA-256 before touching the filesystem beyond the temp dir.
info "verifying checksum…"
if have sha256sum; then
	got=$(sha256sum "$tmp/$archive" | awk '{print $1}')
elif have shasum; then
	got=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else
	err "need sha256sum or shasum to verify the download"
fi
want=$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')
[ -n "$want" ] || err "no checksum entry for $archive"
[ "$got" = "$want" ] || err "checksum mismatch for $archive (got $got, want $want)"

tar -xzf "$tmp/$archive" -C "$tmp" "$BINARY" || err "failed to extract $BINARY from $archive"
chmod +x "$tmp/$BINARY"

# Pick an install dir: the override, else /usr/local/bin if writable, else ~/.local/bin.
dir=$IQ_INSTALL_DIR
if [ -z "$dir" ]; then
	if [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir="$HOME/.local/bin"
	fi
fi
mkdir -p "$dir" || err "cannot create install dir: $dir"
mv "$tmp/$BINARY" "$dir/$BINARY" ||
	err "cannot write to $dir — set IQ_INSTALL_DIR to a writable path"
info "installed $BINARY $tag -> $dir/$BINARY"

case ":$PATH:" in
*":$dir:"*) ;;
*) info "note: $dir is not on your PATH — add it, e.g.  export PATH=\"$dir:\$PATH\"" ;;
esac

"$dir/$BINARY" version 2>/dev/null || true
