#!/usr/bin/env bash
# Create (once) and boot the Android emulator used for phone tests.
# Run inside `nix develop .#android`; headless, so it is safe in a terminal.
set -euo pipefail
cd "$(dirname "$0")/../.."

mkdir -p .dev/avd .dev/android-home
avd=natcatty-avd
image="system-images;android-36;google_apis;arm64-v8a"

if ! emulator -list-avds | grep -qx "$avd"; then
  echo no | avdmanager create avd -n "$avd" -k "$image" --force >/dev/null
fi

# `adb start-server` can hang on the reply-fd path inside a nix shell; starting
# the fork-server directly is reliable and gives the same server.
adb kill-server >/dev/null 2>&1 || true
nohup adb -L tcp:5037 fork-server server >/dev/null 2>&1 &
sleep 1

if adb devices | grep -q '^emulator-'; then
  adb devices | awk '/^emulator-/ {print $1; exit}'
  exit 0
fi

nohup emulator -avd "$avd" -no-window -no-audio -no-snapshot -no-boot-anim \
  >.dev/android-emulator.log 2>&1 &
echo $! >.dev/android-emulator.pid

adb wait-for-device
for _ in $(seq 1 180); do
  if [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then
    adb devices | awk '/^emulator-/ {print $1; exit}'
    exit 0
  fi
  sleep 2
done

echo "emulator did not finish booting; see .dev/android-emulator.log" >&2
exit 1
