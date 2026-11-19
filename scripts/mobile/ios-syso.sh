#!/usr/bin/env bash
# ios-syso.sh — stage libghostty-vt for one iOS target as the Go object the
# phone app links.
#
# Method A: iOS links the pinned static library through a *_ios_arm64.syso
# built with `ld -r -all_load`, staged next to ui/terminal/mobile, with an
# empty #cgo LDFLAGS. MyGo's own final Swift link is not modified.
#
# A device and a simulator slice share the file name and architecture, so
# exactly one is staged and the script refuses the wrong one: the slice's
# LC_BUILD_VERSION platform must be 2 (device) or 7 (simulator), and the
# output is checked again before it is installed. check-compile.sh calls
# --check, so a wrong or missing object fails the build, not the running app.
#
# Usage:
#   scripts/mobile/ios-syso.sh {device|simulator} [--fallback]
#   scripts/mobile/ios-syso.sh {device|simulator} --check
#
# Flags:
#   --input PATH    the .a slice; defaults to the pinned artifact under
#                   build/ghostty-vt/ (build it with build-ghostty-vt.sh)
#   --out PATH      where to write the .syso; default
#                   ui/terminal/mobile/ghostty_ios_arm64.syso
#   --fallback      also carry `.linker_option "-lz"` in the object, for a
#                   library that needs a linker argument MyGo's link cannot
#                   take; link-smoke.sh proves this path works
#   --check         only verify the --out object's target and symbols
set -euo pipefail
cd "$(dirname "$0")/../.."

target=${1:-}
case "$target" in
device | simulator) ;;
*)
  echo "usage: $(basename "$0") {device|simulator} [--fallback] [--check]" >&2
  exit 2
  ;;
esac
shift

fallback=false
check=false
input=""
out="ui/terminal/mobile/ghostty_ios_arm64.syso"
while [ $# -gt 0 ]; do
  case "$1" in
  --fallback) fallback=true ;;
  --check) check=true ;;
  --input)
    input=${2:?--input needs a path}
    shift
    ;;
  --out)
    out=${2:?--out needs a path}
    shift
    ;;
  *)
    echo "ios-syso.sh: unknown argument $1" >&2
    exit 2
    ;;
  esac
  shift
done

if [ "$(uname -s)" != "Darwin" ]; then
  echo "ios-syso.sh: iOS objects need macOS with Xcode" >&2
  exit 1
fi

case "$target" in
device)
  sdk=iphoneos
  platform=ios
  platform_num=2
  clang_target_suffix=""
  default_input=build/ghostty-vt/libghostty-vt-befcdfd2-ios-arm64.a
  ;;
simulator)
  sdk=iphonesimulator
  platform=ios-simulator
  platform_num=7
  clang_target_suffix="-simulator"
  default_input=build/ghostty-vt/libghostty-vt-befcdfd2-ios-arm64-simulator.a
  ;;
esac
input=${input:-$default_input}

manifest_platform() {
  /usr/bin/otool -l "$1" 2>/dev/null |
    awk '/LC_BUILD_VERSION/{found=1} found && $1=="platform"{print $2; exit}'
}

manifest_minos() {
  /usr/bin/otool -l "$1" 2>/dev/null |
    awk '/LC_BUILD_VERSION/{found=1} found && $1=="minos"{print $2; exit}'
}

has_symbol() {
  # Read the whole listing: under `set -o pipefail`, grep -q closing the
  # pipe early would make nm's SIGPIPE the pipeline's status.
  /usr/bin/nm -gU "$1" 2>/dev/null | grep '_ghostty_type_json' >/dev/null
}

verify() { # verify PATH WHAT
  local path=$1 what=$2 got
  if ! got=$(manifest_platform "$path") || [ -z "$got" ]; then
    echo "ios-syso.sh: $what has no LC_BUILD_VERSION platform: $path" >&2
    exit 1
  fi
  if [ "$got" != "$platform_num" ]; then
    local other="device"
    [ "$platform_num" = 2 ] && other="simulator"
    echo "ios-syso.sh: $what is built for platform $got, not $platform_num ($target);" >&2
    echo "  a $target build must not link the $other object: $path" >&2
    exit 1
  fi
  if ! has_symbol "$path"; then
    echo "ios-syso.sh: $what does not export ghostty_type_json: $path" >&2
    exit 1
  fi
}

# --check is the build gate: a missing or wrong-platform staged object fails
# here, before the compiler runs.
if $check; then
  if [ ! -f "$out" ]; then
    echo "ios-syso.sh: no $target object at $out" >&2
    echo "  stage it first: scripts/mobile/ios-syso.sh $target" >&2
    exit 1
  fi
  verify "$out" "the staged object"
  echo "ios-syso.sh: $out is a $target object for iOS"
  exit 0
fi

if [ ! -f "$input" ]; then
  echo "ios-syso.sh: pinned $target slice missing: $input" >&2
  echo "  build it: scripts/mobile/build-ghostty-vt.sh build --platforms ios" >&2
  exit 1
fi
verify "$input" "the pinned $target slice"

minimum=$(/usr/bin/plutil -extract ios.minimumSystemVersion raw mygo.json)
sdkpath=$(/usr/bin/xcrun --sdk "$sdk" --show-sdk-path)
sdkver=$(/usr/bin/xcrun --sdk "$sdk" --show-sdk-version)
clang=$(/usr/bin/xcrun --sdk "$sdk" --find clang)

outdir=$(dirname "$out")
mkdir -p "$outdir"
work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-ios-syso.XXXXXX")
trap 'rm -rf "$work"' EXIT

# stage is the write: exactly one target is installed at a time. The lock
# keeps a device and a simulator build from racing in one checkout.
lock="$outdir/.ios-syso.lock"
tries=0
while ! mkdir "$lock" 2>/dev/null; do
  tries=$((tries + 1))
  if [ "$tries" -gt 120 ]; then
    echo "ios-syso.sh: another ios-syso.sh holds $lock" >&2
    exit 1
  fi
  sleep 0.25
done
trap 'rm -rf "$work"; rmdir "$lock" 2>/dev/null || true' EXIT

args=(-r -arch arm64 -platform_version "$platform" "$minimum" "$sdkver" -all_load "$input")
if $fallback; then
  printf '.linker_option "-lz"\n' >"$work/linkopts.s"
  "$clang" -arch arm64 -target "arm64-apple-ios$minimum$clang_target_suffix" \
    -isysroot "$sdkpath" -c "$work/linkopts.s" -o "$work/linkopts.o"
  args+=("$work/linkopts.o")
fi
staged="$work/ghostty_ios_arm64.syso"
/usr/bin/ld "${args[@]}" -o "$staged"
verify "$staged" "the object just built"
if [ "$(manifest_minos "$staged")" != "$minimum" ]; then
  echo "ios-syso.sh: staged object's minimum is $(manifest_minos "$staged"), mygo.json declares $minimum" >&2
  exit 1
fi

mv "$staged" "$out"
echo "ios-syso.sh: staged $target object ($(wc -c <"$out" | tr -d ' ') bytes) at $out"
