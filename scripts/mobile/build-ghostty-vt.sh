#!/usr/bin/env bash
# Build libghostty-vt for natcatty's phone targets from the pinned commit.
#
# This is the only entry point: it downloads and verifies the pinned Ghostty
# source and Zig 0.16.0, builds the iOS device and simulator static libraries
# and the three Android ABI archives, checks that every artifact reports the
# ABI MyGo's Go bindings mirror (library version 0.1.0-dev), checks the
# binding's 93 symbols, links the Android archives into a 16 KB-page probe,
# and writes ui/terminal/artifacts.json (commit + sha256 + size per target).
# No binary is written into the repository.
#
# Prerequisites:
#   iOS      macOS with Xcode:            nix develop .#ios
#   Android  the pinned NDK and emulator: nix develop .#android
#   Zig      downloaded and sha256-checked when not already available
#
# Examples:
#   # every phone target on macOS (Xcode plus the Android NDK shell).
#   nix develop .#android --command scripts/mobile/build-ghostty-vt.sh build
#
#   # only the Android archives on Linux CI.
#   nix develop .#android --command scripts/mobile/build-ghostty-vt.sh \
#       build --platforms android
#
#   # re-check an existing output directory against the committed manifest.
#   scripts/mobile/build-ghostty-vt.sh verify
#
#   # show the recorded manifest after validation.
#   scripts/mobile/build-ghostty-vt.sh manifest
#
# Build output goes to build/ghostty-vt/ (gitignored). To publish, upload
# that directory's files and SHA256SUMS as the assets of the release named in
# the manifest; the URLs in ui/terminal/artifacts.json point there.
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

if ! command -v go >/dev/null 2>&1; then
    echo "build-ghostty-vt.sh: go is not on PATH; run inside nix develop .#ios or .#android" >&2
    exit 1
fi

exec go run ./tools/ghosttyvt "$@"
