#!/usr/bin/env bash
# check-deps.sh — the phone dependency gate.
#
# It walks the phone composition root's dependency graph, ./app under each
# phone GOOS, and fails when a desktop-only package or a MyGo plugin manifest
# is reachable. The graph is the truth a phone build links; a build tag on a
# file does not help if a shared file still imports the desktop package.
#
# CGO_ENABLED=1 matches what MyGo builds phones with. It is not incidental:
# from M1 on ui/terminal/mobile has cgo-only files, so a CGO_ENABLED=0 walk
# reports "build constraints exclude all Go files" for a package that is in
# fact part of the phone build. `go list` needs no C compiler.
#
# Usage: scripts/mobile/check-deps.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

module=github.com/mslxl/natcatty

# Exact import paths, never prefixes: ui/shell/state, ui/vault/state and
# ui/vault/section are allowed while ui/shell and ui/vault are not, and
# adapters/sftp is the protocol client the Session factory uses while
# ui/transfer is the desktop panel.
forbidden=(
  platform/pty
  adapters/localpty
  adapters/serial
  application/desktop
  application/sysmanager
  application/mcp
  ui/browser
  ui/titlebar
  ui/layout
  ui/dropdown
  ui/quake
  ui/tray
  ui/shell
  ui/monitoring
  ui/serial
  ui/forwarding
  ui/transfer
  ui/sysmanager
  ui/applock
  ui/vault
)

check() {
  local goos=$1 goarch=$2
  local graph
  # No -e: a load error must fail the gate, not be quietly skipped.
  if ! graph=$(GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=1 \
    go list -deps -f '{{.ImportPath}}	{{.Dir}}' ./app); then
    echo "check-deps: $goos/$goarch: go list failed; the phone graph does not load" >&2
    exit 1
  fi

  local failed=0 path dir rel forbidden_path
  while IFS=$'\t' read -r path dir; do
    rel=${path#"$module"/}
    for forbidden_path in "${forbidden[@]}"; do
      if [ "$path" = "$module/$forbidden_path" ]; then
        echo "check-deps: $goos/$goarch: desktop-only $forbidden_path is in the phone graph" >&2
        failed=1
      fi
    done
    if [ -f "$dir/mygo-plugin.json" ]; then
      if [ -n "$rel" ]; then
        echo "check-deps: $goos/$goarch: plugin manifest $rel/mygo-plugin.json is in the phone graph" >&2
      else
        echo "check-deps: $goos/$goarch: plugin manifest is in the phone graph at $dir" >&2
      fi
      failed=1
    fi
  done <<<"$graph"

  if [ "$failed" -ne 0 ]; then
    echo "check-deps: $goos/$goarch: the phone build reaches desktop-only code" >&2
    exit 1
  fi
  echo "check-deps: $goos/$goarch: phone graph is free of desktop-only packages and plugin manifests"
}

# The asymmetric tags make the pair the real target, not the single tag:
# ios implies darwin, android implies linux. Walk both phone roots.
check ios arm64
check android arm64

echo "check-deps: ok — no desktop-only code in the phone graph"
