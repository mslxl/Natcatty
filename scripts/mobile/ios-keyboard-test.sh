#!/usr/bin/env bash
# ios-keyboard-test.sh — drive iOS's real soft keyboard and IME on a
# simulator and read what the terminal wrote to its connection.
#
# The natcatty_smoke terminal diagnostic records the bytes its echo
# connection receives and shows them as an accessibility label, because a
# rendered terminal cell cannot tell Return's CR from a line feed. The
# XCUITest bundle next to this script taps the real UIKit keyboard: it
# presents and dismisses it, types Latin text, and composes Pinyin. The
# terminal's text client has unit tests too; this run is the check that the
# host path (UITextInput, insertText, the surface resize on dismissal) works.
#
# An iPad is used because its on-screen keyboard carries the explicit
# "Hide keyboard" key that a manual dismissal needs. CoreSimulator attaches
# a hardware keyboard by default, which suppresses the software keyboard,
# so the run detaches it with a small private-API helper.
#
# Usage: scripts/mobile/ios-keyboard-test.sh [-d UDID] [-k]
#   -d UDID   use this already booted iPad simulator and leave it running
#   -k        keep the build and result directory
#
# Run inside `nix develop .#ios` from the repository root. A run without
# -d creates (or reuses) its own iPad, and always shuts it down, including
# after a failure.
set -euo pipefail
cd "$(dirname "$0")/../.."

device=
keep=0
while [ $# -gt 0 ]; do
  case "$1" in
  -d | --device)
    device=${2:? -d needs a simulator UDID}
    shift
    ;;
  -k | --keep) keep=1 ;;
  *)
    echo "usage: $(basename "$0") [-d UDID] [-k]" >&2
    exit 2
    ;;
  esac
  shift
done

if [ "$(uname -s)" != "Darwin" ]; then
  echo "ios-keyboard-test.sh: the iOS simulator needs macOS with Xcode" >&2
  exit 1
fi
for tool in xcrun xcodebuild go; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "ios-keyboard-test.sh: $tool is not on PATH; run inside nix develop .#ios" >&2
    exit 1
  fi
done

# The app links the simulator slice of libghostty-vt, and the vendored host
# carries the keyboard patches.
scripts/mobile/ios-syso.sh simulator
scripts/mobile/mygo-patch.sh

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-ios-keyboard.XXXXXX")
stop_device=0
udid=$device
bundle_id=
cleanup() {
  if [ -n "$udid" ] && [ -n "$bundle_id" ]; then
    xcrun simctl terminate "$udid" "$bundle_id" >/dev/null 2>&1 || true
  fi
  if [ "$stop_device" -eq 1 ]; then
    xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
  fi
  if [ "$keep" -eq 0 ]; then
    rm -rf "$work"
  else
    echo "ios-keyboard-test.sh: kept $work" >&2
  fi
}
trap cleanup EXIT

if [ -z "$udid" ]; then
  mkdir -p .dev
  state=.dev/ios-keyboard-device
  if [ -f "$state" ]; then
    udid=$(cat "$state")
  fi
  if [ -z "$udid" ] || ! xcrun simctl list devices | grep -q "$udid"; then
    udid=$(xcrun simctl list devices |
      sed -n 's/.*natcatty-ipad-keyboard (\([0-9A-F-]*\)).*/\1/p' | head -1)
  fi
  if [ -z "$udid" ]; then
    # The same runtime rules as scripts/dev/ios-boot.sh: a general iOS
    # runtime ends at the version, while per-device runtime identifiers
    # cannot host a new device. Newest iPad device type first in simctl's
    # list, which is the one current runtimes accept.
    runtime=$(xcrun simctl list runtimes |
      grep -oE 'com\.apple\.CoreSimulator\.SimRuntime\.iOS-[0-9-]+$' |
      sort -t- -k2,2n -k3,3n | tail -1)
    devicetype=$(xcrun simctl list devicetypes |
      grep -oE 'com\.apple\.CoreSimulator\.SimDeviceType\.iPad-[A-Za-z0-9-]+' |
      head -1)
    if [ -z "$runtime" ] || [ -z "$devicetype" ]; then
      echo "ios-keyboard-test.sh: no iPad simulator runtime or device type installed" >&2
      exit 1
    fi
    udid=$(xcrun simctl create natcatty-ipad-keyboard "$devicetype" "$runtime")
  fi
  echo "$udid" >"$state"
  stop_device=1
fi
if ! xcrun simctl list devices | grep -q "$udid.*Booted"; then
  xcrun simctl boot "$udid"
fi
xcrun simctl bootstatus "$udid" -b >/dev/null
echo "ios-keyboard-test.sh: simulator $udid"

# A hardware keyboard hides the software keyboard, and the test would then
# have nothing to tap. The helper is compiled here so the checkout carries
# only its source.
xcrun clang -fobjc-arc -framework Foundation \
  -o "$work/hwkeyboard" scripts/mobile/ios-keyboard-uitest/hwkeyboard.m

# use_keyboard enables exactly one language keyboard for a phase. The
# keyboard daemon also caches the current input mode in its own preference
# domain, so changing AppleKeyboards alone keeps the previous keyboard until
# that cache is cleared and the device restarts. The restart re-attaches a
# hardware keyboard, hence the helper call after boot.
use_keyboard() {
  local list=$1
  xcrun simctl spawn "$udid" defaults write -g AppleKeyboards "($list)"
  xcrun simctl terminate "$udid" "$bundle_id" >/dev/null 2>&1 || true
  xcrun simctl spawn "$udid" defaults delete com.apple.keyboard.preferences KeyboardsCurrentAndNext >/dev/null 2>&1 || true
  xcrun simctl spawn "$udid" defaults delete com.apple.keyboard.preferences KeyboardLastUsed >/dev/null 2>&1 || true
  xcrun simctl shutdown "$udid"
  xcrun simctl boot "$udid"
  xcrun simctl bootstatus "$udid" -b >/dev/null
  "$work/hwkeyboard" "$udid" 0
}

# Each phase enables exactly one language keyboard. The globe key cycles
# through them plus the emoji keyboard, and its order is not stable enough
# to script; with one language there is nothing to cycle to. The Latin and
# dismissal tests run with English, the IME test with the Pinyin keyboard.
english_keyboard='"en_US@sw=QWERTY;hw=Automatic"'
pinyin_keyboard='"{{SECRET_njvhgzwm}};hw=Automatic"'

echo "ios-keyboard-test.sh: building the natcatty_smoke app"
GOFLAGS="${GOFLAGS:+$GOFLAGS }-tags=natcatty_smoke" \
  go tool mygo build -platform ios/simulator -o "$work/dist"
app=$(find "$work/dist/ios-simulator" -maxdepth 1 -name '*.app' -print -quit)
if [ -z "$app" ] || [ ! -d "$app" ]; then
  echo "ios-keyboard-test.sh: MyGo did not produce a simulator .app under $work/dist/ios-simulator" >&2
  exit 1
fi
bundle_id=$(/usr/bin/plutil -extract CFBundleIdentifier raw "$app/Info.plist")
xcrun simctl install "$udid" "$app"

# The nix shell exports LD and NIX_LDFLAGS for its own clang; Xcode must use
# its own linker, which knows the current -objc_abi_version flag.
run_suite() {
  local name=$1
  shift
  echo "ios-keyboard-test.sh: running the $name suite"
  if ! env -u LD -u NIX_LDFLAGS -u NIX_CFLAGS_COMPILE xcodebuild test \
    -project scripts/mobile/ios-keyboard-uitest/NatcattyKeyboardUITests.xcodeproj \
    -scheme NatcattyKeyboardUITests \
    -destination "platform=iOS Simulator,id=$udid" \
    -derivedDataPath "$work/derived" \
    -resultBundlePath "$work/result-$name.xcresult" \
    CODE_SIGNING_ALLOWED=NO "$@"; then
    keep=1
    echo "ios-keyboard-test.sh: the $name suite failed; results in $work/result-$name.xcresult" >&2
    return 1
  fi
}

use_keyboard "$english_keyboard"
run_suite english \
  -only-testing:NatcattyKeyboardUITests/SoftKeyboardUITests/testKeyboardComesBackAfterDismissal \
  -only-testing:NatcattyKeyboardUITests/SoftKeyboardUITests/testSoftKeyboardTypesAndReturnSendsCarriageReturn

use_keyboard "$pinyin_keyboard"
run_suite pinyin \
  -only-testing:NatcattyKeyboardUITests/SoftKeyboardUITests/testPinyinCompositionCommitsOnceAndEditsLocally

echo "ios-keyboard-test.sh: soft keyboard, dismissal and IME checks passed"
