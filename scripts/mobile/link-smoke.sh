#!/usr/bin/env bash
# link-smoke.sh — prove the final link of the phone terminal, including the
# fallback for a library that needs a linker argument.
#
# The Go c-archive is not the final link: MyGo's Swift host links the app.
# This script takes the staged object to that final link on its own:
#
#   1. link a probe that calls libghostty-vt through the object — success;
#   2. link a probe that also needs -lz without the fallback object — this
#      MUST fail with an undefined symbol, so a missing linker argument is a
#      loud build failure, not a crash at run time;
#   3. build the fallback object (`.linker_option "-lz"`), put it in an
#      archive, and link the same probe — success, so the fallback is known
#      to work before it is ever needed.
#
# Usage: scripts/mobile/link-smoke.sh [device|simulator]   (default simulator)
set -euo pipefail
cd "$(dirname "$0")/../.."

target=${1:-simulator}
case "$target" in
device)
  sdk=iphoneos
  clang_suffix=""
  ;;
simulator)
  sdk=iphonesimulator
  clang_suffix="-simulator"
  ;;
*)
  echo "usage: $(basename "$0") [device|simulator]" >&2
  exit 2
  ;;
esac

minimum=$(/usr/bin/plutil -extract ios.minimumSystemVersion raw mygo.json)
sdkpath=$(/usr/bin/xcrun --sdk "$sdk" --show-sdk-path)
clang=$(/usr/bin/xcrun --sdk "$sdk" --find clang)
triple="arm64-apple-ios$minimum$clang_suffix"

work=$(mktemp -d "${TMPDIR:-/tmp}/natcatty-link-smoke.XXXXXX")
trap 'rm -rf "$work"' EXIT

echo "link-smoke: staging the $target object"
scripts/mobile/ios-syso.sh "$target" --out "$work/ghostty_ios_arm64.syso"
cp "$work/ghostty_ios_arm64.syso" "$work/ghostty.o"

# Every symbol the bindings look up is defined exactly once.
nm_out=$work/nm.txt
/usr/bin/nm -gU "$work/ghostty.o" >"$nm_out"
missing=0
while read -r sym; do
  if ! grep -q " _$sym\$" "$nm_out"; then
    echo "link-smoke: $sym is missing from the $target object" >&2
    missing=1
  fi
done <ui/terminal/internal/vt/symbols.txt
[ "$missing" = 0 ] || exit 1
dups=$(awk '$2 ~ /^[TDBR]$/ {print $3}' "$nm_out" | sort | uniq -d)
if [ -n "$dups" ]; then
  echo "link-smoke: duplicate definitions in the $target object:" >&2
  echo "$dups" >&2
  exit 1
fi
echo "link-smoke: all $(wc -l <ui/terminal/internal/vt/symbols.txt | tr -d ' ') binding symbols defined once"

# probe.c needs the library and libz, so the link tells the two apart.
cat >"$work/probe.c" <<'EOF'
extern const char *ghostty_type_json(void);
extern int compress2(unsigned char *dest, unsigned long *destLen,
                     const unsigned char *source, unsigned long sourceLen, int level);
int main(void) {
    volatile const char *m = ghostty_type_json();
    volatile int z = compress2(0, 0, 0, 0, 0);
    return (m == 0) | (z == 12345);
}
EOF

# probe_real.c only needs the library: the staged object must link on its
# own, or the app could not.
cat >"$work/probe_real.c" <<'EOF'
extern const char *ghostty_type_json(void);
int main(void) {
    return ghostty_type_json() == 0;
}
EOF

"$clang" -arch arm64 -target "$triple" -isysroot "$sdkpath" \
  "$work/probe_real.c" "$work/ghostty.o" -o "$work/probe-real"
echo "link-smoke: the $target object links through the final-link toolchain"

# Step 2: without the fallback the -lz symbols are undefined, and that is a
# loud failure.
if "$clang" -arch arm64 -target "$triple" -isysroot "$sdkpath" \
  "$work/probe.c" "$work/ghostty.o" -o "$work/probe-no-lz" >"$work/no-lz.txt" 2>&1; then
  echo "link-smoke: a link missing -lz succeeded; the gate is not testing anything" >&2
  exit 1
fi
if ! grep -q '_compress2' "$work/no-lz.txt"; then
  echo "link-smoke: the failed link did not report the missing -lz symbol:" >&2
  cat "$work/no-lz.txt" >&2
  exit 1
fi
echo "link-smoke: a missing linker argument fails the final link"

# Step 3: the fallback object carries the option, survives membership in an
# archive, and makes the same link succeed.
scripts/mobile/ios-syso.sh "$target" --fallback --out "$work/ghostty-fallback.syso"
if ! /usr/bin/otool -l "$work/ghostty-fallback.syso" | grep -- '-lz' >/dev/null; then
  echo "link-smoke: the fallback object carries no -lz linker option" >&2
  exit 1
fi
cp "$work/ghostty-fallback.syso" "$work/ghostty-fallback.o"
/usr/bin/ar rcs "$work/libghostty-fallback.a" "$work/ghostty-fallback.o"
"$clang" -arch arm64 -target "$triple" -isysroot "$sdkpath" \
  "$work/probe.c" "$work/libghostty-fallback.a" -o "$work/probe-fallback"
echo "link-smoke: the fallback object links the same probe through an archive"

echo "link-smoke: ok — $target object and fallback both link"
