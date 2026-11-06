#!/usr/bin/env bash
# Build the MyGo native desktop client and stage its installer artifact.
# Usage: scripts/build-native.sh darwin-arm64|linux-amd64|linux-arm64|windows-amd64 [dist-dir] [release-tag]
# darwin-arm64 must run on an Apple Silicon macOS host (codesign, DMG, bundle
# checks). Linux targets are cgo-free cross-compiles; amd64 additionally runs
# the bundled core to prove the version, arm64 relies on the shared prepare
# pipeline and deb metadata checks. windows-amd64 must run on a Windows host:
# the NSIS installer (downloaded on demand by MyGo) is only built there.
set -euo pipefail
TARGET=${1:?Usage: build-native.sh darwin-arm64|linux-amd64|linux-arm64|windows-amd64 [dist-dir] [release-tag]}
DIST=${2:-}
TAG=${3:-}
case "$TARGET" in
  darwin-arm64|linux-amd64|linux-arm64|windows-amd64) ;;
  *) { echo "Unsupported native target: $TARGET (darwin-arm64, linux-amd64, linux-arm64, windows-amd64)" >&2; exit 1; } ;;
esac
OS=${TARGET%%-*}; ARCH=${TARGET##*-}
if [[ "$OS" == darwin ]]; then
  [[ $(uname -s) == Darwin && $(uname -m) == arm64 ]] || { echo 'Native darwin-arm64 packaging requires an Apple Silicon macOS host' >&2; exit 1; }
elif [[ "$OS" == windows ]]; then
  [[ $(uname -s) == MINGW* || $(uname -s) == MSYS* || $(uname -s) == CYGWIN* || $(uname -s) == Windows_NT ]] || { echo 'Native windows-amd64 packaging requires a Windows host (the NSIS installer is built there only)' >&2; exit 1; }
else
  [[ $(uname -s) == Linux ]] || { echo "Native $TARGET packaging requires a Linux host" >&2; exit 1; }
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [[ -n "$DIST" && "$DIST" != /* ]]; then DIST="$ROOT/$DIST"; fi
if [[ -n "$TAG" ]]; then
  cd "$ROOT"
  python3 scripts/release.py validate "$TAG"
  VERSION=$(python3 -c 'import json; print(json.load(open("desktop-native/mygo.json"))["version"])')
  [[ "$VERSION" == "${TAG#v}" ]] || { echo 'Native version does not match release tag' >&2; exit 1; }
  # Windows has no signed update channel yet: its installer ships unsigned,
  # so only the darwin/linux targets need the updater key.
  if [[ "$OS" != windows ]]; then
    [[ -n "${MYGO_UPDATER_PRIVATE_KEY:-}" ]] || { echo "Native releases require MYGO_UPDATER_PRIVATE_KEY" >&2; exit 1; }
  fi
  export EASYAGENT_BUILD_VERSION="$TAG"
fi
cd "$ROOT/desktop-native"
if [[ "$OS" == linux ]]; then
  go run ./tools/build -platform "linux/$ARCH"
  OUT="build.noindex/$TARGET"
  DEB=$(find "$OUT" -maxdepth 1 -name 'easyagent_*_*.deb' | head -1)
  ARCHIVE=$(find "$OUT" -maxdepth 1 -name "easyagent-*-linux-$ARCH.tar.gz" | head -1)
  [[ -n "$DEB" && -s "$DEB" ]] || { echo "MyGo build did not produce a deb in $OUT" >&2; exit 1; }
  [[ -n "$ARCHIVE" && -s "$ARCHIVE" ]] || { echo "MyGo build did not produce a tar.gz in $OUT" >&2; exit 1; }
  dpkg-deb --info "$DEB" >/dev/null || { echo "Invalid deb package: $DEB" >&2; exit 1; }
  # The deb must carry the core beside the UI; local_backend.go depends on it.
  # grep reads the whole listing: pipefail turns grep -q's early exit into a SIGPIPE failure.
  dpkg-deb -c "$DEB" | grep "opt/easyagent/easyagent-core$" > /dev/null || { echo 'Deb is missing /opt/easyagent/easyagent-core' >&2; exit 1; }
  if [[ -n "$TAG" && "$ARCH" == amd64 ]]; then
    [[ "$("$OUT/easyagent-core" --version)" == "easyagent $TAG" ]] \
      || { echo 'Bundled core version does not match the release tag' >&2; exit 1; }
  fi
  if [[ -n "$DIST" ]]; then
    mkdir -p "$DIST"
    VERSION=$(python3 -c 'import json; print(json.load(open("mygo.json"))["version"])')
    DEB_ARCH=$([[ "$ARCH" == amd64 ]] && echo amd64 || echo arm64)
    cp "$DEB" "$DIST/easyagent-native-linux-$ARCH.deb"
    cp "$ARCHIVE" "$DIST/"
    cp "$OUT/install.sh" "$DIST/easyagent-native-install-linux-$ARCH.sh"
    if [[ -n "$TAG" ]]; then
      [[ -s "$OUT/update-linux-$ARCH.json" ]] || { echo 'MyGo build did not sign an update manifest; check MYGO_UPDATER_PRIVATE_KEY' >&2; exit 1; }
      cp "$OUT/update-linux-$ARCH.json" "$DIST/"
      go run ./tools/verify-update mygo.json "$DIST" "$VERSION" "linux-$ARCH"
    fi
    echo "Native artifacts: $DIST/easyagent-native-linux-$ARCH.deb + tar.gz"
  else
    echo "Native deb: $DEB"
    echo "Native tar.gz: $ARCHIVE"
  fi
  exit 0
fi

if [[ "$OS" == windows ]]; then
  go run ./tools/build -platform "windows/$ARCH"
  OUT="build.noindex/$TARGET"
  SETUP=$(find "$OUT" -maxdepth 1 -name '*Setup*.exe' | head -1)
  [[ -n "$SETUP" && -s "$SETUP" ]] || { echo "MyGo build did not produce a Windows installer in $OUT" >&2; exit 1; }
  # The staged installer must carry the core beside the UI, like the deb check
  # above: local_backend.go looks for easyagent-core.exe next to EasyAgent.exe.
  [[ -f "$OUT/easyagent-core.exe" ]] || { echo 'Windows build is missing easyagent-core.exe' >&2; exit 1; }
  if [[ -n "$TAG" ]]; then
    # Running a foreign binary needs the host; windows-amd64 always runs on a
    # Windows host, so the version probe is direct.
    [[ "$("$OUT/easyagent-core.exe" --version)" == "easyagent $TAG" ]] \
      || { echo 'Bundled core version does not match the release tag' >&2; exit 1; }
  fi
  if [[ -n "$DIST" ]]; then
    mkdir -p "$DIST"
    cp "$SETUP" "$DIST/easyagent-native-windows-$ARCH-setup.exe"
    echo "Native artifact: $DIST/easyagent-native-windows-$ARCH-setup.exe"
  else
    echo "Native installer: $SETUP"
  fi
  exit 0
fi

if [[ -n "$TAG" ]]; then
  python3 "$ROOT/scripts/macos-signing.py" -- go run ./tools/build
else
  # Development bundles remain disposable and do not require the release key.
  go run ./tools/build -sign -
fi

APP=$(find build.noindex/darwin-arm64 -maxdepth 1 -name '*.app' | head -1)
[[ -n "$APP" && -d "$APP" ]] || { echo 'MyGo build did not produce an .app bundle' >&2; exit 1; }
# Clean up registration even when validation fails; never launch the GUI here.
trap 'python3 "$ROOT/scripts/unregister-desktop-apps.py" "$APP" || true' EXIT

DMG=$(find build.noindex/darwin-arm64 -maxdepth 1 -name '*.dmg' | head -1)
[[ -n "$DMG" && -s "$DMG" ]] || { echo 'MyGo build did not produce a DMG' >&2; exit 1; }

# Built bundles must carry the promoted identity before they can ship.
plutil -extract CFBundleIdentifier raw -o - "$APP/Contents/Info.plist" | grep -qx 'com.hwj.easyagent' \
  || { echo 'Native app bundle identifier is not com.hwj.easyagent' >&2; exit 1; }

ICON=$(plutil -extract CFBundleIconFile raw -o - "$APP/Contents/Info.plist")
[[ -n "$ICON" && -s "$APP/Contents/Resources/$ICON" ]] \
  || { echo 'Native app is missing its Dock icon' >&2; exit 1; }

if [[ -n "$TAG" ]]; then
  CORE="$APP/Contents/Resources/easyagent"
  [[ $("$CORE" --version) == "easyagent $TAG" ]] || { echo 'Bundled core version does not match tag' >&2; exit 1; }
  REVISION=$(git rev-parse HEAD)
  METADATA=$(go version -m "$CORE")
  [[ "$METADATA" == *"vcs.revision=$REVISION"* && "$METADATA" == *"vcs.modified=false"* ]] \
    || { echo 'Bundled core source metadata does not match clean release revision' >&2; exit 1; }
fi

# Exercise bundle discovery AND the actual local-backend launcher with isolated
# configuration. A healthy Resources binary alone does not catch UI recursion.
# Scope to the packaging guarantees: the full desktop-native suite spawns
# WebView previews that need an unattended GUI session; it gates PRs on hosted
# runners (see #81) and times out on the shared-desktop self-hosted machine
# regardless of display state.
EASYAGENT_NATIVE_KEYCHAIN_TEST=1 EASYAGENT_TEST_BUNDLE="$PWD/$APP" go test -count=1 -run '^TestPackagedCoreSmoke$' .
EASYAGENT_NATIVE_KEYCHAIN_TEST=1 go test -count=1 ./tools/...

if [[ -n "$DIST" ]]; then
  mkdir -p "$DIST"
  cp "$DMG" "$DIST/easyagent-native-darwin-arm64.dmg"
  if [[ -n "$TAG" ]]; then
    cp "build.noindex/$TARGET/update-$TARGET.json" "$DIST/"
    cp "build.noindex/$TARGET/easyagent-${TAG#v}-$TARGET.tar.gz" "$DIST/"
    go run ./tools/verify-update mygo.json "$DIST" "${TAG#v}" "$TARGET"
  fi
  echo "Native artifact: $DIST/easyagent-native-darwin-arm64.dmg"
else
  echo "Native app: $APP"
  echo "Native DMG: $DMG"
fi
