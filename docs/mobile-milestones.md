# Mobile milestone notes

Measurements and gate results recorded as each mobile milestone lands. The
contract is `.scratch/mobile-app/spec.md`; the reasoning and build recipes are
in the mobile-support annex. This file records what the builds actually
produced, not targets.

## M1 — libghostty-vt linked into the phone app

Recorded 2026-11-19 while implementing ticket 07 (iOS terminal rendering);
re-measured 2026-11-21 while reworking ticket 07 after its audit. Built from
the pinned commit `befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028` with
Zig 0.16.0 and Xcode 27.2 (iPhoneSimulator 27.2 SDK), `ReleaseFast`, stripped.
The iOS slices come from `scripts/mobile/build-ghostty-vt.sh build
--platforms ios`; the object is staged with `scripts/mobile/ios-syso.sh`.

### Sizes

| Artifact | Bytes | MiB | Budget |
| --- | ---: | ---: | --- |
| iOS device library (`ios-arm64` `.a`) | 10,918,320 | 10.41 | — |
| iOS simulator library (`ios-arm64-simulator` `.a`) | 10,822,984 | 10.32 | — |
| iOS device staged object (`ghostty_ios_arm64.syso`, platform 2) | 2,728,312 | 2.60 | ≤ 4 MiB |
| iOS simulator staged object (`ghostty_ios_arm64.syso`, platform 7) | 2,689,504 | 2.56 | ≤ 4 MiB |
| Fallback device object (with `.linker_option "-lz"`) | 2,728,352 | 2.60 | ≤ 4 MiB |
| Fallback simulator object (with `.linker_option "-lz"`) | 2,689,544 | 2.56 | ≤ 4 MiB |

Both staged objects are far under the 4 MiB cap. Android ABIs are recorded
when ticket 08 builds them against the 15 MiB-per-ABI cap.

The M1 numbers recorded on 2026-11-19 were device 2,727,640 B and simulator
2,688,832 B (fallbacks 2,727,680 B and 2,688,872 B). The re-measurement is
672 B larger for every object; it was not a silent replacement. The whole
difference is the Mach-O symbol string table (`strsize`), which `ld -r`
derives from the path of the input archive: staging the same sha256-pinned
slice from a shorter checkout path produces a smaller object (a `/tmp`
checkout gave 2,688,856 B for the simulator object, 648 B below the table).
The table's numbers were measured with the slice under the audit checkout
path, so a future comparison must use a like-for-like path.

The sizes above are the local rebuild that `ui/terminal/artifacts.json`
records (10,918,320 and 10,822,984 bytes): the manifest is written by
`scripts/mobile/build-ghostty-vt.sh build --platforms all` from the pinned
source, so it matches the artifact the gate stages. A rebuild on another host
is not byte-reproducible, so CI builds from that same sha256-pinned source
when the release asset is unavailable instead of requiring a foreign copy to
hash identically.

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

These are the locally rebuilt archives and the sizes and sums
`ui/terminal/artifacts.json` records. The build is not byte-reproducible
across hosts, so a rebuild elsewhere can differ by a few KiB; CI therefore
builds from the same sha256-pinned source when the release asset is
unavailable rather than requiring byte-identical copies. The archives are
static inputs to the final link, not the bytes shipped in the APK.

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

## M3 — phone Session lifecycle

Recorded 2026-11-20 while implementing ticket 10 (Session lifecycle) on an
arm64 Darwin host (macOS 27.2.1) with Go 1.27.4 from the pinned `.#default`
nix shell. The rule under test is the phone's: one Tab per Session, one Pane
per Tab, the visible Session resumed, every other Session hibernated with its
transport and replay kept and its live terminal view released.

### Memory with several Sessions open

The measurement is
`TestPhoneHibernationMemoryWithSeveralSessions` in `app`, run with:

```
nix develop .#default --command bash -c \
  'go test ./app/ -run TestPhoneHibernationMemoryWithSeveralSessions -count=1 -v'
```

It opens six real Sessions against six in-process SSH servers (the production
Connection Manager, Transport factory and Session service; the fixture is only
the wire endpoint), attaches each Session once to a real libghostty-vt
terminal, writes 500 lines to it, closes the terminal and hibernates the
Session. It then samples memory with every Session hibernated and again with
one Session resumed as the phone's single live view. One sample is
`runtime.GC()` twice, then `runtime.ReadMemStats` (HeapAlloc and HeapInuse),
plus the process's current resident set from `ps -o rss= -p <pid>`; the Go
heap does not include the terminal emulator's native allocation, which is why
RSS is recorded too. Deltas are against a sample taken before any Session
opened.

Representative run (four consecutive runs were within the ranges shown):

| State | HeapAlloc | HeapInuse | RSS |
| --- | ---: | ---: | ---: |
| No Sessions (baseline) | 3,704,232 B | 6,340,608 B | 45,216 KiB |
| 6 Sessions, all hibernated | 4,852,480 B | 8,044,544 B | 51,632 KiB |
| 5 hibernated + 1 live view | 4,933,088 B | 8,110,080 B | 52,016 KiB |

- Six hibernated Sessions cost +1,148,248 B of Go heap (1.10 MiB, about
  191 KB per Session; four runs: +1,148,248 to +1,153,000 B). That is the
  Session bookkeeping plus each Session's bounded replay (500 lines), not a
  terminal screen.
- The phone's one live terminal adds +80,608 B of Go heap and +65,536 B of
  heap in use over the all-hibernated state (four runs: +80,608 to +84,120 B).
  The rest of a terminal's footprint is native and appears only in RSS.
- RSS rises by 6,416 KiB across the six Sessions and by 384 KiB for the live
  view (four runs: +6,192 to +6,608 KiB, live +384 to +464 KiB). The test
  process also hosts the six SSH servers, so this RSS is an upper bound on the
  phone's cost, not only the client's.

The test asserts the structural part — six open Sessions, zero live views with
all of them hibernated, and exactly one after one is resumed — so a regression
in the rule fails even when the heap is noisy; the numbers above are the
recorded measurement, not a target.

