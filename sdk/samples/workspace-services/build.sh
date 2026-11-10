#!/usr/bin/env bash
# Rebuilds the committed workspace-services sample module. The .wasm artifact
# is checked in so `go test ./...` and CI never need a C toolchain.
#
# Requires clang with wasm-ld. The Nix dev shell ships clang but not lld, so
# either put wasm-ld on PATH or point WASM_LD at it, for example:
#
#   nix build nixpkgs#lld
#   WASM_LD=$(nix build --print-out-paths nixpkgs#lld)/bin/wasm-ld ./build.sh
#
# After rebuilding, update [wasm].sha256 in plugin.toml with the value the
# script prints and commit both files.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
clang=${CLANG:-clang}
lld=${WASM_LD:-$(command -v wasm-ld || true)}
if [ -z "$lld" ]; then
  echo "build.sh: wasm-ld not found; set WASM_LD" >&2
  exit 1
fi

# The module declares the 64 MiB maximum the manifest also requests.
"$clang" --target=wasm32 -O2 -nostdlib -ffreestanding -fno-builtin \
  -fuse-ld="$lld" \
  -Wl,--no-entry -Wl,--export-memory \
  -Wl,--initial-memory=262144 -Wl,--max-memory=67108864 \
  -o "$here/plugin.wasm" "$here/src/main.c"

if command -v sha256sum >/dev/null 2>&1; then
  sum=$(sha256sum "$here/plugin.wasm" | cut -d' ' -f1)
else
  sum=$(shasum -a 256 "$here/plugin.wasm" | cut -d' ' -f1)
fi
echo "plugin.wasm sha256: $sum"
echo "Update [wasm].sha256 in $here/plugin.toml, then commit both files."
