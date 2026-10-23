#!/usr/bin/env bash
# Build a same-certificate future APK, then exercise the normal current build.
set -euo pipefail
cd "$(dirname "$0")/../mobile-native"
./gradlew --no-daemon :app:assembleDebug -PeasyagentVersion=99.0.0 -PeasyagentVersionCode=990000300
mkdir -p app/build/generated/update-fixture-assets
cp app/build/outputs/apk/debug/app-debug.apk app/build/generated/update-fixture-assets/update.apk
fixture_signing=$(mktemp -d)
trap 'rm -rf "$fixture_signing"' EXIT
export EASYAGENT_TEST_SIGNING_PASSWORD=disposable-updater-fixture
"${JAVA_HOME}/bin/keytool" -genkeypair -alias fixture -keyalg RSA -keysize 2048 -validity 2 \
  -dname 'CN=Untrusted update test' -storetype PKCS12 -keystore "$fixture_signing/untrusted.p12" \
  -storepass:env EASYAGENT_TEST_SIGNING_PASSWORD -noprompt
"${ANDROID_HOME}/build-tools/35.0.1/apksigner" sign --ks "$fixture_signing/untrusted.p12" \
  --ks-key-alias fixture --ks-pass env:EASYAGENT_TEST_SIGNING_PASSWORD \
  --out app/build/generated/update-fixture-assets/untrusted.apk app/build/generated/update-fixture-assets/update.apk
./gradlew --no-daemon :app:connectedDebugAndroidTest "$@"
