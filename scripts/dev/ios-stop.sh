#!/usr/bin/env bash
# Shut down the simulator started by ios-boot.sh. Safe to run when it is already off.
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ -f .dev/ios-device ]; then
  xcrun simctl shutdown "$(cat .dev/ios-device)" 2>/dev/null || true
fi
