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
# Usage: scripts/mobile/check-compile.sh ios
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
  sdk=$("$xcrun" --sdk iphonesimulator --show-sdk-path)
  clang=$("$xcrun" --sdk iphonesimulator --find clang)
  # The minimum must be the one the committed configuration declares. Read it
  # from mygo.json so the gate cannot drift from what the app ships.
  local min
  min=$(/usr/bin/plutil -extract ios.minimumSystemVersion raw mygo.json)
  if [ -z "$min" ]; then
    echo "check-compile.sh: mygo.json does not declare ios.minimumSystemVersion" >&2
    exit 1
  fi

  local arch=arm64
  local cc="$clang -target $arch-apple-ios$min-simulator -isysroot $sdk"

  echo "check-compile: ios/$arch, minimum iOS $min"
  echo "check-compile: CC=$cc"

  # The app links libghostty-vt through a staged object. Require the simulator
  # slice and verify it before compiling: a device object (platform 2) in a
  # simulator build fails here, not on the running app.
  echo "check-compile: checking the staged terminal object"
  scripts/mobile/ios-syso.sh simulator --check

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

  # The archive must carry the linked library. Without the staged object the
  # link fails on the undefined ghostty_* symbols that addr.c takes the
  # addresses of; this assertion also catches an object silently dropped.
  if ! /usr/bin/nm -gU "$work/libapp.a" 2>/dev/null | grep '_ghostty_type_json' >/dev/null; then
    echo "check-compile: the app archive does not carry libghostty-vt" >&2
    echo "  the staged object was not linked; check ui/terminal/mobile/ghostty_ios_arm64.syso" >&2
    exit 1
  fi

  echo "check-compile: ok — ios phone build compiles and links"
}

check_android() {
  local want=${1:-}
  # The ABIs and compiler triples are the ones cmd/mygo/android.go ships:
  # androidABIs. Keep them in step with it; armeabi-v7a needs GOARM=7.
  local abis=("arm64-v8a arm64 aarch64-linux-android"
              "armeabi-v7a arm armv7a-linux-androideabi"
              "x86_64 amd64 x86_64-linux-android")

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
  local entry abi goarch triple
  for entry in "${abis[@]}"; do
    read -r abi goarch triple <<<"$entry"
    if [ -z "$want" ] || [ "$want" = "$abi" ] || [ "$want" = "$goarch" ]; then
      selected+=("$entry")
    fi
  done
  if [ ${#selected[@]} -eq 0 ]; then
    echo "check-compile.sh: unknown Android ABI $want: arm64, arm or amd64" >&2
    exit 2
  fi

  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT

  for entry in "${selected[@]}"; do
    read -r abi goarch triple <<<"$entry"
    local cc="$host/bin/${triple}${api}-clang"
    if [ ! -x "$cc" ]; then
      echo "check-compile.sh: $cc does not exist (minSdk $min, clang API $api)" >&2
      exit 1
    fi
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
  done

  echo "check-compile: ok — android phone build compiles and links"
}

case ${1:-} in
  ios)
    check_ios
    ;;
  android)
    check_android
    ;;
  android/*)
    check_android "${1#android/}"
    ;;
  *)
    echo "usage: $(basename "$0") ios | android[/arm64|/arm|/amd64]" >&2
    exit 2
    ;;
esac
