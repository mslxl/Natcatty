#!/usr/bin/env bash
# Rebuilds the committed TinyGo sample module from the generated bindings.
#
# TinyGo 0.43.0 is pinned by the flake. target.json derives from TinyGo's
# wasm-unknown target but sets gc=none, panic=trap, no entry point, exported
# memory, and the manifest's 64 MiB maximum.
#
# TinyGo's c-shared link mode exports compiler-rt helpers and _initialize. The
# module contract has a closed export table, so wasm-opt removes exactly those
# toolchain symbols. The guest never calls _initialize: the sample has no
# package init, heap, scheduler, or WASI dependency.
#
# After rebuilding, update [wasm].sha256 in plugin.toml with the value the
# script prints and commit both files.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
tinygo=${TINYGO:-tinygo}
wasm_opt=${WASM_OPT:-$(command -v wasm-opt || true)}
if [ -z "$wasm_opt" ]; then
  echo "build.sh: wasm-opt not found; enter the Nix dev shell or set WASM_OPT" >&2
  exit 1
fi

# Build from the sample directory so TinyGo resolves this sample's go.mod, not
# the repository module that contains it.
(
  cd "$here"
  # -buildvcs=false is a Go build flag TinyGo does not always forward to the
  # go list it uses to resolve the package, so set it in GOFLAGS as well.
  GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false" \
    "$tinygo" build -target "$here/target.json" -no-debug -opt=z \
    -buildvcs=false -trimpath -o "$here/plugin.raw.wasm" .
)

"$wasm_opt" \
  --remove-exports='fminimum,fminimumf,fmaximum,fmaximumf,_initialize' \
  -o "$here/plugin.wasm" "$here/plugin.raw.wasm"
rm -f "$here/plugin.raw.wasm"

if command -v sha256sum >/dev/null 2>&1; then
  sum=$(sha256sum "$here/plugin.wasm" | cut -d' ' -f1)
else
  sum=$(shasum -a 256 "$here/plugin.wasm" | cut -d' ' -f1)
fi
echo "plugin.wasm sha256: $sum"
echo "Update [wasm].sha256 in $here/plugin.toml, then commit both files."
