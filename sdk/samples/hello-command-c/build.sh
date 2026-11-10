#!/usr/bin/env bash
# Rebuilds the committed C sample module from the generated C bindings.
#
# The Nix dev shell pins clang 21.1.8, LLD 21.1.8 (wasm-ld), and Go. The module
# is a freestanding reactor: no libc, no WASI, exported memory, and a bounded
# 64 MiB maximum the manifest also declares.
#
# After rebuilding, update [wasm].sha256 in plugin.toml with the value the
# script prints and commit both files.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
# The Nix dev shell exports the unwrapped pinned compiler: its cc-wrapper adds
# macOS hardening flags (-fzero-call-used-regs) that wasm32 rejects.
clang=${CLANG:-${NATCATTY_CLANG:-clang}}
lld=${WASM_LD:-${NATCATTY_WASM_LD:-$(command -v wasm-ld || true)}}
if [ -z "$lld" ]; then
  echo "build.sh: wasm-ld not found; enter the Nix dev shell or set WASM_LD" >&2
  exit 1
fi

"$clang" --target=wasm32 -O2 -nostdlib -ffreestanding -fno-builtin \
  -fuse-ld="$lld" -I"$here" \
  -Wl,--no-entry -Wl,--export-memory \
  -Wl,--initial-memory=262144 -Wl,--max-memory=67108864 \
  -o "$here/plugin.wasm" "$here/src/main.c" "$here/natcatty_v1.c"

if command -v sha256sum >/dev/null 2>&1; then
  sum=$(sha256sum "$here/plugin.wasm" | cut -d' ' -f1)
else
  sum=$(shasum -a 256 "$here/plugin.wasm" | cut -d' ' -f1)
fi
echo "plugin.wasm sha256: $sum"
echo "Update [wasm].sha256 in $here/plugin.toml, then commit both files."
