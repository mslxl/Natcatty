#!/usr/bin/env bash
# fetch-artifacts.sh — put the pinned libghostty-vt artifacts in place.
#
# CI starts from a clean checkout, where build/ghostty-vt is empty. This
# downloads only what a job needs (ios or android), verifying each file's
# sha256 and size against ui/terminal/artifacts.json. A missing or corrupt
# artifact fails here, naming the artifact, instead of failing a link later.
#
# The release is the fast path, not the only one: when an asset cannot be
# downloaded (an unpublished or withdrawn release), the command builds the
# same pinned commit from source. That source tarball is sha256-pinned too, so
# a fresh runner still gets verified native inputs without a binary ever being
# committed. Pass --build-if-missing=false to require the release instead.
#
# Usage:
#   scripts/mobile/fetch-artifacts.sh --platform ios
#   scripts/mobile/fetch-artifacts.sh --platform android [--out DIR]
#   scripts/mobile/fetch-artifacts.sh --platform ios --build-if-missing=false
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

if ! command -v go >/dev/null 2>&1; then
    echo "fetch-artifacts.sh: go is not on PATH; run inside nix develop .#ios or .#android" >&2
    exit 1
fi

exec go run ./tools/ghosttyvt fetch "$@"
