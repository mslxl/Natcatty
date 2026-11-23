# Building and releasing the phone app

Natcatty ships the same workspace on iOS 17+ and Android 8+ (API 26). A phone
build carries only remote Sessions — SSH, telnet, Mosh and EternalTerminal — in
a terminal that links natcatty's own libghostty-vt fork natively. There is no
local shell, PTY or serial device on a phone, and no webview terminal. The
decisions behind that shape are
[ADR-0014](adr/0014-mobile-remote-only-support.md),
[ADR-0023](adr/0023-pin-libghostty-vt-0.1.0-dev.md) and
[ADR-0024](adr/0024-direct-mobile-distribution.md). Measured artifact sizes and
gate results are in [mobile-milestones.md](mobile-milestones.md); desktop
packaging is in [packaging.md](packaging.md) and the Android signing details are
in [android-release.md](android-release.md).

## Prerequisites

The phone work runs in dedicated Nix shells entered from the checkout root:

| Shell | Use |
| --- | --- |
| `nix develop .#default` | Desktop suite, `go vet`, and the Go tool for the artifact tools. `CGO_ENABLED=0`. |
| `nix develop .#ios` | iOS builds. Uses the host Xcode, sets `DEVELOPER_DIR` to `/Applications/Xcode.app/Contents/Developer`, and clears the nix `SDKROOT`. `CGO_ENABLED=1`. |
| `nix develop .#android` | Android builds. Pins the SDK, NDK `30.0.16248370`, the emulator and the API 36 `arm64-v8a` system image, and points `ANDROID_NDK_HOME` at that NDK. `CGO_ENABLED=1`. |

`go` is available only inside those shells; the fetch and build scripts refuse
to run without it. For iOS you also need an installed simulator runtime;
`scripts/dev/ios-boot.sh` names `xcodebuild -downloadPlatform iOS` when none is
installed. Android AVDs live under `.dev/`, not in the read-only nix store.

Boot and shut down test devices with the dev scripts, and shut them down as soon
as a run finishes:

```sh
nix develop .#ios --command scripts/dev/ios-boot.sh       # prints the UDID
nix develop .#ios --command scripts/dev/ios-stop.sh

nix develop .#android --command scripts/dev/android-boot.sh # prints the serial
nix develop .#android --command scripts/dev/android-stop.sh
```

The Android emulator is headless and logs to `.dev/android-emulator.log`.
`ios-render.sh` boots and shuts down the shared simulator itself unless you pass
`-d UDID`; in that case you own the device's lifetime.

## The pinned native artifacts

The terminal binds one libghostty-vt ABI, pinned in
[`ui/terminal/artifacts.json`](../ui/terminal/artifacts.json):

- Ghostty commit `befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028`
- library version `0.1.0-dev`
- Zig `0.16.0`, NDK `30.0.16248370`, `ReleaseFast`, stripped
- one sha256 and size for each of the five targets: iOS device, iOS simulator,
  and the Android `arm64-v8a`, `armeabi-v7a` and `x86_64` archives

The shipped Android ABIs are the list in
[`scripts/mobile/android-abis.txt`](../scripts/mobile/android-abis.txt). The
compile gate, the staging script and `internal/archtest` all read that one file.
No native binary is committed.

### Fetch the release artifacts

From a clean checkout, fetch the artifacts a job needs:

```sh
nix develop .#ios --command scripts/mobile/fetch-artifacts.sh --platform ios
nix develop .#android --command scripts/mobile/fetch-artifacts.sh --platform android
```

The command verifies each file's sha256 and size against `artifacts.json` and
leaves a matching file alone, so a warm cache costs no network. `--out DIR`
changes the destination (default `build/ghostty-vt`) and `--manifest PATH`
names a different manifest.

The release is the fast path, not the only one. The pinned release
`libghostty-vt-befcdfd2-mobile-1` is unpublished — its assets return 404 — so
when a download fails the command builds the same sha256-pinned Ghostty commit
from source instead. `--build-if-missing=false` requires the release and fails
instead. The fallback is why CI stays green without a published binary, and it
is recorded as a judgement call in [ADR-0023](adr/0023-pin-libghostty-vt-0.1.0-dev.md).

### Build from source

`scripts/mobile/build-ghostty-vt.sh` is the only entry point. It downloads and
sha256-verifies the pinned Ghostty source, obtains Zig 0.16.0, builds the
requested targets, checks every artifact's ELF/Mach-O identity, binding
symbols, reported library version and 16 KB page alignment, and — when every
target is built — writes `ui/terminal/artifacts.json`.

```sh
# iOS slices on macOS with Xcode.
nix develop .#ios --command scripts/mobile/build-ghostty-vt.sh \
  build --platforms ios --no-manifest

# Android archives with the pinned NDK.
nix develop .#android --command scripts/mobile/build-ghostty-vt.sh \
  build --platforms android --no-manifest

# Every target on a host with both Xcode and the NDK. This is the only form
# that writes the manifest, because the pin covers all five targets.
nix develop .#android --command scripts/mobile/build-ghostty-vt.sh build
```

Only rebuild the manifest when you intend to repin the terminal. Otherwise use
`--no-manifest`, as the release workflow does. `--ndk PATH`, `--zig PATH`,
`--out DIR`, `--cache DIR`, `--manifest PATH` and `--release URL` are the
remaining flags.

To re-check an existing output directory against the committed pin:

```sh
nix develop .#android --command scripts/mobile/build-ghostty-vt.sh verify
nix develop .#android --command scripts/mobile/build-ghostty-vt.sh manifest
```

### Stage the artifact for cgo

Staging puts one exactly-matching artifact where the phone packages name it.

```sh
# iOS: one object for the target. Writes ui/terminal/mobile/ghostty_ios_arm64.syso.
scripts/mobile/ios-syso.sh simulator
scripts/mobile/ios-syso.sh device
scripts/mobile/ios-syso.sh simulator --check   # verify only

# Android: copies each archive to ui/terminal/mobile/lib/android/<abi>/.
scripts/mobile/android-libs.sh
scripts/mobile/android-libs.sh --abi arm64-v8a
scripts/mobile/android-libs.sh --check         # verify only
```

A device and a simulator slice share the file name and architecture, so exactly
one is staged. `ios-syso.sh` reads the slice's `LC_BUILD_VERSION` platform and
refuses a mismatch (device is 2, simulator is 7). `android-libs.sh` refuses an
archive that is the wrong ELF target, is missing or duplicating a binding
symbol, reports a different library version, or cannot link with 16 KB pages.

## Build and test

Run the gates on every change to the phone graph or the terminal:

```sh
nix develop .#default --command bash -c 'go test ./... -count=1 -p 1'
nix develop .#default --command bash -c 'go vet ./...'

nix develop .#default --command scripts/mobile/check-deps.sh
```

`check-deps.sh` walks the phone composition root under each phone `GOOS` with
`CGO_ENABLED=1` and fails when a desktop-only package or a MyGo plugin manifest
is reachable.

Compile and link the app with the same target toolchain MyGo uses:

```sh
nix develop .#ios --command scripts/mobile/check-compile.sh ios/simulator
nix develop .#ios --command scripts/mobile/check-compile.sh ios/device

nix develop .#android --command scripts/mobile/check-compile.sh android
nix develop .#android --command scripts/mobile/check-compile.sh android/arm64-v8a
nix develop .#android --command scripts/mobile/check-compile.sh android/armeabi-v7a
nix develop .#android --command scripts/mobile/check-compile.sh android/x86_64
```

The `android/` targets also accept the Go names `arm64`, `arm` and `amd64`. The
gate stages and verifies the matching artifact before compiling, links the
c-archive (iOS) or the c-shared `libmygoapp.so` (Android), and checks the linked
artifact against `ui/terminal/internal/vt/symbols.txt`.

The separate link gates isolate the native link:

```sh
scripts/mobile/link-smoke.sh simulator
scripts/mobile/link-smoke.sh device
scripts/mobile/android-link-smoke.sh
scripts/mobile/android-link-smoke.sh --abi arm64-v8a

scripts/mobile/check-symbols.sh macho ARTIFACT
scripts/mobile/check-symbols.sh elf ARTIFACT
```

### Build an app

```sh
# iOS simulator app. Run inside .#ios after staging the simulator object.
nix develop .#ios --command bash -c \
  'scripts/mobile/ios-syso.sh simulator && go tool mygo build -platform ios/simulator -o dist'

# Android universal APK (debug-signed when no keystore is configured).
nix develop .#android --command bash -c \
  'scripts/mobile/fetch-artifacts.sh --platform android &&
   scripts/mobile/android-libs.sh &&
   go tool mygo build -platform android -o dist'

# iOS device package. Needs a signing identity and profile in mygo.json.
nix develop .#ios --command go tool mygo build -platform ios -o dist
```

The simulator output is `dist/ios-simulator/Natcatty.app`; CI checks
`vtool -show-build` reports `platform IOSSIMULATOR` before zipping it. The
Android output is under `dist/android/`; a release renames it with
`nu scripts/release.nu rename-assets --dir dist --version <version>`. Without a
keystore the APK is signed with Gradle's debug key and is never published.

Run the device smoke when the change touches platform behaviour:

```sh
# Boots the shared simulator and shuts it down when done.
nix develop .#ios --command bash -c \
  'scripts/mobile/ios-syso.sh simulator && scripts/mobile/ios-render.sh'

# Uses the connected device or emulator for its own ABI.
nix develop .#android --command scripts/mobile/android-render.sh
```

`ios-render.sh` runs the smoke build and waits for the diagnostic page to log
the echo corpus. `android-render.sh` pushes and runs `TestVTCorpus` and
`TestDiagnosticRunsEchoTransport` for the device's ABI.

## Vault Sync OAuth clients on phones

The phone configures Google Drive and OneDrive on Settings → Vault sync.
Unlike the desktop, it does not listen on `127.0.0.1`: iOS and Android suspend
the app while the system browser is open, so the providers redirect to the
app's custom scheme and the OS hands the URL back to the running app.

`mygo.json` declares `netcatty` in `urlSchemes`. MyGo turns that into
`CFBundleURLTypes` in the iOS bundle and a VIEW intent-filter in the Android
manifest, so both platforms deliver
`netcatty://oauth/callback` to `App.OnOpenURL`. The phone root routes that URL
to the sync engine (`HandleOpenURL`); ssh:// links still go to the Host
router. The waiting window pauses while the app is suspended, so switching to
the browser never spends the user's foreground budget.

Before an acceptance run, register a mobile OAuth client for each provider:

- **Google Drive.** Create an iOS and/or Android OAuth client (not a Desktop
  client) in the Google Cloud console for the same project as the Drive API.
  The iOS client is bound to the bundle identifier
  `com.mslxl.natcatty`; the Android client is bound to the package name
  `com.mslxl.natcatty` and the signing certificate SHA-1 (debug and release
  keystores are separate clients). The authorized redirect must be a custom
  scheme the app declares. Google's mobile client types may require their
  reversed-client-id scheme (`com.googleusercontent.apps.<client-id>:/…`)
  instead of the generic `netcatty://oauth/callback`; in that case set
  `NATCATTY_OAUTH_REDIRECT_URI` to the reversed-client-id URI, add its scheme
  to `urlSchemes` in `mygo.json` for that build, and register the same URI in
  the console. Pass the client id to the device as
  `NATCATTY_GDRIVE_CLIENT_ID`; the app never ships a client secret.
- **OneDrive.** Register a mobile application in Microsoft Entra ID. A phone
  redirect is configured by the iOS bundle identifier or by the Android
  package name plus its signing certificate hash, not by a loopback port.
  Add `netcatty://oauth/callback` as a mobile redirect URI (or the override
  described below) and leave the client public (no secret). Pass the
  application (client) id as `NATCATTY_ONEDRIVE_CLIENT_ID`.

If an acceptance run uses a different callback, set
`NATCATTY_OAUTH_REDIRECT_URI` and declare the same scheme in `mygo.json`; the
two must agree or the OS will not deliver the redirect. Fakes for endpoint
overrides are the same `NATCATTY_GDRIVE_*` / `NATCATTY_ONEDRIVE_*` variables
the desktop uses.

Acceptance of the callback itself is a real-device check: the simulator and
emulator can exercise the scheme registration, but a browser installed on a
real phone, the system's suspension of the app, and the provider's acceptance
of the bundle/package-bound redirect are only proven on device. Record the
result per platform in the device matrix; an unverified platform keeps the
provider hidden rather than showing a button that cannot finish.

## CI

`.github/workflows/phone-ci.yml` runs on every pull request and on pushes to
`main` whose paths are not docs, Markdown, `LICENSE` or `NOTICE`. It also runs
nightly at 05:41 UTC, on `workflow_dispatch`, and as `workflow_call` from the
release workflow.

| Job | What it proves |
| --- | --- |
| `phone-deps` | The phone graph reaches no desktop-only package or plugin manifest. |
| `compile-ios` | The simulator and device c-archives compile and link with the pinned object. |
| `compile-android` | Every ABI in `android-abis.txt` compiles and links; `libmygoapp.so` is the right ELF class and machine and has 16 KB `LOAD` segments. |
| `link-smoke-ios` / `link-smoke-android` | Every binding symbol is defined exactly once in the linked object, and the iOS fallback `-lz` path works. |
| `ios-simulator-artifact` | Builds `dist/ios-simulator/Natcatty.app`, verifies it is an `IOSSIMULATOR` bundle, and uploads `natcatty-ios-simulator-<sha>.zip`. |
| `ios-device-package` | Only with an Apple account; builds and uploads an `.ipa`. Skipped otherwise. |
| `smoke-ios` / `smoke-android` | Device smoke on phone-code pull requests, nightly, and before a tag. |

The expensive smoke jobs are selected by the `changes` job. Compile, link and
dependency jobs run on every code pull request, so a phone build is never
skipped because a path filter guessed wrong. Because the native release is
unpublished, the fetch step builds Ghostty from the pinned source in CI.

## Releasing

The version lives in `mygo.json`. The Android `versionCode` is computed from it
by `internal/tools/androidversion`; the validate job fails unless the new code is
greater than the previous release tag's. A pushed `v*` tag publishes a GitHub
release; `workflow_dispatch` with a `tag` input validates and builds as a dry
run and publishes nothing.

`.github/workflows/release.yml` calls `phone-ci.yml` as `phone-smoke` before it
builds, so a broken phone build cannot reach a release tag.

### Android APK

The `build-android` job:

1. installs `platforms;android-36`, `build-tools;35.0.0` and
   `ndk;30.0.16248370`;
2. builds the pinned Android archives with
   `scripts/mobile/build-ghostty-vt.sh build --platforms android --ndk <ndk> --no-manifest`;
3. stages them with `scripts/mobile/android-libs.sh`;
4. decodes the keystore from `ANDROID_KEYSTORE_BASE64` into `$RUNNER_TEMP`
   (never the checkout) and writes the computed version code into the disposable
   manifest with `nu scripts/release.nu android-manifest`;
5. builds one universal APK with `go tool mygo build -platform android -o dist`,
   deletes the Play `.aab`, and renames the APK;
6. verifies the signature and certificate with `apksigner verify --print-certs`,
   the version code with `apkanalyzer manifest version-code`, and every ABI's
   16 KB `LOAD` alignment with `zipalign -c -P 16 4` and `llvm-readelf`.

Required repository secrets and variable:

| Name | Kind | Meaning |
| --- | --- | --- |
| `ANDROID_KEYSTORE_BASE64` | secret | The PKCS12/JKS keystore, base64-encoded. |
| `ANDROID_KEYSTORE_PASSWORD` | secret | The keystore secret; MyGo receives it as `MYGO_ANDROID_KEYSTORE_PASSWORD`. |
| `ANDROID_KEY_ALIAS` | secret | The key alias in the keystore. |
| `ANDROID_KEY_PASSWORD` | secret, optional | The key secret. Leave unset for PKCS12; MyGo then reuses the keystore secret. |
| `ANDROID_SIGNING_CERT_SHA256` | repository variable | The expected `Signer #1 certificate SHA-256 digest`. The job fails on any other certificate. |

The job fails closed when a required value is missing. Keystore generation,
backup, the unrecoverable loss of the key and the local verification commands
are in [android-release.md](android-release.md).

### iOS

CI uploads the simulator app as the `natcatty-ios-simulator-<sha>` artifact. A
device package is produced only when the `APPLE_DEVELOPMENT_TEAM` repository
variable exists, together with `APPLE_CERTIFICATE_P12`,
`APPLE_CERTIFICATE_PASSWORD` and `APPLE_PROVISIONING_PROFILE`. Without them the
job is skipped, so CI never uploads an unsigned device package. There is no App
Store or TestFlight step.

### Handing out a build

Distribution is direct: the Android APK is installed from the release file
(`adb install` or opening it on the device), and there is no store account or
store update channel. A newer release installs over the installed app. The
desktop, Android and remaining release assets are described in
[packaging.md](packaging.md). The release body appends
[`docs/releases/android-apk.md`](releases/android-apk.md) on every release and
[`docs/releases/identifier-migration.md`](releases/identifier-migration.md) only
on the first release whose identifier differs from the previous tag's.

## Troubleshooting a failed phone build

Start by reading the first failing gate, not the last line of the log. Every
gate names the artifact or package it rejected.

### `go is not on PATH; run inside nix develop .#ios or .#android`

The artifact scripts and gates need the pinned Go. Re-run the command inside
`nix develop .#default`, `.#ios` or `.#android` as appropriate. The phone
compile gates also need the platform toolchain: Xcode for iOS, the pinned NDK
for Android.

### iOS: no staged object, or the wrong slice

- `ios-syso.sh: no simulator object at ui/terminal/mobile/ghostty_ios_arm64.syso`
  with `stage it first`: run
  `scripts/mobile/ios-syso.sh simulator` (or `device`).
- `is built for platform 2, not 7 (simulator)` (or the reverse): the staged
  object is the other target's slice. Stage the matching one; `check-compile.sh`
  and `link-smoke.sh` call `ios-syso.sh --check` before compiling.
- `pinned simulator slice missing: build/ghostty-vt/...`: fetch or build the
  artifact first with `scripts/mobile/fetch-artifacts.sh --platform ios`.

### Android: missing, wrong or misaligned archive

- `android-libs.sh: no Android NDK`: run inside `nix develop .#android`, or set
  `ANDROID_NDK_HOME` to the pinned NDK.
- `not an ELF archive`, `is not ELF64 AArch64`, `does not contain the version
  string`, `defines <symbol> N times, want once`: the archive is not the pinned
  one. Re-fetch it and stage it again; do not mix archives between ABIs.
- `the 16 KB probe has no LOAD segments` or `LOAD segment alignment is N
  bytes, want at least 16384`: the archive was built without 16 KB pages.
  Rebuild it from the pinned source and restage.

### Symbol or version mismatch

- `check-symbols: <artifact> is missing the pinned symbol <name>` or
  `ABI drift in <artifact>`: the linked library is not the pinned ABI. Restage
  the artifact named by `ui/terminal/artifacts.json`; a real upgrade is a binding
  port (see [ADR-0023](adr/0023-pin-libghostty-vt-0.1.0-dev.md)), not a version
  bump.
- `terminal: linked libghostty-vt reports "...", want "0.1.0-dev"` at launch:
  the app linked a library whose reported version differs from
  `ui/terminal/mobile.Version`. Rebuild and restage the pinned artifact.

### The phone graph reaches desktop code

`check-deps: <goos>/<goarch>: desktop-only <package> is in the phone graph` (or
`plugin manifest ... is in the phone graph`) names the import that leaked.
Remove the import, or move the code behind a build tag that excludes phones.
Do not treat the gate as advisory: the phone build would link that package.

### Fetch fails

`fetch: ... 404 ...` followed by `building the same pinned commit from source`
is the expected path while the release is unpublished. If the source build then
fails, check that the host has network access and the target toolchain (Xcode
for iOS, the NDK for Android). `--build-if-missing=false` turns the fallback off
when you specifically want to test the release path.

### No simulator runtime

`scripts/dev/ios-boot.sh` exits with `no iOS simulator runtime installed` when
none is available. Install one with `xcodebuild -downloadPlatform iOS`, then
boot again.

### The emulator does not boot

Read `.dev/android-emulator.log`, stop any stale emulator with
`scripts/dev/android-stop.sh`, then run `scripts/dev/android-boot.sh` again.
`android-boot.sh` waits for `sys.boot_completed=1` for up to 180 seconds and
exits non-zero rather than leaving a half-booted device.

### A release fails

- `secret ANDROID_KEYSTORE_BASE64 is not set` (or `ANDROID_KEYSTORE_PASSWORD`
  or `ANDROID_KEY_ALIAS`):
  configure the repository secrets in the table above. The job fails closed.
- `repository variable ANDROID_SIGNING_CERT_SHA256 is not set`, or the APK
  certificate is not the expected one: set the variable to the release
  certificate's SHA-256 digest; a different digest means the wrong keystore or
  a stale variable.
- `APK versionCode <n> is not the computed <m>`, or the validate job's version
  code is not greater than the previous tag's: fix the version in `mygo.json`;
  a release cannot reuse or lower a code.
- `mygo.json commits an android.keystore path` or a committed keystore file:
  remove the signing material from the checkout; the release job supplies it
  from secrets.
- `ios-device-package` is skipped: no `APPLE_DEVELOPMENT_TEAM`; this is
  expected until the project has a paid Apple developer account.

### Keyboard and IME notes

The terminal implements the framework's text-input client itself, one
implementation for desktop and phones. Tapping a focused terminal re-requests
the soft keyboard; IME composition is displayed as marked text in the terminal
and commits once; Enter and Tab arrive as keys. The client's document is a
viewport snapshot of the visible grid, so scrollback is not part of it. On
desktop the shared path preserves held-key repeat; `OptionAsAlt` is never
enabled. Known v1 gaps are iOS hardware-keyboard auto-repeat and two-finger
scrollback. If the keyboard does not return after a manual dismissal on a real
device, that is an iOS-host issue, not a routing bug; stop and ask before
vendoring a framework change.
