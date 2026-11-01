#!/usr/bin/env bash
# Exercise the actual release builder in a disposable repository. Never pushes a tag.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
target=${1:-all}
artifacts=${2:-}
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
git clone --quiet --no-local "$root" "$fixture/repo"
cd "$fixture/repo"
git config user.name 'Release fixture'
git config user.email 'release-test@example.invalid'
# Pick a deliberately separate local-only version for the fixture.
version=v999.0.0-rc.1
printf '\n## [%s]\n\nDisposable release build verification.\n' "$version" >> CHANGELOG.md
git add CHANGELOG.md
# Matrix jobs must construct the same fixture revision so aggregation can
# verify every binary's source metadata, not just the output filenames.
epoch=$(git show -s --format=%ct HEAD)
GIT_AUTHOR_DATE="$epoch +0000" GIT_COMMITTER_DATE="$epoch +0000" \
  git -c commit.gpgsign=false commit --quiet -m 'test: local release build fixture'
git update-ref refs/remotes/origin/main HEAD
git -c tag.gpgsign=false tag -a "$version" -m 'Local test only; never publish'
if [[ "$target" == --assemble ]]; then
  mkdir dist
  cp "$artifacts/"* dist/
fi
bash scripts/build-release.sh "$version" "$target"
if [[ -n "$artifacts" && "$target" != --assemble ]]; then
  mkdir -p "$artifacts"
  cp dist/* "$artifacts/"
fi
