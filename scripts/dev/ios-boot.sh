#!/usr/bin/env bash
# Boot the shared iPhone simulator used for iOS tests and print its UDID.
# Run inside `nix develop .#ios`.
set -euo pipefail
cd "$(dirname "$0")/../.."

mkdir -p .dev
state=.dev/ios-device

if [ -f "$state" ]; then
  udid=$(cat "$state")
fi
if [ -z "${udid:-}" ] || ! xcrun simctl list devices | grep -q "$udid"; then
  # Reuse the shared device if it exists (its state file can be lost, e.g. a
  # clean CI checkout), else create it.
  udid=$(xcrun simctl list devices |
    sed -n 's/.*natcatty-ios (\([0-9A-F-]*\)).*/\1/p' | head -1)
fi
if [ -z "${udid:-}" ]; then
  # Only a runtime identifier that ends after the version is a general iOS
  # runtime. Xcode also lists per-device runtimes such as
  # `...SimRuntime.iOS-27-2-iPhone19-4`, which cannot host an iPhone device
  # type; taking the first match made device creation fail with "Incompatible
  # device". Pick the newest general runtime. BSD sort has no -V, so sort the
  # version fields instead.
  runtime=$(xcrun simctl list runtimes |
    grep -oE 'com\.apple\.CoreSimulator\.SimRuntime\.iOS-[0-9-]+$' |
    sort -t- -k2,2n -k3,3n | tail -1)
  if [ -z "$runtime" ]; then
    echo "no iOS simulator runtime installed; run: xcodebuild -downloadPlatform iOS" >&2
    exit 1
  fi
  devicetype=$(xcrun simctl list devicetypes |
    grep -oE 'com\.apple\.CoreSimulator\.SimDeviceType\.iPhone-[A-Za-z0-9-]+' |
    head -1)
  if [ -z "$devicetype" ]; then
    echo "no iPhone simulator device type installed" >&2
    exit 1
  fi
  udid=$(xcrun simctl create natcatty-ios "$devicetype" "$runtime")
fi
echo "$udid" >"$state"

xcrun simctl boot "$udid" 2>/dev/null || true
xcrun simctl bootstatus "$udid" -b >/dev/null
echo "$udid"
