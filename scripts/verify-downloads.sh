#!/usr/bin/env bash
# Verify every workflow artifact the release build jobs uploaded.
#
# releasecheck classify decides what each directory is; this script does not
# inspect names itself, so the workflow and the Go tooling share one rule.
# Platform directories are verified one by one (a bad checksum or an unlisted
# artifact fails the step), the release-notes directory is skipped because it
# has no SHA256SUMS and is not a release artifact, and anything else fails
# closed before any verification runs.
#
# Usage: scripts/verify-downloads.sh DOWNLOADS_DIR
# Set RELEASECHECK to run a prebuilt tool; default: go run ./internal/tools/releasecheck
set -euo pipefail

root=${1:?usage: verify-downloads.sh DOWNLOADS_DIR}

if [[ -n ${RELEASECHECK:-} ]]; then
  releasecheck=("${RELEASECHECK}")
else
  releasecheck=(go run ./internal/tools/releasecheck)
fi

classification=$(mktemp)
trap 'rm -f "$classification"' EXIT

"${releasecheck[@]}" classify -dir "$root" > "$classification"
platforms=0
while IFS=$'\t' read -r kind dir; do
  case "$kind" in
    platform)
      "${releasecheck[@]}" verify-sums -dir "$dir"
      platforms=$((platforms + 1))
      echo "verified ${dir}"
      ;;
    notes)
      echo "${dir} holds the release notes, not a platform artifact"
      ;;
    *)
      echo "unknown artifact kind ${kind} for ${dir}" >&2
      exit 1
      ;;
  esac
done < "$classification"

# The notes alone are not a release: without at least one verified platform
# directory the build artifacts never arrived, so this step must not pass.
if (( platforms == 0 )); then
  echo "no platform artifact directories under ${root}" >&2
  exit 1
fi
