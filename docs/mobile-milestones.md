# Mobile milestone notes

Measurements and gate results recorded as each mobile milestone lands. The
contract is `.scratch/mobile-app/spec.md`; the reasoning and build recipes are
in the mobile-support annex. This file records what the builds actually
produced, not targets.

## M1 — libghostty-vt linked into the phone app

Recorded 2026-11-19 while implementing ticket 07 (iOS terminal rendering).
Built from the pinned commit `befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028` with
Zig 0.16.0 and Xcode 27.2 (iPhoneSimulator 27.2 SDK), `ReleaseFast`, stripped.
The iOS slices come from `scripts/mobile/build-ghostty-vt.sh build
--platforms ios`; the object is staged with `scripts/mobile/ios-syso.sh`.

### Sizes

| Artifact | Bytes | MiB | Budget |
| --- | ---: | ---: | --- |
| iOS device library (`ios-arm64` `.a`) | 10,918,320 | 10.41 | — |
| iOS simulator library (`ios-arm64-simulator` `.a`) | 10,822,984 | 10.32 | — |
| iOS device staged object (`ghostty_ios_arm64.syso`, platform 2) | 2,727,640 | 2.60 | ≤ 4 MiB |
| iOS simulator staged object (`ghostty_ios_arm64.syso`, platform 7) | 2,688,832 | 2.56 | ≤ 4 MiB |
| Fallback device object (with `.linker_option "-lz"`) | 2,727,680 | 2.60 | ≤ 4 MiB |
| Fallback simulator object (with `.linker_option "-lz"`) | 2,688,872 | 2.56 | ≤ 4 MiB |

Both staged objects are far under the 4 MiB cap. Android ABIs are recorded
when ticket 08 builds them against the 15 MiB-per-ABI cap.

The sizes above are a local rebuild. `ui/terminal/artifacts.json` still carries
the sizes and sha256 sums recorded when the release assets were produced
(10,913,432 and 10,818,088 bytes); a rebuild differs by under 0.5%, so the
manifest remains the pinned record and the staged object's size is recorded
here.

### Gate results

- `scripts/mobile/ios-syso.sh {device|simulator}` stages exactly one object,
  verifies the slice's `LC_BUILD_VERSION` platform (2 device, 7 simulator),
  the minimum OS from `mygo.json`, and the `ghostty_type_json` symbol.
- Staging a device object and running `scripts/mobile/check-compile.sh ios`
  fails before compiling: the staged object is platform 2, not 7. The app
  never starts with a mixed object.
- With no staged object, `check-compile.sh ios` names the missing file and
  the staging command. Missing linker input is a build failure.
- `scripts/mobile/link-smoke.sh {device|simulator}` links a probe through the
  final-link toolchain, asserts all 93 binding symbols are defined once, and
  proves the fallback: a probe that needs `-lz` fails without the fallback
  object (undefined `_compress2`) and links with it, including when the
  fallback object is reached through a static archive.
- `check-compile.sh ios` builds and links the app as a c-archive with the
  staged object and asserts the archive carries `ghostty_type_json`.
  MyGo's own final Swift link is not modified.
- The library reports `library_version = 0.1.0-dev` in its type manifest, and
  the phone `terminal.Load` asserts it at startup against
  `ui/terminal/mobile.Version`; a mismatch fails the launch.
- The VT corpus (`ui/terminal/corpus.go`) asserts text, style runs (palette,
  bold, 24-bit colour, inverse, background), cursor cell and a steady bar
  cursor over the echo transport.

## M2 — the same terminal on Android

Recorded 2026-11-19 while implementing ticket 08 (Android terminal rendering).
The Android archives are the same pinned commit as M1
(`befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028`), built with Zig 0.16.0 against
NDK 30.0.16248370. `scripts/mobile/android-libs.sh` stages each archive into
the path `ui/terminal/mobile/mobile.go` names for that ABI, after checking the
ELF target, all 93 binding symbols, the reported library version, and a 16 KB
page-aligned probe link.

### Sizes

| ABI | Staged archive | Bytes | MiB |
| --- | --- | ---: | ---: |
| `arm64-v8a` | `lib/android/arm64-v8a/libghostty-vt.a` | 18,810,760 | 17.94 |
| `armeabi-v7a` | `lib/android/armeabi-v7a/libghostty-vt.a` | 11,377,320 | 10.85 |
| `x86_64` | `lib/android/x86_64/libghostty-vt.a` | 18,355,614 | 17.51 |

These are the locally rebuilt archives, a few KiB larger than the sizes in
`ui/terminal/artifacts.json` because the rebuild is not byte-reproducible
across hosts; the manifest stays the pinned record. The archives are static
inputs to the final link, not the bytes shipped in the APK.

### Gate results

- `scripts/mobile/android-libs.sh --check` accepts all three staged archives:
  `ELF64 AArch64`, `ELF32 ARM` and `ELF64 X86-64` respectively, each with the
  93 binding symbols defined once, `0.1.0-dev`, and every probe `LOAD`
  segment aligned to `0x4000`.
- `scripts/mobile/check-compile.sh android` compiles and links the app for all
  three ABIs. The linked `jniLibs/<abi>/libmygoapp.so` — the object Gradle
  ships — is checked after the link: correct ELF class and machine, carries
  `ghostty_type_json`, and every `LOAD` segment is at least 16 KB and a power
  of two.
- The ABI list (`scripts/mobile/android-abis.txt`) includes `armeabi-v7a` and
  is pinned by `internal/archtest`; the compile gate and the staging script
  both read that one list.
- On `emulator-5554` (arm64-v8a, Android 16, API 36),
  `scripts/mobile/android-render.sh` ran the test binaries for the device's
  own ABI: `TestVTCorpus` and `TestVTCorpusThroughEcho` passed (text, style
  runs, cursor cell and steady bar cursor), and
  `TestDiagnosticRunsEchoTransport` passed, logging
  `natcatty_smoke: libghostty-vt 0.1.0-dev, echo text "hello\nred green true
  inv\nblue-bg plain"`.
- Not observed on this host: an x86_64 emulator run and a physical ARM device
  run. The host is arm64 Darwin and the installed emulator has only the
  `darwin-aarch64` QEMU backend and the arm64-v8a system image, so it cannot
  boot an x86_64 AVD; no physical device was attached. The x86_64 library
  still builds, links and passes the 16 KB gate.
