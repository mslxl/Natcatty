#!/usr/bin/env bash
# android-link-smoke.sh — link each Android ABI's staged archive and compare
# the linked object with the Go bindings' symbol list.
#
# The compile gate links the Go app; this gate isolates the native link. For
# one ABI (or every ABI) it:
#
#   1. verifies the staged archive with android-libs.sh (ELF identity, all
#      binding symbols defined once, library version, 16 KB pages);
#   2. links the archive into a probe DSO with --whole-archive and the same
#      16 KB page flag the app uses;
#   3. reads that linked DSO with check-symbols.sh, so a symbol the linker
#      dropped or duplicated fails here instead of on a device.
#
# Usage: scripts/mobile/android-link-smoke.sh [--abi ABI]
#   ABI is an android-abis.txt name (arm64-v8a, armeabi-v7a, x86_64) or a
#   Go target (arm64, arm, amd64); default: every ABI.
set -euo pipefail
cd "$(dirname "$0")/../.."

want=
while [ $# -gt 0 ]; do
  case "$1" in
  --abi)
    want=${2:?--abi needs an ABI}
    shift
    ;;
  *)
    echo "usage: $(basename "$0") [--abi ABI]" >&2
    exit 2
    ;;
  esac
  shift
done

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
  echo "android-link-smoke.sh: no Android NDK; run inside nix develop .#android, or set ANDROID_NDK_HOME" >&2
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
  echo "android-link-smoke.sh: $ndk has no LLVM toolchain" >&2
  exit 1
fi
bin=$host/bin

min=$(sed -n 's/.*"minSdk"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' mygo.json | head -n 1)
if [ -z "$min" ]; then
  echo "android-link-smoke.sh: mygo.json does not declare android.minSdk" >&2
  exit 1
fi
api=$((min > 21 ? min : 21))

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-android-link.XXXXXX")
trap 'rm -rf "$work"' EXIT

selected=0
while read -r abi goarch triple artifact class machine; do
  case "$abi" in '' | '#'*) continue ;; esac
  if [ -n "$want" ] && [ "$want" != "$abi" ] && [ "$want" != "$goarch" ]; then
    continue
  fi
  selected=$((selected + 1))

  dst="ui/terminal/mobile/lib/android/$abi/libghostty-vt.a"
  echo "android-link-smoke: verifying the staged $abi archive"
  scripts/mobile/android-libs.sh --check --abi "$abi"

  cc="$bin/${triple}${api}-clang"
  if [ ! -x "$cc" ]; then
    echo "android-link-smoke.sh: $cc does not exist (minSdk $min, clang API $api)" >&2
    exit 1
  fi
  probe="$work/probe-$abi.c"
  so="$work/libghostty-$abi.so"
  printf 'int natcatty_link_smoke;\n' >"$probe"
  if ! "$cc" --target="$triple$api" -fPIC -shared -O0 \
    -Wl,--whole-archive "$dst" -Wl,--no-whole-archive \
    -Wl,-z,max-page-size=16384 -o "$so" >"$work/link-$abi.log" 2>&1; then
    echo "android-link-smoke: $abi: linking the probe DSO failed:" >&2
    cat "$work/link-$abi.log" >&2
    exit 1
  fi

  header=$("$bin/llvm-readelf" -h "$so")
  if ! grep -q "Class:[[:space:]]*$class\$" <<<"$header"; then
    echo "android-link-smoke: $abi: the linked DSO is not $class" >&2
    exit 1
  fi
  if ! grep -Eq "Machine:.*${machine}\$" <<<"$header"; then
    echo "android-link-smoke: $abi: the linked DSO is not $machine" >&2
    exit 1
  fi

  scripts/mobile/check-symbols.sh elf "$so" "$bin/llvm-nm"
  echo "android-link-smoke: $abi links as $class $machine with every pinned symbol defined once"
done <scripts/mobile/android-abis.txt

if [ "$selected" -eq 0 ]; then
  echo "android-link-smoke.sh: unknown Android ABI $want: arm64-v8a, armeabi-v7a or x86_64" >&2
  exit 2
fi
echo "android-link-smoke: ok — every selected ABI links with the binding list intact"
