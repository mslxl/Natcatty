#!/usr/bin/env bash
# Run the Atuin v18.24.5 release-acceptance conformance suite.
#
# This script starts a self-hosted Atuin server on an ephemeral loopback port,
# drives the official Atuin CLI and Netcatty's real sync stack, and tears every
# process and temporary database down again. It never contacts a hosted Atuin
# service and never calls DELETE /api/v0/store.
#
# Required environment:
#   ATUIN_CLI_BIN     path to the official `atuin` v18.24.5 binary
#   ATUIN_SERVER_BIN  path to the `atuin-server` v18.24.5 binary
#
# See docs/atuin/conformance.md for artifact checksums, local instructions,
# and the CI recipe. Extra arguments are passed to `go test` (for example,
# -run TestUnknownTagDoesNotAffectHistorySync).
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# Keep these values in sync with conformance/atuin/harness.go and
# docs/atuin/conformance.md when the reference pin moves.
pinned_version=18.24.5
pinned_commit=ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e

fail() {
  printf 'atuin-conformance: %s\n' "$*" >&2
  exit 1
}

: "${ATUIN_CLI_BIN:?set ATUIN_CLI_BIN to the official atuin v18.24.5 binary}"
: "${ATUIN_SERVER_BIN:?set ATUIN_SERVER_BIN to the atuin-server v18.24.5 binary}"

require_version() {
  local output=$1
  local name=$2
  local prefix="$name $pinned_version"
  case "$output" in
    "$prefix" | "$prefix "* | "$prefix("*) return 0 ;;
    *) fail "$name is '$output'; refusing to substitute a release other than $pinned_version" ;;
  esac
}

cli_version=$("$ATUIN_CLI_BIN" --version)
server_version=$("$ATUIN_SERVER_BIN" --version)
require_version "$cli_version" atuin
require_version "$server_version" atuin-server
[[ $cli_version == *"$pinned_commit"* ]] ||
  fail "official CLI is '$cli_version'; refusing evidence without pinned client commit $pinned_commit"

printf 'atuin-conformance: pinned CLI:    %s\n' "$cli_version"
printf 'atuin-conformance: pinned server: %s\n' "$server_version"
if command -v shasum >/dev/null 2>&1; then
  shasum -a 256 "$ATUIN_CLI_BIN" "$ATUIN_SERVER_BIN"
elif command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$ATUIN_CLI_BIN" "$ATUIN_SERVER_BIN"
fi

cd "$repo_root"
export GOFLAGS=${GOFLAGS:--buildvcs=false}
if command -v go >/dev/null 2>&1; then
  exec go test -tags conformance ./conformance/atuin -v -count=1 "$@"
fi
command -v nix >/dev/null 2>&1 ||
  fail "neither go nor nix is on PATH; install Go or enter the repository dev shell"
exec nix develop --command env GOFLAGS="$GOFLAGS" go test -tags conformance ./conformance/atuin -v -count=1 "$@"
