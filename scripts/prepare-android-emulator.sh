#!/usr/bin/env bash
set -euo pipefail

# The emulator action runs each input line in a separate shell. Keep the
# build wait and conditional cleanup together in this single script.
: "${RUNNER_TEMP:?}"
timeout 900 bash -c 'until test -f "$RUNNER_TEMP/android-build.exit"; do sleep 1; done'
cat "$RUNNER_TEMP/android-build.log"
test "$(cat "$RUNNER_TEMP/android-build.exit")" = 0

# Check after downloads and compilation. API 31 can require over 7 GiB even
# with disk-size configured. Spacious runners need no costly cleanup.
available_kb=$(df -Pk "$HOME" | awk 'NR == 2 {print $4}')
if [ "$available_kb" -lt 10485760 ]; then
  df -h "$HOME"
  # This hosted job uses Java/Gradle/Android, not .NET or the CodeQL bundle.
  sudo rm -rf /usr/share/dotnet "${RUNNER_TOOL_CACHE:?}/CodeQL"
  df -h "$HOME"
fi
