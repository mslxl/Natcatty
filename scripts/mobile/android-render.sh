#!/usr/bin/env bash
# android-render.sh — run the terminal corpus and the diagnostic page on the
# connected Android device (or emulator), for the device's own ABI.
#
# The phone build is the same Go code for every ABI; the ABI only selects the
# NDK target and the archive cgo links. This script builds the two test
# binaries for the device's primary ABI, pushes them, and runs:
#
#   ui/terminal    TestVTCorpus: the shared VT corpus through the echo
#                  transport, asserting text, style runs, cursor cell and
#                  cursor shape on the device.
#   ui/phoneshell  TestDiagnosticRunsEchoTransport (natcatty_smoke): the
#                  diagnostic page renders the same corpus.
#
# The ARM device's ABI may be arm64-v8a or armeabi-v7a; an x86_64 emulator
# selects amd64. Each ABI's archive is verified by android-libs.sh first,
# including its 16 KB page alignment.
#
# Usage: scripts/mobile/android-render.sh [-s SERIAL]
#   -s SERIAL   the adb serial; default: adb's one connected device
set -euo pipefail
cd "$(dirname "$0")/../.."

serial=
while [ $# -gt 0 ]; do
  case "$1" in
  -s)
    serial=${2:? -s needs a serial}
    shift
    ;;
  *)
    echo "usage: $(basename "$0") [-s SERIAL]" >&2
    exit 2
    ;;
  esac
  shift
done

adb=(adb)
if [ -n "$serial" ]; then
  adb+=(-s "$serial")
fi

state=$("${adb[@]}" get-state 2>/dev/null || true)
if [ "$state" != "device" ]; then
  echo "android-render.sh: no Android device is ready; boot one: scripts/dev/android-boot.sh" >&2
  exit 1
fi

abi=$("${adb[@]}" shell getprop ro.product.cpu.abi | tr -d '\r')
row=
while read -r row_abi goarch triple artifact class machine; do
  case "$row_abi" in '' | '#'*) continue ;; esac
  if [ "$row_abi" = "$abi" ]; then
    row="$row_abi $goarch $triple $artifact $class $machine"
  fi
done <scripts/mobile/android-abis.txt
if [ -z "$row" ]; then
  echo "android-render.sh: the device reports ABI $abi, which is not a shipped ABI" >&2
  exit 1
fi
read -r abi goarch triple artifact class machine <<<"$row"

echo "android-render.sh: device $("${adb[@]}" get-serialno), ABI $abi ($goarch)"

# Resolve the NDK exactly as the compile gate does.
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
  echo "android-render.sh: no Android NDK; run inside nix develop .#android" >&2
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
  echo "android-render.sh: $ndk has no LLVM toolchain" >&2
  exit 1
fi

min=$(sed -n 's/.*"minSdk"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' mygo.json | head -n 1)
api=$((min > 21 ? min : 21))

# Refuse a staged archive that could not run on this device before spending
# time on the cross-build.
scripts/mobile/android-libs.sh --check --abi "$abi"

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-android-render.XXXXXX")
remote=/data/local/tmp/natcatty-render
cleanup() {
  "${adb[@]}" shell rm -rf "$remote" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

cc="$host/bin/${triple}${api}-clang"
build_env=(GOOS=android GOARCH="$goarch" CGO_ENABLED=1 CC="$cc"
  CGO_LDFLAGS="-Wl,-z,max-page-size=16384")
if [ "$goarch" = "arm" ]; then
  build_env+=(GOARM=7)
fi

echo "android-render.sh: building the test binaries for $abi"
env "${build_env[@]}" go test -c -o "$work/terminal.test" ./ui/terminal
env "${build_env[@]}" go test -c -tags natcatty_smoke -o "$work/phoneshell.test" ./ui/phoneshell

"${adb[@]}" shell rm -rf "$remote"
"${adb[@]}" shell mkdir -p "$remote"
"${adb[@]}" push "$work/terminal.test" "$remote/terminal.test" >/dev/null
"${adb[@]}" push "$work/phoneshell.test" "$remote/phoneshell.test" >/dev/null
"${adb[@]}" shell chmod 755 "$remote/terminal.test" "$remote/phoneshell.test"

# run_test runs one on-device test and requires its PASS line: adb shell's
# exit status alone would not prove the suite ran. NATCATTY_GHOSTTY_VT tells
# the shared test helper not to skip a statically linked library.
run_test() { # run_test TEST BINARY
  local name=$1 binary=$2 log="$work/$name.log"
  echo "android-render.sh: running $name on $abi"
  if ! "${adb[@]}" shell \
    "NATCATTY_GHOSTTY_VT=linked $remote/$binary -test.v -test.run $name -test.timeout 5m" \
    | tee "$log"; then
    echo "android-render.sh: $name failed on $abi" >&2
    exit 1
  fi
  if ! grep -q -- "--- PASS: $name " "$log"; then
    echo "android-render.sh: $name did not report PASS on $abi" >&2
    exit 1
  fi
}

run_test TestVTCorpus terminal.test
run_test TestDiagnosticRunsEchoTransport phoneshell.test

echo "android-render.sh: ok — corpus styles/cursor and the diagnostic page passed on $abi"
