#!/usr/bin/env bash
# check-compile.sh — a phone target's compile gate.
#
# It builds the phone composition root and the packages a phone can reach with
# the SAME target toolchain MyGo uses to build the app, never the host
# compiler:
#
#   ios      Xcode's clang with the iPhoneOS SDK's -target and -isysroot, and
#            the app linked as the c-archive MyGo's Swift host links. A bare
#            GOOS=ios build against the host clang compiles cgo for macOS and
#            exits 0 without proving anything.
#   android  the NDK's <triple><minSdk>-clang for each ABI, which is the
#            compiler MyGo's androidToolchain.clang picks, and the app linked
#            as the c-shared library Gradle puts in jniLibs. Against the host
#            clang the build dies inside runtime/cgo instead of compiling for
#            Android.
#
# Usage: scripts/mobile/check-compile.sh ios          # the simulator (default)
#        scripts/mobile/check-compile.sh ios/simulator
#        scripts/mobile/check-compile.sh ios/device
#        scripts/mobile/check-compile.sh android          # every shipped ABI
#        scripts/mobile/check-compile.sh android/arm64    # one ABI: arm64, arm, amd64
set -euo pipefail
cd "$(dirname "$0")/../.."

roots=(./app ./ui/phoneshell ./ui/session ./ui/terminal/...)

# min_sdk reads the Android minimum from mygo.json, where the committed
# configuration declares it, so the gate cannot drift from what the app ships.
# The key is unique in the file; the iOS minimum is read with plutil below
# because macOS also has a minimumSystemVersion key.
min_sdk() {
  sed -n 's/.*"minSdk"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' mygo.json | head -n 1
}

check_ios() {
  local target=${1:-simulator} sdk_name clang_suffix
  case "$target" in
  device)
    sdk_name=iphoneos
    clang_suffix=""
    ;;
  simulator)
    sdk_name=iphonesimulator
    clang_suffix="-simulator"
    ;;
  *)
    echo "check-compile.sh: unknown iOS target $target: device or simulator" >&2
    exit 2
    ;;
  esac
  if [ "$(uname -s)" != "Darwin" ]; then
    echo "check-compile.sh: iOS builds need macOS with Xcode" >&2
    exit 1
  fi

  # The nix dev shell puts an xcbuild wrapper named xcrun on PATH and points
  # DEVELOPER_DIR at nixpkgs' macOS SDK, neither of which can resolve an iOS
  # SDK. Use the host Xcode toolchain the flake's ios shell names, and clear
  # the nix SDKROOT so clang takes the iPhoneSimulator SDK.
  export DEVELOPER_DIR="${NATCATTY_XCODE_DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}"
  unset SDKROOT
  local xcrun=/usr/bin/xcrun
  local sdk clang
  sdk=$("$xcrun" --sdk "$sdk_name" --show-sdk-path)
  clang=$("$xcrun" --sdk "$sdk_name" --find clang)
  # The minimum must be the one the committed configuration declares. Read it
  # from mygo.json so the gate cannot drift from what the app ships.
  local min
  min=$(/usr/bin/plutil -extract ios.minimumSystemVersion raw mygo.json)
  if [ -z "$min" ]; then
    echo "check-compile.sh: mygo.json does not declare ios.minimumSystemVersion" >&2
    exit 1
  fi

  local arch=arm64
  local cc="$clang -target $arch-apple-ios$min$clang_suffix -isysroot $sdk"

  echo "check-compile: ios/$target, minimum iOS $min"
  echo "check-compile: CC=$cc"

  # The app links libghostty-vt through a staged object. Require the matching
  # slice and verify it before compiling: a device object (platform 2) in a
  # simulator build fails here, not on the running app, and the reverse too.
  echo "check-compile: checking the staged $target terminal object"
  scripts/mobile/ios-syso.sh "$target" --check

  echo "check-compile: compiling ${roots[*]}"
  GOOS=ios GOARCH="$arch" CGO_ENABLED=1 CC="$cc" go build "${roots[@]}"

  # Compiling packages does not link them. Produce the archive MyGo hands to
  # swiftc, which runs the C linker with the target clang above.
  # work is global so the EXIT trap can still name it after this function
  # returns; a local would be unset when the trap runs.
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  echo "check-compile: linking the app c-archive"
  GOOS=ios GOARCH="$arch" CGO_ENABLED=1 CC="$cc" \
    go build -buildmode=c-archive -o "$work/libapp.a" .

  # The archive must carry every symbol the bindings look up, exactly once.
  # This is the linked artifact, not the staged object: an object silently
  # dropped or a linker that drops a symbol fails here.
  scripts/mobile/check-symbols.sh macho "$work/libapp.a" /usr/bin/nm

  echo "check-compile: ok — ios/$target phone build compiles and links"
}

check_android() {
  local want=${1:-}
  # The ABI list is scripts/mobile/android-abis.txt, shared with
  # android-libs.sh and internal/archtest; it mirrors cmd/mygo/android.go's
  # androidABIs. armeabi-v7a is kept and needs GOARM=7.

  local ndk=${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}
  if [ -z "$ndk" ]; then
    local sdk=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
    # Glob expansion is lexicographic, so the last directory is the newest.
    local candidate
    for candidate in "$sdk"/ndk/*; do
      if [ -d "$candidate" ]; then
        ndk=$candidate
      fi
    done
  fi
  if [ -z "$ndk" ] || [ ! -d "$ndk" ]; then
    echo "check-compile.sh: no Android NDK; run inside nix develop .#android, or set ANDROID_NDK_HOME" >&2
    exit 1
  fi
  # A glob loop, not `ls`: under pipefail a failing ls would abort the script
  # with ls's own error instead of the diagnostic below.
  local host=
  local prebuilt
  for prebuilt in "$ndk"/toolchains/llvm/prebuilt/*; do
    if [ -d "$prebuilt/bin" ]; then
      host=$prebuilt
      break
    fi
  done
  if [ -z "$host" ] || [ ! -d "$host/bin" ]; then
    echo "check-compile.sh: $ndk has no LLVM toolchain" >&2
    exit 1
  fi

  local min
  min=$(min_sdk)
  if [ -z "$min" ]; then
    echo "check-compile.sh: mygo.json does not declare android.minSdk" >&2
    exit 1
  fi
  # MyGo clamps the compiler's API level to 21; a lower minimum still builds
  # against 21 (cmd/mygo/android.go, androidToolchain.clang).
  local api=$(( min > 21 ? min : 21 ))

  local selected=()
  local abi goarch triple artifact class machine
  while read -r abi goarch triple artifact class machine; do
    case "$abi" in '' | '#'*) continue ;; esac
    if [ -z "$want" ] || [ "$want" = "$abi" ] || [ "$want" = "$goarch" ]; then
      selected+=("$abi $goarch $triple $artifact $class $machine")
    fi
  done <scripts/mobile/android-abis.txt
  if [ ${#selected[@]} -eq 0 ]; then
    echo "check-compile.sh: unknown Android ABI $want: arm64, arm or amd64" >&2
    exit 2
  fi

  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT

  # verify_shared_object checks what Gradle actually ships in
  # jniLibs/<abi>: the c-shared library's ELF target, the linked terminal
  # library, and the LOAD alignment. The page flag is an input; this is the
  # output, so a flag that did not take effect still fails the gate.
  verify_shared_object() {
    local so=$1 abi=$2 class=$3 machine=$4 header aligns align
    if ! header=$("$host/bin/llvm-readelf" -h "$so" 2>/dev/null); then
      echo "check-compile: $abi: libmygoapp.so is not an ELF object" >&2
      exit 1
    fi
    if ! grep -q "Class:[[:space:]]*$class\$" <<<"$header"; then
      echo "check-compile: $abi: libmygoapp.so is not $class" >&2
      exit 1
    fi
    if ! grep -Eq "Machine:.*${machine}\$" <<<"$header"; then
      echo "check-compile: $abi: libmygoapp.so is not $machine" >&2
      exit 1
    fi
    if ! "$host/bin/llvm-nm" "$so" 2>/dev/null |
      awk '$NF == "ghostty_type_json" { n++ } END { exit !(n > 0) }'; then
      echo "check-compile: $abi: libmygoapp.so does not carry libghostty-vt" >&2
      exit 1
    fi
    aligns=$("$host/bin/llvm-readelf" -l -W "$so" | awk '$1 == "LOAD" { print $NF }')
    if [ -z "$aligns" ]; then
      echo "check-compile: $abi: libmygoapp.so has no LOAD segments" >&2
      exit 1
    fi
    for align in $aligns; do
      align=$((align))
      if [ "$align" -lt 16384 ] || [ $((align & (align - 1))) -ne 0 ]; then
        echo "check-compile: $abi: libmygoapp.so has a $((align))-byte LOAD alignment, want at least 16384 (0x4000)" >&2
        exit 1
      fi
    done
    echo "check-compile: $abi libmygoapp.so is $class $machine with 16 KB LOAD segments"
  }

  for entry in "${selected[@]}"; do
    read -r abi goarch triple artifact class machine <<<"$entry"
    local cc="$host/bin/${triple}${api}-clang"
    if [ ! -x "$cc" ]; then
      echo "check-compile.sh: $cc does not exist (minSdk $min, clang API $api)" >&2
      exit 1
    fi

    # The app links libghostty-vt through the staged archive cgo names.
    # Verify every selected ABI before compiling: a missing, wrong-target or
    # 4 KB-page archive fails here, not on a device at launch.
    echo "check-compile: checking the staged $abi terminal archive"
    scripts/mobile/android-libs.sh --check --abi "$abi"

    # 16 KB pages are what new devices have and Google Play asks for; MyGo
    # passes the same CGO_LDFLAGS (cmd/mygo/android.go, buildGo).
    local cgo_ldflags="-Wl,-z,max-page-size=16384"
    local -a build_env=(GOOS=android GOARCH="$goarch" CGO_ENABLED=1 CC="$cc" CGO_LDFLAGS="$cgo_ldflags")
    if [ "$goarch" = "arm" ]; then
      build_env+=(GOARM=7)
    fi

    echo "check-compile: android/$abi ($goarch), minimum API $min"
    echo "check-compile: CC=$cc"
    echo "check-compile: compiling ${roots[*]}"
    env "${build_env[@]}" go build "${roots[@]}"

    # Compiling packages does not link them. Produce the c-shared library
    # Gradle puts in jniLibs/<abi>/libmygoapp.so, which is where an undefined
    # symbol or a wrong-target object surfaces.
    echo "check-compile: linking the app c-shared library"
    mkdir -p "$work/$abi"
    env "${build_env[@]}" go build -buildmode=c-shared -o "$work/$abi/libmygoapp.so" .
    verify_shared_object "$work/$abi/libmygoapp.so" "$abi" "$class" "$machine"
    # The linked jniLibs object, not the staging archive, must carry every
    # binding symbol exactly once. ABI drift fails the build here.
    scripts/mobile/check-symbols.sh elf "$work/$abi/libmygoapp.so" "$host/bin/llvm-nm"
  done

  echo "check-compile: ok — android phone build compiles and links"
}

case ${1:-} in
  ios)
    check_ios simulator
    ;;
  ios/simulator)
    check_ios simulator
    ;;
  ios/device)
    check_ios device
    ;;
  android)
    check_android
    ;;
  android/*)
    check_android "${1#android/}"
    ;;
  *)
    echo "usage: $(basename "$0") ios | ios/device | ios/simulator | android[/arm64|/arm|/amd64]" >&2
    exit 2
    ;;
esac
