#!/usr/bin/env bash
# Linux core builder. Desktop payloads join this staging manifest before publication.
set -euo pipefail
version=${1:?Usage: build-release.sh vMAJOR.MINOR.PATCH [linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|--assemble]}
target=${2:-all}
case "$target" in
  all|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|--assemble) ;;
  *) echo "Unsupported core target: $target" >&2; exit 1 ;;
esac
python3 scripts/release.py validate "$version"
revision=$(git rev-parse HEAD)
# Never mix leftovers from another tag into an upload.
if [[ "$target" != --assemble ]]; then
  [[ ! -e dist ]] || { echo 'dist already exists; use a clean checkout' >&2; exit 1; }
  mkdir dist
  targets=("$target")
  if [[ "$target" == all ]]; then targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64); fi
  for platform in "${targets[@]}"; do
    for binary in easyagent easyagent-bridge; do
      # Windows executables need the .exe suffix for the OS and exec.LookPath.
      suffix=""; [[ ${platform%/*} == windows ]] && suffix=".exe"
      CGO_ENABLED=0 GOOS=${platform%/*} GOARCH=${platform#*/} go build -trimpath \
        -ldflags "-s -w -X main.version=$version" \
        -o "dist/$binary-${platform%/*}-${platform#*/}$suffix" "./cmd/$binary"
    done
  done
fi
# Generated output is ignored; source changes during bundle generation are not.
[[ -z $(git status --porcelain --untracked-files=normal) ]] \
  || { echo 'Source changed during release build; publication refused' >&2; exit 1; }
for artifact in dist/easyagent-{linux,darwin,windows}-* dist/easyagent-bridge-*; do
  [[ -f "$artifact" ]] || continue
  metadata=$(go version -m "$artifact")
  [[ "$metadata" == *"vcs.revision=$revision"* && "$metadata" == *"vcs.modified=false"* ]] \
    || { echo "Invalid source metadata: $artifact" >&2; exit 1; }
done
# Both binaries must expose the version without starting a service or reading credentials.
if [[ "$target" == all || "$target" == linux/amd64 || "$target" == --assemble ]]; then
[[ $(dist/easyagent-linux-amd64 --version) == "easyagent $version" ]]
[[ $(dist/easyagent-bridge-linux-amd64 --version) == "easyagent-bridge $version" ]]
fi
# Server .deb packages wrap the two Linux binaries for Debian/Ubuntu hosts.
for arch in amd64 arm64; do
  [[ "$target" == all || "$target" == "linux/$arch" ]] || continue
  CORE_BIN="dist/easyagent-linux-$arch" BRIDGE_BIN="dist/easyagent-bridge-linux-$arch" \
    bash scripts/build-server-deb.sh "${version#v}" "$arch" dist
done
if [[ "$target" == all || "$target" == --assemble ]]; then
python3 scripts/release.py manifest "$version" "$revision" dist --core-only
(cd dist && sha256sum --check checksums.txt)
fi
