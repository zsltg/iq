# iq build and release automation. See README "Common commands".
# Release logic lives in scripts/release.sh; this Makefile is a thin wrapper and
# the single source of truth for build-time version embedding.

# Version metadata embedded via ldflags. DATE uses the commit time, not the wall
# clock, so a build from a given commit is reproducible.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/zsltg/iq/cmd.version=$(VERSION) \
           -X github.com/zsltg/iq/cmd.commit=$(COMMIT) \
           -X github.com/zsltg/iq/cmd.date=$(DATE)

.PHONY: build version changelog release tools

# build compiles the binary with version metadata embedded.
build:
	go build -ldflags "$(LDFLAGS)" -o iq .

# version prints the version the next release would take.
version:
	@svu next

# changelog regenerates CHANGELOG.md in place (no bump, commit, or tag),
# including any unreleased commits under the next version.
changelog:
	bash scripts/release.sh --changelog-only

# release bumps the version, regenerates the changelog, commits, and tags.
release:
	bash scripts/release.sh

# tools installs the release toolchain (svu, git-chglog) into GOPATH/bin.
tools:
	go install github.com/caarlos0/svu@latest
	go install github.com/git-chglog/git-chglog/cmd/git-chglog@latest
