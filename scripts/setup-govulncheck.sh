#!/usr/bin/env bash
# golang/vuln publishes source modules, not official binary assets. Restore the
# small binary cache first; bootstrap once on a miss instead of go install in
# every audit job. Do not cache a second copy of its module/build dependencies.
set -euo pipefail
destination=${1:?Usage: setup-govulncheck.sh ABSOLUTE_TOOL_DIRECTORY}
[[ "$destination" = /* ]] || { echo 'Tool directory must be absolute' >&2; exit 1; }
version=v1.9.0
binary="$destination/govulncheck"
if [[ ! -x "$binary" ]]; then
  mkdir -p "$destination"
  temporary=$(mktemp -d)
  trap 'rm -rf "$temporary"' EXIT
  (
    cd "$temporary"
    export GOWORK=off
    go mod init easyagent-ci-tools
    go get "golang.org/x/vuln/cmd/govulncheck@$version"
    go build -trimpath -buildvcs=false -o "$binary" golang.org/x/vuln/cmd/govulncheck
  )
fi
go version -m "$binary" | grep -F "golang.org/x/vuln" | grep -F "$version"
