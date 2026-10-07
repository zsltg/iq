#!/usr/bin/env bash
# Checks that `go install github.com/zsltg/iq@<version>` can install the module.
#
#   bash scripts/go-installable.sh [dir]
#
# go install refuses a module at a version when its go.mod has a replace or an
# exclude directive. A build from the checkout applies these directives, so no
# other pull request job finds the problem. Before this check, only the install
# smoke test found it, after the release was published (v0.38.2).
# The check reads go.mod in <dir>, or in the repository root when <dir> is not
# given. scripts/check.sh (make check) and the CI lint job run it.
# To use a fork of a dependency, change the module path in the fork and import
# that path. DEVELOPMENT.md shows the example of github.com/zsltg/rdb.
set -uo pipefail

dir=${1:-$(git rev-parse --show-toplevel)} || exit 1

# go mod edit -json writes the Replace and Exclude keys only when go.mod has
# such directives.
json=$(cd "$dir" && go mod edit -json) || {
  echo "go-installable: cannot read go.mod in $dir" >&2
  exit 1
}
if grep -qE '"(Replace|Exclude)":' <<<"$json"; then
  echo "go-installable: go.mod has a replace or exclude directive, and go install refuses such a module." >&2
  echo "go-installable: remove the directive. To use a fork, change the module path in the fork and import that path." >&2
  exit 1
fi
echo "go-installable: passed"
