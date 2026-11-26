#!/usr/bin/env bash
# android-libs.sh — stage and verify libghostty-vt for the Android ABIs.
#
# The Android phone build links one static archive per ABI through the
# #cgo LDFLAGS in ui/terminal/mobile/mobile.go. The archives are release
# artifacts, never committed, so this script copies them from the pinned
# build output into the paths cgo names. It refuses an archive that is not
# the ABI's ELF target, is missing a symbol the bindings look up, reports a
# different library version, or cannot be linked into a DSO whose LOAD
# segments are aligned for 16 KB pages.
#
# The ABI list is scripts/mobile/android-abis.txt, which the compile gate
# and internal/archtest read too.
#
# Usage:
#   scripts/mobile/android-libs.sh [--abi ABI] [--input DIR]
#   scripts/mobile/android-libs.sh --check [--abi ABI]
#
# Flags:
#   --abi ABI     arm64-v8a, armeabi-v7a or x86_64; default: every ABI
#   --input DIR   directory holding libghostty-vt-<commit8>-<artifact>.a;
#                 default build/ghostty-vt (build it with
#                 scripts/mobile/build-ghostty-vt.sh build --platforms android)
#   --check       verify the staged archives and stage nothing; the compile
#                 gate uses this, so a missing or wrong archive fails the
#                 build instead of the app at launch
set -euo pipefail
cd "$(dirname "$0")/../.."

abis_file=scripts/mobile/android-abis.txt
input=build/ghostty-vt
want=
check=false
while [ $# -gt 0 ]; do
  case "$1" in
  --abi)
    want=${2:?--abi needs an ABI}
    shift
    ;;
  --input)
    input=${2:?--input needs a directory}
    shift
    ;;
  --check) check=true ;;
  *)
    echo "android-libs.sh: unknown argument $1" >&2
    exit 2
    ;;
  esac
  shift
done

# The NDK is the compiler MyGo's androidToolchain.clang picks and the source
# of llvm-nm and llvm-readelf. Resolve it the same way check-compile.sh does.
ndk=${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}
if [ -z "$ndk" ]; then
  sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
  for candidate in "$sdk"/ndk/*; do
    if [ -d "$candidate" ]; then
      ndk=$candidate
    fi
  done
fi
if [ -z "$ndk" ] || [ ! -d "$ndk" ]; then
  echo "android-libs.sh: no Android NDK; run inside nix develop .#android, or set ANDROID_NDK_HOME" >&2
  exit 1
fi
host=
for prebuilt in "$ndk"/toolchains/llvm/prebuilt/*; do
  if [ -d "$prebuilt/bin" ]; then
    host=$prebuilt
    break
  fi
done
if [ -z "$host" ] || [ ! -d "$host/bin" ]; then
  echo "android-libs.sh: $ndk has no LLVM toolchain" >&2
  exit 1
fi
bin=$host/bin

min=$(sed -n 's/.*"minSdk"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' mygo.json | head -n 1)
if [ -z "$min" ]; then
  echo "android-libs.sh: mygo.json does not declare android.minSdk" >&2
  exit 1
fi
# MyGo clamps the compiler's API level to 21 (cmd/mygo/android.go).
api=$((min > 21 ? min : 21))

# The pinned library version and commit prefix come from the committed
# manifest, so the staged archive cannot be one the Go bindings reject.
version=$(sed -n 's/.*"libVersion": "\([^"]*\)".*/\1/p' ui/terminal/artifacts.json | head -n 1)
prefix=$(sed -n 's/.*"commit": "\([0-9a-f]\{8\}\)[0-9a-f]*".*/\1/p' ui/terminal/artifacts.json | head -n 1)
if [ -z "$version" ] || [ -z "$prefix" ]; then
  echo "android-libs.sh: ui/terminal/artifacts.json has no libVersion or commit" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-android-libs.XXXXXX")
trap 'rm -rf "$work"' EXIT

# nm_list writes the archive's defined global symbols to a file once, so the
# binding's list is checked without re-reading a 10-20 MB archive per symbol.
nm_list() { # nm_list ARCHIVE OUT
  "$bin/llvm-nm" -g --defined-only "$1" >"$2"
}

# verify ARCHIVE ABI CLASS MACHINE TRIPLE
# The archive is either the build output or the staged copy; both must pass.
verify() {
  local archive=$1 abi=$2 class=$3 machine=$4 triple=$5
  local header
  if ! header=$("$bin/llvm-readelf" -h "$archive" 2>/dev/null); then
    echo "android-libs.sh: $abi: not an ELF archive: $archive" >&2
    exit 1
  fi
  if ! grep -q "Class:[[:space:]]*$class\$" <<<"$header"; then
    echo "android-libs.sh: $abi: $archive is not $class" >&2
    exit 1
  fi
  if ! grep -Eq "Machine:.*${machine}\$" <<<"$header"; then
    echo "android-libs.sh: $abi: $archive is not $machine" >&2
    exit 1
  fi

  local symbols="$work/nm-$abi.txt"
  nm_list "$archive" "$symbols"
  local name count bad=0
  while IFS= read -r name; do
    [ -z "$name" ] && continue
    count=$(awk -v s="$name" '$NF == s { n++ } END { print n + 0 }' "$symbols")
    if [ "$count" != 1 ]; then
      echo "android-libs.sh: $abi: $archive defines $name $count times, want once" >&2
      bad=1
    fi
  done <ui/terminal/internal/vt/symbols.txt
  [ "$bad" = 0 ] || exit 1

  if ! grep -a -q "$version" "$archive"; then
    echo "android-libs.sh: $abi: $archive does not contain the version string $version" >&2
    exit 1
  fi

  # A static archive has no program headers. Link it into a probe DSO with
  # the 16 KB page size MyGo passes and check every LOAD segment: that is
  # what proves the archive can ship on Android 15+ devices.
  local probe="$work/page-$abi.c" so="$work/page-$abi.so"
  printf 'int natcatty_page_probe;\n' >"$probe"
  local cc="$bin/${triple}${api}-clang"
  if [ ! -x "$cc" ]; then
    echo "android-libs.sh: $cc does not exist (minSdk $min, clang API $api)" >&2
    exit 1
  fi
  if ! "$cc" --target="$triple$api" -fPIC -shared -O0 \
    -Wl,--whole-archive "$archive" -Wl,--no-whole-archive \
    -Wl,-z,max-page-size=16384 -o "$so" >"$work/link-$abi.log" 2>&1; then
    echo "android-libs.sh: $abi: linking the 16 KB probe failed:" >&2
    cat "$work/link-$abi.log" >&2
    exit 1
  fi
  local aligns align
  aligns=$("$bin/llvm-readelf" -l -W "$so" | awk '$1 == "LOAD" { print $NF }')
  if [ -z "$aligns" ]; then
    echo "android-libs.sh: $abi: the 16 KB probe has no LOAD segments" >&2
    exit 1
  fi
  for align in $aligns; do
    align=$((align))
    if [ "$align" -lt 16384 ] || [ $((align & (align - 1))) -ne 0 ]; then
      echo "android-libs.sh: $abi: LOAD segment alignment is $((align)) bytes, want at least 16384 (0x4000) for 16 KB pages" >&2
      exit 1
    fi
  done

  echo "android-libs.sh: $abi ($class $machine) has all binding symbols, version $version, and 16 KB pages"
}

selected=0
while read -r abi goarch triple artifact class machine; do
  case "$abi" in '' | '#'*) continue ;; esac
  if [ -n "$want" ] && [ "$want" != "$abi" ] && [ "$want" != "$goarch" ]; then
    continue
  fi
  selected=$((selected + 1))

  dst="ui/terminal/mobile/lib/android/$abi/libghostty-vt.a"
  if $check; then
    if [ ! -f "$dst" ]; then
      echo "android-libs.sh: no staged $abi archive at $dst" >&2
      echo "  stage it first: scripts/mobile/android-libs.sh --abi $abi" >&2
      exit 1
    fi
    verify "$dst" "$abi" "$class" "$machine" "$triple"
    echo "android-libs.sh: $dst is a verified $abi archive"
    continue
  fi

  asset="libghostty-vt-$prefix-$artifact.a"
  src="$input/$asset"
  if [ ! -f "$src" ]; then
    echo "android-libs.sh: pinned $abi archive missing: $src" >&2
    echo "  build it: scripts/mobile/build-ghostty-vt.sh build --platforms android" >&2
    echo "  or copy the release asset $asset into $input" >&2
    exit 1
  fi
  cp "$src" "$work/$abi.a"
  verify "$work/$abi.a" "$abi" "$class" "$machine" "$triple"
  mkdir -p "$(dirname "$dst")"
  mv "$work/$abi.a" "$dst"
  echo "android-libs.sh: staged $abi ($(wc -c <"$dst" | tr -d ' ') bytes) at $dst"
done <"$abis_file"

if [ "$selected" -eq 0 ]; then
  echo "android-libs.sh: unknown Android ABI $want: arm64-v8a, armeabi-v7a or x86_64" >&2
  exit 2
fi
