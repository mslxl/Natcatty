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
