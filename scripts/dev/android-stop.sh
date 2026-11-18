#!/usr/bin/env bash
# Stop the emulator started by android-boot.sh. Safe to run when it is already off.
set -euo pipefail
cd "$(dirname "$0")/../.."

adb emu kill >/dev/null 2>&1 || true
if [ -f .dev/android-emulator.pid ]; then
  kill "$(cat .dev/android-emulator.pid)" 2>/dev/null || true
  rm -f .dev/android-emulator.pid
fi
adb kill-server >/dev/null 2>&1 || true
