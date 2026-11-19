#!/usr/bin/env bash
# fetch-artifacts.sh — download the pinned libghostty-vt release artifacts.
#
# CI starts from a clean checkout, where build/ghostty-vt is empty. This
# downloads only what a job needs (ios or android), verifying each file's
# sha256 and size against ui/terminal/artifacts.json. A missing or corrupt
# artifact fails here, naming the artifact, instead of failing a link later.
#
# Usage:
#   scripts/mobile/fetch-artifacts.sh --platform ios
#   scripts/mobile/fetch-artifacts.sh --platform android [--out DIR]
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

if ! command -v go >/dev/null 2>&1; then
    echo "fetch-artifacts.sh: go is not on PATH; run inside nix develop .#ios or .#android" >&2
    exit 1
fi

exec go run ./tools/ghosttyvt fetch "$@"
