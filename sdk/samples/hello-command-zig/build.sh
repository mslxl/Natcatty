#!/usr/bin/env bash
# Rebuilds the committed Zig sample module. Zig 0.16.0 is pinned by the flake.
#
# The generated C bindings live in this directory; natcatty_v1.c is compiled
# alongside the Zig source so the wasm imports keep their real module and field
# names. The result is a freestanding reactor with exported, bounded memory.
#
# After rebuilding, update [wasm].sha256 in plugin.toml with the value the
# script prints and commit both files.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
zig=${ZIG:-zig}

"$zig" build-exe "$here/src/main.zig" "$here/natcatty_v1.c" \
  -target wasm32-freestanding -O ReleaseSmall \
  -fno-entry -fstrip -rdynamic -I"$here" \
  --export-memory --max-memory=67108864 \
  -femit-bin="$here/plugin.wasm"

if command -v sha256sum >/dev/null 2>&1; then
  sum=$(sha256sum "$here/plugin.wasm" | cut -d' ' -f1)
else
  sum=$(shasum -a 256 "$here/plugin.wasm" | cut -d' ' -f1)
fi
echo "plugin.wasm sha256: $sum"
echo "Update [wasm].sha256 in $here/plugin.toml, then commit both files."
