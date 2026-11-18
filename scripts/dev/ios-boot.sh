#!/usr/bin/env bash
# Boot the shared iPhone simulator used for iOS tests and print its UDID.
# Run inside `nix develop .#ios`.
set -euo pipefail
cd "$(dirname "$0")/../.."

mkdir -p .dev
state=.dev/ios-device

if [ -f "$state" ]; then
  udid=$(cat "$state")
else
  runtime=$(xcrun simctl list runtimes | grep -o 'com\.apple\.CoreSimulator\.SimRuntime\.iOS-[A-Za-z0-9-]*' | head -1 || true)
  if [ -z "$runtime" ]; then
    echo "no iOS simulator runtime installed; run: xcodebuild -downloadPlatform iOS" >&2
    exit 1
  fi
  devicetype=$(xcrun simctl list devicetypes | grep -o 'com\.apple\.CoreSimulator\.SimDeviceType\.iPhone-[A-Za-z0-9-]*' | head -1)
  udid=$(xcrun simctl create natcatty-ios "$devicetype" "$runtime")
  echo "$udid" >"$state"
fi

xcrun simctl boot "$udid" 2>/dev/null || true
xcrun simctl bootstatus "$udid" -b >/dev/null
echo "$udid"
