#!/usr/bin/env bash
# check-symbols.sh — compare a linked artifact with the binding list.
#
# ui/terminal/internal/vt/symbols.txt is the list of libghostty-vt symbols
# the Go bindings look up. It is the contract between the pinned native
# library and the app: a symbol missing from the final link is a crash at
# run time, and a symbol defined twice is ABI drift. This gate reads the
# linked artifact itself (a c-archive, a c-shared library or a probe DSO),
# not the staging archive, so a linker that drops or duplicates a symbol
# fails the build.
#
# Usage: scripts/mobile/check-symbols.sh {macho|elf} ARTIFACT [NM]
#   macho     symbols are prefixed with "_" (Apple nm)
#   elf       symbols are bare (llvm-nm or GNU nm)
#   NM        the symbol reader; defaults per format
set -euo pipefail
cd "$(dirname "$0")/../.."

format=${1:-}
artifact=${2:-}
nm=${3:-}
case "$format" in
macho | elf) ;;
*)
  echo "usage: $(basename "$0") {macho|elf} ARTIFACT [NM]" >&2
  exit 2
  ;;
esac
if [ -z "$artifact" ] || [ ! -f "$artifact" ]; then
  echo "check-symbols: no artifact at ${artifact:-<empty>}" >&2
  exit 1
fi
if [ -z "$nm" ]; then
  if [ "$format" = "macho" ]; then
    nm=/usr/bin/nm
  elif command -v llvm-nm >/dev/null 2>&1; then
    nm=llvm-nm
  else
    nm=nm
  fi
fi
if ! command -v "$nm" >/dev/null 2>&1; then
  echo "check-symbols: symbol reader $nm not found" >&2
  exit 1
fi

symbols=ui/terminal/internal/vt/symbols.txt
if [ ! -s "$symbols" ]; then
  echo "check-symbols: $symbols is missing or empty" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-symbols.XXXXXX")
trap 'rm -rf "$work"' EXIT
listing=$work/nm.txt

# Read the whole listing once: under pipefail a downstream grep -q could
# close the pipe early and make the symbol reader's SIGPIPE the status.
if ! "$nm" -g --defined-only "$artifact" >"$listing" 2>"$work/nm.err"; then
  echo "check-symbols: cannot read symbols from $artifact with $nm:" >&2
  cat "$work/nm.err" >&2
  exit 1
fi

prefix=
[ "$format" = "macho" ] && prefix=_

missing=0
duplicated=0
while IFS= read -r name; do
  [ -z "$name" ] && continue
  want="${prefix}${name}"
  count=$(awk -v s="$want" '$NF == s { n++ } END { print n + 0 }' "$listing")
  if [ "$count" -eq 0 ]; then
    echo "check-symbols: $artifact is missing the pinned symbol $name" >&2
    missing=1
  elif [ "$count" -gt 1 ]; then
    echo "check-symbols: $artifact defines the pinned symbol $name $count times, want once" >&2
    duplicated=1
  fi
done <"$symbols"

if [ "$missing" -ne 0 ] || [ "$duplicated" -ne 0 ]; then
  echo "check-symbols: ABI drift in $artifact; the Go bindings expect $symbols" >&2
  exit 1
fi
echo "check-symbols: $artifact defines all $(wc -l <"$symbols" | tr -d ' ') binding symbols exactly once"
