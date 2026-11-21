#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../workflow-runtime"
npm ci --ignore-scripts
npm run build
npm test
gzip -n -9 -c output/workflow-runtime.mjs > ../internal/dynamicflow/workflow-runtime.mjs.gz

cp vendor/ZCODE-LICENSE ../internal/dynamicflow/ZCODE-LICENSE.txt
cp vendor/ZCODE-NOTICE.md ../internal/dynamicflow/ZCODE-NOTICE.txt
cp vendor/ZCODE-THIRD-PARTY-NOTICES.md ../internal/dynamicflow/ZCODE-THIRD-PARTY-NOTICES.txt
