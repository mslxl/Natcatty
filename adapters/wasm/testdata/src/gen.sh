#!/usr/bin/env bash
# Rebuilds the committed WASM contract fixtures. The binaries are checked in
# so tests and CI never need a C toolchain.
#
# Requires clang with wasm-ld and Go on PATH (for example an LLVM 21 dev shell plus
# `nix build nixpkgs#lld`). Override with CLANG and WASM_LD if needed.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
out=$(dirname "$here")
clang=${CLANG:-clang}
lld=${WASM_LD:-$(command -v wasm-ld || true)}
if [ -z "$lld" ]; then
  echo "gen.sh: wasm-ld not found; set WASM_LD" >&2
  exit 1
fi

for source in "$here"/*.c; do
  name=$(basename "$source" .c)
  # Most fixtures declare the 64 MiB default maximum. The memory-growth
  # fixture is linked with the 1 MiB maximum its contract test declares, so
  # it can actually reach the ceiling and trap.
  max_memory=67108864
  case "$name" in
    memory-growth) max_memory=1048576 ;;
    unbounded-memory) max_memory="" ;;
    start-section)
      # wasm-ld only emits a core start section for PIC or shared-memory
      # output. Link this reactor with its start function exported, then drop
      # that export and insert section 8, so the fixture exercises the start
      # section rule without exporting _start.
      "$clang" --target=wasm32 -O2 -nostdlib -fuse-ld="$lld" \
        -Wl,--no-entry -Wl,--export-memory \
        -Wl,--initial-memory=262144 -Wl,--max-memory=67108864 \
        -Wl,--export=plugin_start -o "$out/$name.raw.wasm" "$source"
      go run "$here/start_section_gen.go" "$out/$name.raw.wasm" "$out/$name.wasm" plugin_start
      rm -f "$out/$name.raw.wasm"
      continue
      ;;
  esac
  if [ -n "$max_memory" ]; then
    "$clang" --target=wasm32 -O2 -nostdlib -fuse-ld="$lld" \
      -Wl,--no-entry -Wl,--export-memory \
      -Wl,--initial-memory=262144 -Wl,--max-memory="$max_memory" \
      -o "$out/$name.wasm" "$source"
  else
    "$clang" --target=wasm32 -O2 -nostdlib -fuse-ld="$lld" \
      -Wl,--no-entry -Wl,--export-memory -Wl,--initial-memory=262144 \
      -o "$out/$name.wasm" "$source"
  fi
done

cp "$out/hello.wasm" "$out/devplugin/plugin.wasm"
