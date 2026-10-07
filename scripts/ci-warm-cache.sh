#!/usr/bin/env bash
# Build the Go cache that the CI jobs restore. The go-cache job of
# .github/workflows/ci.yml runs this script. A pull request that changes this
# script or the shared action starts that job, so the pull request tests the
# build steps before they reach main.
#
#   bash scripts/ci-warm-cache.sh linux
#   bash scripts/ci-warm-cache.sh other
#
# The mode linux compiles every variant that the Linux jobs build and the tools
# that more than one job runs. It runs none of them: a tool can call the network
# when it starts, and the host list of the go-cache job blocks that. The mode
# other (macOS and Windows) runs the build without cgo and the plain test
# compile, as the test job does on those systems.
#
# The cache key hashes this script. Go's build cache is content-addressed, so if
# the build flags of a job stop matching these commands, that job misses the
# cache and compiles. It gets no wrong result.
#
# RUNNER_TEMP holds the test binaries. A local run uses a temporary directory.
set -euo pipefail

mode=${1:-}
case "$mode" in
  linux | other) ;;
  *)
    echo "usage: bash scripts/ci-warm-cache.sh linux|other" >&2
    exit 2
    ;;
esac

cd "$(git rev-parse --show-toplevel)"

if [ -n "${RUNNER_TEMP:-}" ]; then
  out=$RUNNER_TEMP
else
  out=$(mktemp -d)
  trap 'rm -rf "$out"' EXIT
fi

if [ "$mode" = other ]; then
  export CGO_ENABLED=0
  go mod download
  go build -trimpath ./...
  go test -c -o "$out/plain/" ./...
  exit 0
fi

go mod download
go build ./...
CGO_ENABLED=0 go build -trimpath ./...

# go test -c -o <dir>/ fails when two packages have the same last path element.
# No package has one now. The three variants match the test job (-race), the
# coverage groups (-covermode and -coverpkg) and a plain run.
go test -c -o "$out/plain/" ./...
go test -race -c -o "$out/race/" ./...
go test -covermode=atomic -coverpkg=./... -c -o "$out/cover/" ./...

# A crasher in the seed corpus fails this script. Then the job saves nothing, and
# the fuzz job of the next pull request reports the same crasher.
IQ_FUZZ_TIME=1x bash scripts/fuzz.sh

# go install compiles each tool and does not run it. A version command can call
# the network. For example govulncheck -version contacts vuln.go.dev, and the
# host list of the go-cache job blocks that host. A later go run takes the
# compiled packages from the cache and only links the tool again.
# shellcheck source=/dev/null
. scripts/tool-versions.env
export GOBIN="$out/bin"
go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$GOLANGCI_LINT_VERSION"
go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION"
go install "golang.org/x/tools/cmd/deadcode@$DEADCODE_VERSION"
go install "github.com/rhysd/actionlint/cmd/actionlint@$ACTIONLINT_VERSION"
