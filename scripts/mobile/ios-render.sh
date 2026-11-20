#!/usr/bin/env bash
# ios-render.sh — run the terminal diagnostic on the iOS simulator.
#
# The simulator smoke builds a natcatty_smoke app, installs it on the shared
# simulator, launches it, and waits for the diagnostic page to log the echo
# corpus it rendered. That line is the proof the staged simulator object, the
# Go c-archive, the Swift host and the paint all work on the simulator; a
# production app would not open the diagnostic, so this is the smoke build.
#
# The staged object must already be the simulator slice:
#   scripts/mobile/ios-syso.sh simulator
#
# Usage: scripts/mobile/ios-render.sh [-d UDID]
#   -d UDID   use this simulator, already booted, and leave it running;
#             default: scripts/dev/ios-boot.sh boots the shared simulator,
#             and this script shuts it down when the run finishes.
set -euo pipefail
cd "$(dirname "$0")/../.."

device=
while [ $# -gt 0 ]; do
  case "$1" in
  -d | --device)
    device=${2:? -d needs a simulator UDID}
    shift
    ;;
  *)
    echo "usage: $(basename "$0") [-d UDID]" >&2
    exit 2
    ;;
  esac
  shift
done

if [ "$(uname -s)" != "Darwin" ]; then
  echo "ios-render.sh: the iOS simulator needs macOS with Xcode" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "ios-render.sh: go is not on PATH; run inside nix develop .#ios" >&2
  exit 1
fi

# A device object in a simulator build fails before the app can start.
scripts/mobile/ios-syso.sh simulator --check
# The Swift host comes from the vendored MyGo; without its patches the app
# builds but the keyboard fix is absent.
scripts/apply-mygo-patches.sh

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-ios-render.XXXXXX")
launch_pid=
bundle_id=
stop_device=0
cleanup() {
  if [ -n "$launch_pid" ]; then
    kill "$launch_pid" 2>/dev/null || true
  fi
  if [ -n "$device" ] && [ -n "$bundle_id" ]; then
    /usr/bin/xcrun simctl terminate "$device" "$bundle_id" >/dev/null 2>&1 || true
  fi
  if [ "$stop_device" -eq 1 ]; then
    scripts/dev/ios-stop.sh >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

if [ -z "$device" ]; then
  device=$(scripts/dev/ios-boot.sh)
  stop_device=1
fi
if ! /usr/bin/xcrun simctl list devices | grep -q "$device.*Booted"; then
  echo "ios-render.sh: simulator $device is not booted" >&2
  exit 1
fi
echo "ios-render.sh: simulator $device"

echo "ios-render.sh: building the natcatty_smoke app for the simulator"
# mygo reads extra build tags from GOFLAGS; app/run_mobile.go opens the
# diagnostic only in a natcatty_smoke build.
GOFLAGS="${GOFLAGS:+$GOFLAGS }-tags=natcatty_smoke" \
  go tool mygo build -platform ios/simulator -o "$work/dist"

app=$(find "$work/dist/ios-simulator" -maxdepth 1 -name '*.app' -print -quit)
if [ -z "$app" ] || [ ! -d "$app" ]; then
  echo "ios-render.sh: MyGo did not produce a simulator .app under $work/dist/ios-simulator" >&2
  exit 1
fi
exe_name=$(/usr/bin/plutil -extract CFBundleExecutable raw "$app/Info.plist")
bundle_id=$(/usr/bin/plutil -extract CFBundleIdentifier raw "$app/Info.plist")
if ! /usr/bin/vtool -show-build "$app/$exe_name" | grep -q 'platform IOSSIMULATOR'; then
  echo "ios-render.sh: $app/$exe_name is not a simulator executable" >&2
  exit 1
fi

/usr/bin/xcrun simctl install "$device" "$app"
echo "ios-render.sh: launched $bundle_id; waiting for the diagnostic log"

log="$work/launch.log"
/usr/bin/xcrun simctl launch --console-pty "$device" "$bundle_id" >"$log" 2>&1 &
launch_pid=$!

# Wait on the observable log line, not a fixed delay. The deadline is the
# only failure path: on timeout the captured log is the diagnosis.
deadline=$((SECONDS + 180))
while ! grep -q 'natcatty_smoke: libghostty-vt .*echo text' "$log" 2>/dev/null; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "ios-render.sh: the diagnostic did not report the echo text within 180s; log:" >&2
    cat "$log" >&2
    exit 1
  fi
  sleep 1
done
grep 'natcatty_smoke:' "$log"
echo "ios-render.sh: ok — the terminal diagnostic rendered the echo corpus on $device"
