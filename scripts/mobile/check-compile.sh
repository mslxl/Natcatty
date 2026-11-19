#!/usr/bin/env bash
# check-compile.sh — a phone target's compile gate.
#
# It builds the phone composition root and the packages a phone can reach with
# the SAME target toolchain MyGo uses to build the app: Xcode's clang with the
# iPhoneOS SDK's -target and -isysroot, never the host compiler. A bare
# GOOS=ios build against the host clang compiles cgo for macOS and exits 0
# without proving anything; this script also links the app as the c-archive
# MyGo's Swift host links, so an undefined symbol or a wrong-target object
# fails here instead of on a device.
#
# Usage: scripts/mobile/check-compile.sh ios
set -euo pipefail
cd "$(dirname "$0")/../.."

target=${1:-}
if [ "$target" != "ios" ]; then
  echo "usage: $(basename "$0") ios" >&2
  exit 2
fi

if [ "$(uname -s)" != "Darwin" ]; then
  echo "check-compile.sh: iOS builds need macOS with Xcode" >&2
  exit 1
fi

# The nix dev shell puts an xcbuild wrapper named xcrun on PATH and points
# DEVELOPER_DIR at nixpkgs' macOS SDK, neither of which can resolve an iOS
# SDK. Use the host Xcode toolchain the flake's ios shell names, and clear the
# nix SDKROOT so clang takes the iPhoneSimulator SDK.
export DEVELOPER_DIR="${NATCATTY_XCODE_DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}"
unset SDKROOT
xcrun=/usr/bin/xcrun
sdk=$("$xcrun" --sdk iphonesimulator --show-sdk-path)
clang=$("$xcrun" --sdk iphonesimulator --find clang)
# The minimum must be the one the committed configuration declares. Read it
# from mygo.json so the gate cannot drift from what the app ships.
minimum=$(/usr/bin/plutil -extract ios.minimumSystemVersion raw mygo.json)

arch=arm64
cc="$clang -target $arch-apple-ios$minimum-simulator -isysroot $sdk"

echo "check-compile: ios/$arch, minimum iOS $minimum"
echo "check-compile: CC=$cc"

# The app links libghostty-vt through a staged object. Require the simulator
# slice and verify it before compiling: a device object (platform 2) in a
# simulator build fails here, not on the running app.
echo "check-compile: checking the staged terminal object"
scripts/mobile/ios-syso.sh simulator --check

roots=(./app ./ui/phoneshell ./ui/session ./ui/terminal/...)
echo "check-compile: compiling ${roots[*]}"
GOOS=ios GOARCH="$arch" CGO_ENABLED=1 CC="$cc" go build "${roots[@]}"

# Compiling packages does not link them. Produce the archive MyGo hands to
# swiftc, which runs the C linker with the target clang above.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
echo "check-compile: linking the app c-archive"
GOOS=ios GOARCH="$arch" CGO_ENABLED=1 CC="$cc" \
  go build -buildmode=c-archive -o "$work/libapp.a" .

# The archive must carry the linked library. Without the staged object the
# link fails on the undefined ghostty_* symbols that addr.c takes the
# addresses of; this assertion also catches an object silently dropped.
if ! /usr/bin/nm -gU "$work/libapp.a" 2>/dev/null | grep '_ghostty_type_json' >/dev/null; then
  echo "check-compile: the app archive does not carry libghostty-vt" >&2
  echo "  the staged object was not linked; check ui/terminal/mobile/ghostty_ios_arm64.syso" >&2
  exit 1
fi

echo "check-compile: ok — ios phone build compiles and links"
