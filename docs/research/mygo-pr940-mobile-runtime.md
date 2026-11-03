# MyGo PR 940: the iOS and Android runtime, and what it means for mosh and Eternal Terminal

Status: research / decision input  
Date: 2026-11-03  
Ticket: "MyGo PR 940 mobile runtime constraints"  
Scope: what a MyGo app gets on iOS and Android under
[PR #940](https://github.com/egoist/mygo/pull/940), and which platform or
MyGo constraints a pure-Go mosh client or an in-process Apache-2.0 Eternal
Terminal (ET) library would hit.

Sources verified against:

- MyGo PR **#940**, branch `mobile`, head
  **`7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a`**, base `main`. The PR was
  open and unmerged when checked on 2026-11-03; every MyGo citation below is
  pinned to that commit. [PR940]
- Go **1.27.3** source and documentation, the version named by the PR's
  `go.mod`. [M-GOMOD]
- purego **v0.11.1**, the version named by the PR's `go.mod`. [PUREGO]
- Apple's current iOS documentation and the locally installed
  **iPhoneOS27.2.sdk**; Android's current platform documentation, retrieved
  2026-11-03. These are living documents; section names and quoted text are
  identified in the source list.
- mosh at **`decd9b705eb81626f694335b8d5940538beb06da`** and Eternal Terminal
  at **`abaecea985cc98f90da0756211fedf4d78354a7c`**. [MOSH-README] [ET-README]

No MyGo mobile app was built or run for this note: the machine has Xcode but
no Android NDK, and no simulator/device run was performed. Platform behavior
is therefore taken from Apple/Android documentation and PR source, not from
observed device traces. Local experiments that were run are marked as such.

---

## Answers

1. **A MyGo app is a guest in a native host, not a normal desktop Go
   process.** On iOS the Go main package is a `c-archive`
   (`GOOS=ios`, `GOARCH=arm64`, `CGO_ENABLED=1`) linked into a Swift/UIKit
   host by `swiftc`; on Android it is a `c-shared` library
   (`libmygoapp.so`) per ABI, loaded into a Kotlin host process. The Go
   runtime runs `main.main` on a Go-owned, `LockOSThread`-ed thread; the
   host owns the UI/main thread, and MyGo marshals calls that need it.
   [M-IOS] [M-ANDROID] [M-START] [M-GLUE-IOS] [M-GLUE-ANDROID]

2. **Go's standard library largely works, but the mobile sandbox removes
   process spawning and persistent background execution.** TCP/UDP sockets,
   DNS, files in the app container, goroutines, timers, and cgo-linked code
   work; `os/exec` of bundled sidecars does not, and iOS suspends the
   process in the background while Android restricts background services and suspends
   network access under Doze/App Standby. [APPLE-BG] [ANDROID-BG]
   [ANDROID-DOZE]

3. **Build tags need explicit mobile exclusions.** Go treats `android` as
   also matching `linux`, `ios` as also matching `darwin`, and both as
   `unix`; `CGO_ENABLED=1` also sets `cgo`. Code such as PTY or desktop
   process management must therefore use `!(darwin && !ios)` /
   `!(linux && !android)`, not merely `darwin`/`linux`. [GO-TAGS]
   [GO-SYSLIST]

4. **MyGo's network API is status-only, not a roaming/interface API.**
   `mygo.Network.Status` exposes `Online` and `Metered`, and
   `OnChanged` fires when those change. There is no interface list, no
   local-IP-change event, no socket binding per network, and no guarantee
   that a change event precedes a broken socket. A mosh/ET client must
   detect failures itself and rebind. [M-NETWORK] [M-SERVICES]

5. **iOS background execution is the hard blocker for a background mosh
   session.** UIKit suspends an app shortly after it enters the background,
   and Apple says apps "don't normally receive any extra execution time"
   there. `BGTaskScheduler` tasks are opportunistic and time-limited;
   MyGo's own background plugin documents roughly 30 seconds for a short
   task, minutes for a long one, and a 15-minute minimum repeat interval.
   There is no persistent-socket background mode that App Review accepts
   for an SSH/mosh client. [APPLE-BG] [APPLE-BGTASKS] [M-BACKGROUND]

6. **Android background execution is less absolute but still constrained.**
   A background app has a "window of several minutes" before its background
   services are stopped; a foreground service is the supported way to keep
   work alive, but Android 12+ restricts starting one from the background,
   Android 14+ requires a declared service type, and Android 15 gives
   `dataSync` a 6-hour-per-24-hour budget. Doze and App Standby suspend or
   defer network access. [ANDROID-BG] [ANDROID-FGS] [ANDROID-FGS-START]
   [ANDROID-15] [ANDROID-DOZE]

7. **Local-network access is permissioned on both platforms, but only when
   the peer is on the LAN.** Apple's local-network privacy applies to all
   networking APIs, including BSD sockets and UDP unicast, and iOS denies
   the operation from an app in the background while the privilege is
   undetermined; multicast needs an additional entitlement. Android 17
   introduces `ACCESS_LOCAL_NETWORK` for apps targeting API 37+. A remote
   SSH/mosh server on the public Internet is unaffected; a LAN server is
   not. [APPLE-TN3179] [ANDROID-17]

8. **A pure-Go mosh client is a reasonable architectural fit, provided the
   app owns the terminal emulator and accepts foreground-only operation.**
   mosh uses UDP to a remote `mosh-server`, supports roaming and
   intermittent connectivity, and the server keeps the session after the
   client disappears (by default indefinitely). No local process is
   needed: the client can use the app's SSH session to start
   `mosh-server`, read the `MOSH CONNECT` port and key line, then speak
   mosh's UDP protocol.
   MyGo supplies UDP sockets, crypto-capable Go, and lifecycle events, but
   not a mobile terminal emulator, network-change events, or background
   sockets. [MOSH-README] [MOSH-MAN] [M-TERM-PLUGIN]

9. **An in-process ET library is technically plausible but not supported by
   MyGo's native pipeline as it stands.** ET is C++20, builds static
   libraries (`et-lib`, `TerminalCommon`, `libvterm`) through CMake, depends
   on protobuf, OpenSSL, libsodium, zlib, sentry, and others, and exposes a
   C++ `TerminalClient`/`Console` API with no stable C ABI. MyGo compiles
   one dynamic library per plugin from source with `clang`, does not run
   CMake/protoc, does not package prebuilt static libraries per ABI, and
   its clang invocation does not link libc++ automatically (confirmed on
   iOS; Android unverified). A C++ shim and substantial app-side build
   work would be needed.
   [ET-CMAKE] [ET-VCPKG] [ET-CLIENT] [ET-CONSOLE] [M-NATIVES]

10. **Both designs must treat the session as server-side and resumable.**
    iOS can kill a suspended app; Android can stop or kill a background
    process. mosh and ET both keep the remote session alive and reconnect,
    which is exactly the right model. The mobile app should persist
    credentials/session state locally, reconnect on foreground, and never
    assume a socket survives backgrounding. [MOSH-README] [ET-README]
    [M-MOBILE]

11. **App Store and Play review are product constraints, not just technical
    ones.** App Review 2.5.2 requires apps to be self-contained and bars
    downloading/executing code that changes functionality; App Review 2.5.4
    restricts background services to their intended purposes. Apple DTS
    states plainly that iOS apps are not allowed to spawn child processes.
    On Android, the foreground-service type and Play Console declaration
    are reviewed. Neither platform accepts "keep a socket alive with an
    audio/VoIP background mode" for an SSH client. [APPLE-REVIEW]
    [APPLE-DTS] [ANDROID-FGS]

12. **MyGo is missing several pieces a production mobile SSH client would
    otherwise expect.** The list includes mobile PTY/process spawning, a
    mobile terminal widget, persistent background sockets, a foreground
    service API, iOS Network Extension/app-extension targets, network
    interface/roaming events, local-network permission requests, a
    configurable Android `targetSdk`, prebuilt/static native libraries, and
    C++/CMake/protoc integration. Each needs app-side plugins or changes to
    MyGo. See "What MyGo does not provide" below.

---

## 1. What PR 940 builds

PR 940 adds one `internal/mobile` backend used by both phone hosts. The
PR's `docs/mobile.md` describes the model: the same Go app runs on iPhone,
iPad, and Android; web content uses the system web view, native UI draws
with Metal on iOS and OpenGL ES on Android, and the app's Go code, bound
services, and events are the same. [M-MOBILE] [M-ARCH]

### iOS

- Build mode: `-buildmode=c-archive`, `GOOS=ios`, `GOARCH=arm64`,
  `CGO_ENABLED=1`, with Apple clang as `CC`. The resulting archive is
  linked by `swiftc` with the Swift host object and system frameworks
  (UIKit, WebKit, Metal, QuartzCore, CoreText, CoreGraphics, Security,
  CoreFoundation, UserNotifications, UniformTypeIdentifiers, Network,
  AVFoundation), plus `-lresolv`. [M-IOS]
- The exported C entry points are `mygo_start`, `mygo_event`,
  `mygo_drain`, and `mygo_free`. [M-GLUE-IOS]
- `start()` redirects stdout/stderr to the system log and runs the app's
  `main.main` via `//go:linkname` on a goroutine locked to its own OS
  thread. The host UI thread is separate. [M-START]
- `mygo build -platform ios` produces a signed `.ipa`; the CLI builds the
  Swift host, icon, bundle, privacy manifest, and signature without an
  Xcode project. A Mac with Xcode and an Apple developer team/profile are
  required. [M-MOBILE] [M-IOS]

### Android

- Build mode: `-buildmode=c-shared`, `GOOS=android`, `CGO_ENABLED=1`,
  NDK clang, with `CGO_LDFLAGS=-Wl,-z,max-page-size=16384`. Output is
  `jniLibs/<abi>/libmygoapp.so`. Supported ABIs are `arm64-v8a`,
  `armeabi-v7a`, and `x86_64`. [M-ANDROID]
- The Kotlin host loads the library into the app process; JNI calls reach
  the Go backend. `mygo_start_android` starts the Go main function the
  same way as iOS. [M-GLUE-ANDROID] [M-START]
- `mygo build -platform android` drives a generated Gradle project and
  produces `.apk` and `.aab`. [M-MOBILE]

### Threading and lifecycle contract

- The Go backend keeps its own event loop and main goroutine. Calls from
  Go that need the UI are serialized onto the host's UI thread; the host
  can call into Go from the UI thread and wait for a reply. [M-BACKEND]
  [M-TRANSPORT]
- The app never quits itself on a phone. `Backend.Quit` stops the Go loop
  but leaves the process alive; "phones don't quit apps: the system ends
  them." [M-BACKEND]
- Going to the background sends `PowerEvent("suspend")`; returning sends
  `"resume"` and `App.OnActivate`. Frames stop while backgrounded. The
  system may terminate the app; `App.OnSaveState`/`App.RestoredState`
  preserve selected state. [M-MOBILE] [M-EVENTS] [M-BACKEND]
- On iOS the host waits for Go to stop drawing before the app enters the
  background; on Android `onStop` sends the background event but the
  process is not killed. [M-SWIFT-APP] [M-KOTLIN]

### Build tags

Go's own build-constraint matcher makes these tags true together
[GO-TAGS] [GO-SYSLIST]:

| GOOS | Also matches | `unix`? | `cgo`? |
|---|---|---|---|
| `ios` | `darwin` | yes | yes, because `CGO_ENABLED=1` |
| `android` | `linux` | yes | yes, because `CGO_ENABLED=1` |

Consequences for Natcatty:

- `_darwin.go` and `_linux.go` files are included on iOS and Android
  respectively unless they explicitly exclude the mobile GOOS.
- `//go:build unix` is true on both phones.
- PTY and desktop process code must be excluded with tags like
  `!(darwin && !ios)` and `!(linux && !android)`, exactly as MyGo's
  terminal plugin does. [M-TERM-PTY]

### What cgo/Go build modes require

Go documents `c-archive` and `c-shared` as building a main package into a
C archive/shared library whose only callable symbols are those exported
with cgo `//export` comments. They require exactly one main package.
[GO-MODES] The Go toolchain also applies `-shared` code generation for
`c-archive` on `darwin`/`ios` arm64 and for `c-shared` on `linux`/
`android`. [GO-INIT] These modes are why both phone targets link with
cgo enabled.

The network resolver is relevant on phones: Go's `net` package prefers the
cgo resolver on `darwin`, `ios`, and `android`; the comments explain that
Darwin can show dialogs for its own DNS requests and that Android DNS
requests do not work without libc. [GO-NET]

---

## 2. Networking: sockets, roaming, background, permissions

### What works

- Go `net` TCP and UDP sockets work in both hosts because the Go runtime
  is inside a normal app process with cgo enabled. MyGo itself uses BSD
  sockets and platform network status; a mosh UDP socket or an ET TCP
  socket is not special.
- `mygo.Network.Status` reports `{Online, Metered}` and `OnChanged` fires
  on changes. iOS uses `NWPathMonitor` (`online` = path satisfied,
  `metered` = expensive or constrained); Android uses
  `ConnectivityManager` capabilities. [M-NETWORK] [M-SWIFT-HOST]
  [M-KOTLIN]
- DNS resolution uses the platform resolver (cgo) as noted above.

### What is missing for roaming

`mygo.Network` exposes only online/metered. It does not expose:

- the active interface or its addresses;
- an event for an IP address change or interface switch;
- a way to bind a socket to a specific Android `Network` object;
- a guarantee that `OnChanged` fires before the old socket fails.

A mobile mosh/ET client must therefore treat socket errors and timeouts as
the signal to reconnect, recreate sockets, and re-resolve. mosh's protocol
is designed for this; ET also reconnects, but the app still has to decide
when to redial. [MOSH-README] [ET-README]

### iOS background and networking

Apple's background guide says UIKit moves an app to the background and
then suspends it, and:

> "Apps don't normally receive any extra execution time after they enter
> the background."

`BGTaskScheduler` provides short refresh tasks and longer processing tasks,
but scheduling is the system's choice and tasks can be interrupted.
[APPLE-BG] [APPLE-BGTASKS] MyGo's `background` plugin states that iOS
gives a task "about 30 seconds, or minutes with Long", and that `Every` is
at least 15 minutes on either system. That is a MyGo/estimate statement,
not an Apple guarantee. [M-BACKGROUND]

Therefore:

- There is no supported way for a normal iOS SSH/mosh client to keep a
  UDP or TCP socket alive indefinitely in the background.
- `UIBackgroundModes` values such as `audio`, `voip`, or
  `remote-notification` do not legitimately cover an interactive shell;
  App Review 2.5.4 limits background services to their intended purposes.
  [APPLE-REVIEW] [M-MOBILE]
- On resume, the app must assume the socket is dead and reconnect. mosh
  and ET both support this server-side session model.

### Apple local-network privacy

Apple TN3179 states that local network privacy applies to operations on a
"local network", defined as an IP network on a broadcast-capable interface
(Wi-Fi/Ethernet, not cellular or VPN), and that the checks are implemented
"deep in the networking stack" and apply to all networking APIs,
"including ... BSD Sockets". Its table lists sending/receiving UDP unicast
as a local-network operation. It also says:

- an app in the background that performs a local-network operation while
  its privilege is undetermined is denied without an alert;
- multicast/broadcast on iOS additionally requires the multicast
  entitlement;
- the simulator does not implement local-network privacy.

[APPLE-TN3179]

Natcatty implications:

- A remote host on the public Internet does not need local-network
  permission.
- A host on the user's LAN does, as does mosh's UDP traffic to that host.
- MyGo's release Info.plist does **not** add `NSLocalNetworkUsageDescription`;
  it adds it only for debug device builds. The app must add it through
  `ios.infoPlist` if it wants LAN hosts, and the user must approve the
  prompt. [M-IOS]
- MyGo does not request the multicast entitlement. A mosh client using
  only unicast does not need it; LAN discovery would.

### iOS Network Extension

Apple's NetworkExtension framework supports VPN configurations, packet
tunnels, DNS proxies, content filters, and related services, but the
features are implemented as app extensions and some require special
entitlements/approval. [APPLE-NE] [APPLE-NE-ENT] MyGo's plugin schema has
iOS sources, class, frameworks, Info.plist keys, and entitlements, but no
app-extension target or embedded `.appex`; the iOS build creates one host
bundle and signs it. [M-PLUGINS] [M-IOS] A Network Extension would
therefore require forking/extending MyGo's Xcode-less build, not just a
plugin declaration. This note treats that as out of scope for the first
mobile version.

### Android background, Doze, and foreground services

Android 8 background limits: when an app goes to the background it has a
"window of several minutes" in which it may still create/use services;
after that the system stops background services. A foreground service is
the exception, but Android 12+ restricts starting a foreground service
from the background, with specific exemptions. [ANDROID-BG]
[ANDROID-FGS-START]

Android 14+ requires every foreground service to declare an appropriate
type and permission. The relevant types are not a clean fit for an
interactive SSH/mosh session [ANDROID-FGS]:

| Type | Description | Constraint relevant here |
|---|---|---|
| `dataSync` | data upload/download, backup, fetch, network transfer | Android 15: 6 hours per 24 hours for the app's `dataSync` services, then `onTimeout`; timer resets when the app comes foreground. [ANDROID-15] |
| `connectedDevice` | interactions with external devices over Bluetooth/NFC/IR/USB/network | Requires one of `CHANGE_NETWORK_STATE`, `CHANGE_WIFI_STATE`, `CHANGE_WIFI_MULTICAST_STATE`, `NFC`, `TRANSMIT_IR`, a Bluetooth runtime permission, or USB permission; arguably a poor semantic fit for a remote SSH server. |
| `specialUse` | any valid use not covered by another type | Must declare a `<property>` subtype; the values and use case are reviewed in Play Console. |
| `shortService` | critical work that cannot be postponed | About 3 minutes; not suitable for a shell. |

Doze and App Standby: Doze "suspends network access", ignores wake locks,
and defers jobs, syncs, and standard alarms; App Standby defers background
network access and allows idle apps network access roughly once a day.
WorkManager is subject to the same restrictions and has a 15-minute
minimum periodic interval. [ANDROID-DOZE] [ANDROID-WORK]

A foreground service can keep the app process and a socket alive for a
while, but it is not a guarantee against Doze network suspension, and the
type/Play declaration must be defensible. This is an app-side Android
plugin (`application` manifest fragment + Kotlin service), not a MyGo
feature: the plugin schema supports it, but MyGo exposes no foreground
service API. [M-PLUGINS]

### Android local network (Android 17)

Android 17 introduces the `ACCESS_LOCAL_NETWORK` runtime permission; apps
targeting API 37+ must request it to discover/connect to LAN devices.
Enforcement is mandatory for targetSdk 37+, after an opt-in period in
Android 16. [ANDROID-17] MyGo hardcodes `targetSdk = 36` in its generated
Gradle project, with no configuration knob. [M-KOTLIN] So current MyGo
builds are not subject to the Android 17 enforcement, but a future Play
target-API requirement will force a MyGo change. A LAN mosh/ET connection
on Android 17 would otherwise need the permission and a runtime request.

### Android 16 KB pages

MyGo already passes `-Wl,-z,max-page-size=16384` for the Go shared library
and for plugin native libraries. [M-ANDROID] [M-NATIVES] Google Play
requires 16 KB support on 64-bit devices for apps targeting Android 15+
and will block non-compliant updates starting **2027-02-01**.
[ANDROID-16KB] This is one mobile constraint MyGo handles correctly.

---

## 3. Native code, plugins, and prebuilt libraries

### The plugin surface

A Go package declares native mobile code in `mygo-plugin.json` under
`mobile` [M-PLUGINS]:

- `mobile.id`, `mobile.ios` (`sources`, `class`, `frameworks`,
  `infoPlist`, `entitlements`);
- `mobile.android` (`sources`, `class`, `permissions`, `dependencies`,
  `application` XML fragments);
- `mobile.native` (`name`, `archive`, `sources`, `cflags`, `ldflags`,
  `abis`).

Swift is compiled into the host's module; Kotlin into package `dev.mygo`;
both run on the UI thread and are called from Go with JSON via
`mygo.Native(id).Call` and events. [M-MOBILE] [M-PLUGINS]

### The native-library pipeline

`mobile.native` compiles **C code** into one dynamic library per plugin
[M-NATIVES]:

- iOS: clang `-target <ios-target> -isysroot <sdk> -dynamiclib
  -install_name @rpath/<name>.framework/<name>`, packaged as
  `<bundle>/Frameworks/<name>.framework/<name>` with a generated
  Info.plist, then signed with the app identity by `signFrameworks` before
  the app itself is signed and verified. [M-NATIVES] [M-IOS]
- Android: NDK clang `-shared -Wl,-z,max-page-size=16384
  -Wl,-soname,lib<name>.so`, packaged as `jniLibs/<abi>/lib<name>.so`.
  [M-NATIVES]
- Go loads it with `mygo.NativeLibrary(name)` plus purego: on Android the
  name `lib<name>.so`; on iOS the path
  `<exe-dir>/Frameworks/<name>.framework/<name>`; empty on desktops.
  [M-NATIVELIB] [M-MOBILE]
- purego v0.11.1 lists iOS and Android (amd64/arm64) as Tier 1, but both
  require `CGO_ENABLED=1` to compile. [PUREGO]

Compilation details that matter:

- The compiler is `clang`, not `clang++`: MyGo passes `-O2 -fPIC -DNDEBUG
  -w -I <work>`, then cflags, archive sources, listed sources, and
  ldflags. [M-NATIVES]
- Only `.c` files extracted from the optional archive are automatically
  added; `sources` is a glob list and can name other extensions, but no
  language-specific handling exists. [M-NATIVES]
- The native compile cache hashes the plugin JSON, target, compiler, args,
  listed sources, and `.h` files beside them; it does not model external
  libraries named only in `ldflags`. [M-NATIVES]

### What is not supported

These are verified by the schema/code, not by a device run:

- **No prebuilt mobile libraries.** Desktop `libraries` entries are
  skipped only when `mobile.native` compiles a library of the same name;
  otherwise the mobile build tries to pick an `ios-arm64`/
  `android-arm64` file and errors because the plugin lists none. [M-NATIVES-DESKTOP]
  MyGo does not install arbitrary `.a`/`.framework`/`.so` files for phones;
  it signs/compiles what it builds. [M-MOBILE]
- **No CMake, protoc, vcpkg, or other build system.** The only build step
  is the clang invocation above. [M-NATIVES]
- **No C++ standard library by default.** See the experiment below.
- **One native library per plugin.** Dependencies must be compiled into the
  same library or split across plugins with a separate shared-library
  loading scheme; there is no first-class per-ABI dependency list.
- **No iOS app-extension target.** The schema has no extension/appex
  concept, and the build creates one app bundle. [M-PLUGINS] [M-IOS]
- **No privacy-manifest merge from plugin frameworks.** The generated
  `PrivacyInfo.xcprivacy` is derived from the main executable only, unless
  the project supplies `ios/PrivacyInfo.xcprivacy`; plugin framework
  binaries are not scanned. [M-IOS]
- **No configurable Android targetSdk.** It is hardcoded to 36; minSdk
  defaults to 26. [M-KOTLIN] [M-CONFIG]

### Local C++ experiment (verified, not a MyGo build)

To test whether the MyGo native compile command can build C++, the same
clang driver invocation was run against iPhoneOS27.2.sdk with a trivial
C++ file using `<iostream>` and `std::string`. Without `-lc++`, the link
failed with undefined `std::__1::...` symbols. This confirms that MyGo's
`clang` invocation does not automatically link libc++ on iOS. The code
shows `t.clang` is the compiler and `ldflags` are appended before `-o`;
`ldflags: ["-lc++"]` would likely fix the iOS link, but that combination
was not run through `mygo build`. [M-NATIVES] [LOCAL-TEST]

Android was not tested because no NDK is installed on this machine. NDK
C++ linking normally needs `libc++_shared.so` packaged, or a static libc++
link; MyGo's Android pipeline packages only `lib<name>.so` and has no
dependency-packaging step. Treat Android C++ as unverified and likely to
require app-side build changes. [M-NATIVES] [ANDROID-16KB]

---

## 4. Terminal, UI, and process restrictions

### MyGo's UI is not a terminal

MyGo has native UI (Metal/OpenGL ES), text input, lists, tabs, and mobile
widgets, but no built-in terminal widget. The terminal lives in
`plugins/terminal`, which is a separate plugin built on libghostty-vt.
[M-MOBILE] [M-TERM-PLUGIN]

That plugin is **desktop-only in the PR**:

- PTY: `pty_unix.go` is tagged `(darwin && !ios) || (linux && !android)`;
  on all other platforms `pty_other.go` returns `errors.ErrUnsupported` for
  start/read/write/resize/kill. [M-TERM-PTY]
- Terminal emulator: `ffi.go` is tagged for desktop platforms; `ffi_other.go`
  sets `const Supported = false` and returns `ErrUnsupported`. [M-TERM-VT]
- Packaging: `mygo-plugin.json` lists only `darwin-amd64`,
  `darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64`, and
  `windows-arm64` builds of libghostty-vt. There are no `ios-arm64` or
  `android-arm64` entries. [M-TERM-PLUGIN]

Therefore importing the terminal plugin into a mobile MyGo build fails at
resource selection: the mobile build looks for an `ios-arm64` or
`android-arm64` library entry and finds none. Even if that were fixed,
there is no mobile libghostty-vt build in the PR and no mobile PTY.
[M-NATIVES-DESKTOP] [M-TERM-PLUGIN]

Implication: Natcatty cannot take its current terminal plugin to iOS/
Android unchanged. A pure-Go mosh client or ET client would need an
app-side terminal emulator/renderer (or a mobile build of libghostty-vt
plus changes to MyGo's native library pipeline).

### iOS process spawning

Apple DTS's answer in the Developer Forums is explicit:

> "You are correct that **iOS apps are not allowed to spawn child
> processes**."

[APPLE-DTS] This is forum guidance from Apple's DTS, not a formal policy
page, so it is cited as Apple guidance rather than as a documented API
contract.

The SDK itself does not remove all process APIs: in the locally installed
iPhoneOS27.2.sdk, `fork` is declared in `unistd.h` (with watchOS/tvOS
prohibitions, not an iOS one), and `posix_spawn`/`posix_spawnp` are
declared in `spawn.h`. [LOCAL-SDK] The restriction is enforced by the app
sandbox/runtime and App Review, not simply by the compiler. Do not read
the presence of `posix_spawn` as permission to use it.

App Review 2.5.2 adds the distribution constraint:

> "Apps should be self-contained in their bundles, and may not read or
> write data outside the designated container area, nor may they download,
> install, or execute code which introduces or changes features or
> functionality of the app, including other apps."

[APPLE-REVIEW]

Natcatty's current design, which bundles `mosh-client`/`et` binaries as
resources and spawns them via PTYs, therefore cannot work on iOS: there is
no PTY, no app-local execution, and no App Review story for a sidecar.
[APPLE-DTS] [M-TERM-PTY]

### Android process spawning and app-home W^X

Android 10 (API 29) removed execute permission for the writable app home
directory:

> "Untrusted apps that target Android 10 cannot invoke `execve()` directly
> on files within the app's home directory."

Apps should load only binary code embedded in the APK. [ANDROID-10]

MyGo's Android resources are copied from APK assets into
`filesDir/mygo-resources`, which is in the app's home directory, and
`mygo.PathResources` on Android is the `MYGO_RESOURCES` environment
variable pointing there. [M-KOTLIN] [M-PATHS] A resource-bundled sidecar
binary would land in a no-exec location. MyGo's native libraries are
different: they are packaged in `jniLibs` and loaded by the linker, not
executed as child processes. [M-NATIVES]

Android also requires dynamic code loading files to be read-only since
Android 14 for DEX/JAR/APK, and Android 17 extends a form of that rule to
native libraries loaded with `System.load()`. MyGo loads its Go library
through the normal APK/native-library mechanism; this note did not test
the Android 17 path. [ANDROID-14] [ANDROID-17]

### App Store background-mode limits

App Review 2.5.4:

> "Multitasking apps may only use background services for their intended
> purposes: VoIP, audio playback, location, task completion, local
> notifications, etc."

[APPLE-REVIEW]

This rules out the common hack of declaring `audio` or `voip` to keep an
SSH/mosh socket alive. A terminal client's honest background story is:
suspend when backgrounded, reconnect when foregrounded, and use
BGTaskScheduler only for discrete maintenance work (for example, a short
"check whether the session is still alive" task).

---

## 5. Packaging and distribution

### iOS

- Output: signed `.ipa`; the CLI builds/signs the Swift host, frameworks,
  icon, launch screen, privacy manifest, and bundle. [M-MOBILE] [M-IOS]
- Resources: project `resources/` is copied into the bundle; the resource
  directory is the executable's directory (bundle root), and
  `mygo.PathResources` resolves there. [M-RESOURCES] [M-PATHS]
  MyGo's platform-resource directories are only `darwin`, `linux`, and
  `windows`; there is no `ios-arm64` resource directory concept.
  [M-RESOURCES]
- Info.plist defaults include `MinimumOSVersion` 17.0 (config default),
  `ITSAppUsesNonExemptEncryption: false`, and no local-network usage
  description except in debug device builds. Plugin Info.plist keys are
  merged, then the app's own `ios.infoPlist` keys win. [M-IOS] [M-CONFIG]
- Signing: plugin entitlements must already be present in the provisioning
  profile or the build errors; frameworks are signed first, then the app
  is signed with entitlements and verified with `codesign --verify --deep
  --strict`. [M-IOS]
- Privacy: the generated privacy manifest covers the main executable's
  imported APIs; plugin framework binaries are not scanned. A project can
  supply its own `ios/PrivacyInfo.xcprivacy`. [M-IOS]
- iOS App Review concerns: self-contained bundle (2.5.2), public APIs
  (2.5.1), background services for their intended purpose (2.5.4), IPv6
  only networks (2.5.5), and export compliance. [APPLE-REVIEW]
  [APPLE-ENC]

### Android

- Output: `.apk` and `.aab`; ABIs arm64-v8a, armeabi-v7a, x86_64; Go
  library plus plugin native libraries in `jniLibs`. [M-ANDROID]
  [M-NATIVES]
- Manifest: MyGo always declares `INTERNET`, `ACCESS_NETWORK_STATE`, and
  `POST_NOTIFICATIONS`; plugin and `android.permissions` entries are added.
  [M-ANDROID]
- Gradle: `compileSdk = 36`, `targetSdk = 36`, `minSdk` default 26,
  `jniLibs { useLegacyPackaging = false }` (uncompressed libraries).
  [M-KOTLIN] [M-CONFIG]
- Resources: packaged as APK `assets/resources`, then copied by the host
  into `filesDir/mygo-resources` once per version. [M-KOTLIN] [M-PATHS]
- 16 KB ELF alignment: MyGo passes the required max-page-size flags for
  both Go and plugin libraries. [M-ANDROID] [M-NATIVES] [ANDROID-16KB]
- Signing: release builds use `android.keystore`/`keyAlias` or fall back to
  the debug key, which is unsuitable for store distribution. [M-MOBILE]
  [M-CONFIG]

### What ships for both

- No mobile-specific resource directory; the same generic resources are
  selected for iOS and Android (Android always selects the `arm64`
  resource view). [M-RESOURCES] [M-ANDROID]
- Plugin Swift/Kotlin is compiled into the host; plugin C is compiled per
  target into a framework/.so. [M-PLUGINS] [M-NATIVES]

---

## 6. Pure-Go mosh client: constraints and fit

### What mosh requires

mosh is a remote terminal application that supports intermittent
connectivity, roaming, and speculative local echo. It keeps the session
alive when the client sleeps/loses connectivity, and the client/server can
change IP addresses while keeping the connection. [MOSH-README]

`mosh-server`:

- binds UDP by default in the range **60000-61000**;
- exits if no client contacts it within 60 seconds at startup;
- if `MOSH_SERVER_NETWORK_TMOUT` is unset, "will wait indefinitely for a
  client to reappear"; the man page recommends high values such as one
  week or 30 days for mobile clients. [MOSH-MAN]

mosh uses SSH to authenticate and start the remote server. [MOSH-README]
mosh is **GPLv3**; a clean-room pure-Go protocol implementation avoids
copying that code, but porting or linking mosh code would raise GPL
obligations that are awkward for App Store distribution. [MOSH-LICENSE]
This is an engineering/legal risk to review, not a legal conclusion.

### What MyGo gives a pure-Go client

| Need | MyGo/phone situation |
|---|---|
| UDP socket to remote mosh-server | Go `net` works; outbound UDP is not specially restricted. |
| Resolve host | Go `net` uses cgo resolver on iOS/Android. [GO-NET] |
| SSH bootstrap | Natcatty's existing SSH stack can run `mosh-server` remotely; no local process needed. |
| Terminal emulation/prediction | No mobile terminal plugin in MyGo; app-side work. [M-TERM-PLUGIN] |
| Local echo/key handling | MyGo native UI text input exists; terminal key encoding/prediction is app-side. |
| Network change handling | Only online/metered events; no interface/IP/socket-binding API. [M-NETWORK] |
| Background survival | iOS suspends; Android Doze/FGS rules. [APPLE-BG] [ANDROID-DOZE] |
| LAN server | iOS local-network permission; Android 17 permission in future. [APPLE-TN3179] [ANDROID-17] |
| App Store | Self-contained pure-Go implementation is compatible; GPL reuse needs legal review. [APPLE-REVIEW] [MOSH-LICENSE] |
| Encryption declaration | mosh's crypto is the app's responsibility; MyGo defaults `ITSAppUsesNonExemptEncryption=false`. [APPLE-ENC] [M-IOS] |

### Verdict

A pure-Go mosh client removes the sidecar/PTY requirement and is a good
architectural match for mosh's roaming/server-side-session model. The
mobile blockers are not mosh's protocol; they are:

1. the terminal emulator/renderer and prediction logic that the desktop
   terminal plugin currently supplies;
2. background suspension (foreground-only session, reconnect on resume);
3. network-change detection and socket rebinding;
4. LAN permissions and IPv6-only App Review compliance;
5. App Store/export-compliance review of an SSH/mosh client.

These are tractable app-side work, not reasons the design cannot work.

---

## 7. In-process ET native library: constraints and fit

### What ET provides

ET is a remote shell that reconnects without interrupting the session.
The installed programs include:

- `et`, the command-line client: uses SSH to authenticate and start the
  remote session, then maintains the reconnectable ET connection;
- `etserver`, the system service listening on **TCP 2022** by default;
- `etterminal`, an internal server-side helper launched through SSH.
  [ET-README]

It is **Apache-2.0**. [ET-LICENSE]

### Build shape

ET's CMake project requires C++20 and builds static libraries:

- `et-lib` (core networking/protocol/crypto);
- `TerminalCommon` (terminal client/server common code);
- `libvterm` (vendored C terminal emulator, used by HTM);
- executables `et`, `etserver`, `etterminal`, `htm`, `htmd`.
  [ET-CMAKE]

Its vcpkg manifest requires zlib, OpenSSL, protobuf, libsodium,
sentry-native, nlohmann-json, simpleini, platform-folders, cpp-httplib,
cxxopts, and catch2. [ET-VCPKG] The CMake also finds Threads, Sanitizers,
Unwind, UTempter/SELinux on Linux, and generates protobuf C++ sources from
`proto/ET.proto` and `proto/ETerminal.proto`. [ET-CMAKE]

### Embeddability hooks (important nuance)

ET is not merely a monolithic CLI. Its headers expose:

- `TerminalClientMain(int argc, char** argv, const TerminalClientMainHooks&)`,
  where hooks can replace the SSH bootstrap subprocess
  (`SubprocessUtils`) and observe the client once connected.
  [ET-MAIN]
- `TerminalClient`, constructed with socket handlers, endpoint, id,
  passkey, a `Console`, tunnel settings, keepalive, environment, etc.; it
  runs the interactive session and reconnects. [ET-CLIENT]
- `Console`, an abstract interface for terminal info, setup/teardown,
  input polling, and output. A host can implement it over its own
  terminal widget. [ET-CONSOLE]

So in-process embedding is architecturally plausible: replace the SSH
subprocess with Natcatty's SSH, implement `Console`, instantiate
`TerminalClient`, and drive it from a C++ shim.

### Why MyGo's native pipeline does not support it as-is

1. **No C ABI.** ET's public surface is C++ classes/templates with no
   documented stable C API. Go/purego needs a C ABI, so the app must write
   an `extern "C"` C++ shim and compile it into the same library.
   [ET-CLIENT] [ET-CONSOLE] [M-NATIVELIB]
2. **No C++ build system.** MyGo invokes `clang` directly; it does not run
   CMake, vcpkg, protoc, or generate protobuf sources. ET's generated
   `ET.pb.cc`/`ETerminal.pb.cc` and dependency graph must be prepared
   by the app before MyGo sees the sources. [M-NATIVES] [ET-CMAKE]
3. **No static/prebuilt mobile libraries.** ET builds `et-lib` and
   `TerminalCommon` as static archives. MyGo does not install those for
   phones. A plugin could list `.a` files in `sources` or `ldflags`, but
   there is no per-ABI/per-platform selection and no packaging of dependent
   shared libraries; this is unsupported territory. [M-NATIVES]
   [M-NATIVES-DESKTOP]
4. **C++ runtime is not linked by default.** The local iOS clang
   experiment failed without `-lc++`; Android's libc++ packaging is
   untested and likely needs `libc++_shared.so` or a static libc++ link.
   [LOCAL-TEST] [M-NATIVES]
5. **Process-level assumptions.** ET's client has console fd polling,
   signals, an SSH agent proxy, subprocess bootstrap, and possibly other
   process/terminal assumptions. The hooks reduce but do not eliminate
   this; embedding a long-running C++ network client in a Go-hosted app
   needs careful threading and lifecycle work. [ET-CLIENT] [ET-MAIN]
6. **Binary size and review.** ET + protobuf + OpenSSL + libsodium + other
   dependencies is a large native payload, and every included library's
   license notices must ship. Apache-2.0 itself is App Store-compatible.
   [ET-LICENSE] [ET-VCPKG]

### Verdict

An in-process ET client is not a drop-in plugin. It would require:

- a C++ shim compiled with the ET sources and all dependencies;
- a prebuilt/static dependency strategy per ABI (iOS arm64/simulator;
  Android arm64-v8a/armeabi-v7a/x86_64);
- CMake/protoc generation outside MyGo, or vendored generated sources;
- `-lc++`/libc++ packaging and extensive device testing;
- a `Console` implementation integrated with Natcatty's terminal UI;
- replacing ET's SSH subprocess with the app's SSH session.

That is a substantial extension of MyGo's native pipeline, not something
the current `mobile.native` schema can express. A pure-Go mosh client is
the smaller first step.

---

## 8. What MyGo does not provide

Verified by PR source at the pinned commit, unless marked otherwise:

- **No mobile PTY or process-spawning API.** The only PTY implementation
  is the terminal plugin's desktop-only code; no exported MyGo API starts
  a process. [M-TERM-PTY] [M-MOBILE]
- **No mobile terminal widget/emulator.** The terminal plugin cannot
  build for iOS/Android (no mobile libghostty-vt, no mobile PTY). 
  [M-TERM-PLUGIN] [M-TERM-VT]
- **No persistent background socket support.** iOS suspends; Android
  restricts background services/Doze. MyGo's background plugin is discrete
  BGTaskScheduler/WorkManager work, not a socket service. [M-BACKGROUND]
  [APPLE-BG] [ANDROID-DOZE]
- **No foreground-service API.** A plugin can add a manifest `<service>`
  and Kotlin code, but MyGo has no first-class API. [M-PLUGINS]
- **No iOS Network Extension/app-extension target.** [M-PLUGINS] [M-IOS]
- **No network interface/roaming events or per-network socket binding.**
  [M-NETWORK]
- **No local-network permission request API.** The app must add the iOS
  usage description and use a plugin/permission flow; Android 17+ needs
  `ACCESS_LOCAL_NETWORK`. [APPLE-TN3179] [ANDROID-17]
- **No configurable Android targetSdk.** Hardcoded 36. [M-KOTLIN]
- **No prebuilt/static mobile library installation.** `mobile.native` is
  compiled from source; no per-ABI dependencies. [M-NATIVES]
  [M-NATIVES-DESKTOP]
- **No CMake/protoc/C++ runtime integration.** [M-NATIVES]
- **No privacy-manifest merge for plugin frameworks.** [M-IOS]
- **No automatic App Store export-compliance decision.** The default
  `ITSAppUsesNonExemptEncryption` is false; an SSH/mosh app must decide
  whether that is accurate. [M-IOS] [APPLE-ENC]
- **No guarantee of App Review acceptance** for an SSH/mosh client,
  background behavior, or LAN access; that is a product/review process.
  [APPLE-REVIEW]

---

## 9. Unknowns / needs verification

These are explicitly not settled by this note.

### About PR 940 itself

1. **The PR is open and unmerged.** The exact head is pinned, but APIs,
   build steps, and behavior may change before merge. Re-check the SHA
   before implementation.
2. **No MyGo mobile build was executed.** There was no Android NDK and no
   iOS simulator/device run. Claims about generated projects, signing,
   framework `dlopen`, and runtime behavior come from source reading, not
   observation.

### iOS runtime and review

3. **Exact suspension timing and background-task quotas.** Apple does not
   publish a guaranteed number of seconds; MyGo's "about 30 seconds" and
   "minutes with Long" are MyGo statements/estimates. Measure on target
   iOS versions. [M-BACKGROUND] [APPLE-BG]
4. **Local-network prompt behavior with/without a usage description.**
   TN3179 describes the privacy mechanism, but the exact App Review and
   runtime outcome for a user-entered LAN host without
   `NSLocalNetworkUsageDescription` was not tested. [APPLE-TN3179] [M-IOS]
5. **App Review treatment of an SSH/mosh client.** 2.5.2/2.5.4 are clear
   constraints, but whether a particular background/reconnect design is
   accepted is a review outcome. [APPLE-REVIEW]
6. **Export compliance.** Whether Natcatty's SSH/mosh crypto is exempt, and
   therefore whether MyGo's default `ITSAppUsesNonExemptEncryption=false`
   is correct, requires a legal/export determination. [APPLE-ENC]
7. **IPv6-only operation** (App Review 2.5.5) for the chosen mosh/ET
   implementation was not tested. [APPLE-REVIEW]
8. **Network Extension feasibility without forking MyGo.** The schema has
   no extension target; whether an app-side post-build/signing step could
   add one reliably was not investigated.
9. **purego callbacks and long-lived native calls on iOS.** purego is Tier
   1, but callback lifetime, main-thread restrictions, and framework
   loading on a real device were not exercised. [PUREGO]

### Android runtime and review

10. **Android C++/libc++ linkage.** No NDK was available. Whether ET can be
    built with MyGo's clang invocation, and whether `libc++_shared.so` can
    be packaged through the plugin schema, is unverified. [M-NATIVES]
11. **Foreground-service type acceptance.** `dataSync` has a 6-hour limit;
    `connectedDevice` has unrelated-looking prerequisites; `specialUse`
    requires Play review. Which type (if any) Play accepts for an
    interactive SSH/mosh session is a policy question. [ANDROID-FGS]
    [ANDROID-15]
12. **Doze behavior with a foreground service.** The documentation says
    Doze suspends network access; a foreground service keeps the process
    alive but does not obviously exempt it from Doze network suspension.
    Device testing is required. [ANDROID-DOZE]
13. **Android 17 `ACCESS_LOCAL_NETWORK` timing.** MyGo targets 36 today;
    when Google Play requires 37 and how MyGo will expose targetSdk is
    unknown. [ANDROID-17] [M-KOTLIN]
14. **Android 17 native DCL (`System.load` read-only)** and MyGo's loading
    path were not tested. [ANDROID-17]

### ET and mosh implementation

15. **ET in-process viability.** ET's `TerminalClientMainHooks` and
    `Console` suggest embedding is possible, but there is no public C ABI,
    no documented embedding support, and no test of ET's client without
    process-level assumptions (signals, SSH subprocess, console fds).
    [ET-MAIN] [ET-CLIENT] [ET-CONSOLE]
16. **ET dependency build for phones.** protobuf/OpenSSL/libsodium/sentry
    and the rest must be cross-built for iOS/Android; static vs shared,
    license notices, and 16 KB alignment are unresolved. [ET-VCPKG]
    [ET-CMAKE] [ANDROID-16KB]
17. **mosh protocol implementation scope.** A clean-room pure-Go client
    must implement UDP framing, crypto, terminal state sync, prediction,
    and SSH bootstrap; effort and interoperability were not estimated
    here. The GPLv3 license of mosh means code reuse has consequences.
    [MOSH-README] [MOSH-LICENSE]
18. **Existing pure-Go mosh clients.** No maintained pure-Go implementation
    was verified in this review; the conclusion is about constraints, not
    about a library to adopt.
19. **App size/performance.** The native ET payload and a pure-Go terminal
    emulator's memory/CPU cost on phones were not measured.

---

## Sources

Each MyGo URL is pinned to PR head
`7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a`; each Go URL to `go1.27.3`;
each purego URL to `v0.11.1`; mosh to
`decd9b705eb81626f694335b8d5940538beb06da`; ET to
`abaecea985cc98f90da0756211fedf4d78354a7c`.

- [PR940] https://github.com/egoist/mygo/pull/940 — open, unmerged,
  head `7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a`, base `main`, checked
  2026-11-03.
- [M-GOMOD] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/go.mod
- [M-MOBILE] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/mobile.md
- [M-ARCH] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/docs/architecture.md
- [M-IOS] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/ios.go
- [M-ANDROID] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/android.go
- [M-NATIVES] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_natives.go
- [M-PLUGINS] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/mobile_plugins.go
- [M-NATIVES-DESKTOP] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/natives.go
- [M-CONFIG] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/config.go
- [M-RESOURCES] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/cmd/mygo/resources.go
- [M-PATHS] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/paths.go
- [M-NATIVELIB] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/native.go
- [M-BACKEND] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/backend.go
- [M-TRANSPORT] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/transport.go
- [M-EVENTS] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/events.go
- [M-SERVICES] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/services.go
- [M-START] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/start.go
- [M-GLUE-IOS] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/glue_ios.go
- [M-GLUE-ANDROID] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/glue_android.go
- [M-SWIFT-APP] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/ios/Sources/MyGoHost/App.swift
- [M-SWIFT-HOST] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/ios/Sources/MyGoHost/Host.swift
- [M-KOTLIN] https://github.com/egoist/mygo/tree/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/internal/mobile/android
  — `MyGoActivity.kt`, `HostImpl.kt`, `Resources.kt`,
  `app/build.gradle.kts`.
- [M-NETWORK] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/network.go
- [M-TERM-PLUGIN] https://github.com/egoist/mygo/blob/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/mygo-plugin.json
- [M-TERM-PTY] https://github.com/egoist/mygo/tree/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/internal/pty
  — `pty_unix.go`, `pty_other.go`, `pty_darwin.go`, `pty_linux.go`.
- [M-TERM-VT] https://github.com/egoist/mygo/tree/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/terminal/internal/vt
  — `ffi.go`, `ffi_other.go`.
- [M-BACKGROUND] https://github.com/egoist/mygo/tree/7153b6c67c6d3835b7c3d3d33768f20ed7d3d61a/plugins/background
  — `background.go`, `ios/BackgroundPlugin.swift`, `mygo-plugin.json`.
- [GO-TAGS] https://github.com/golang/go/blob/go1.27.3/src/go/build/build.go
- [GO-SYSLIST] https://github.com/golang/go/blob/go1.27.3/src/internal/syslist/syslist.go
- [GO-MODES] https://github.com/golang/go/blob/go1.27.3/src/cmd/go/alldocs.go
- [GO-INIT] https://github.com/golang/go/blob/go1.27.3/src/cmd/go/internal/work/init.go
- [GO-NET] https://github.com/golang/go/blob/go1.27.3/src/net/conf.go
- [PUREGO] https://github.com/ebitengine/purego/blob/v0.11.1/README.md
- [APPLE-BG] https://developer.apple.com/documentation/uikit/preparing-your-ui-to-run-in-the-background
  — "Preparing your UI to run in the background", retrieved 2026-11-03.
- [APPLE-BGTASKS] https://developer.apple.com/documentation/backgroundtasks
  — BackgroundTasks framework and `BGTaskScheduler`, retrieved 2026-11-03.
- [APPLE-TN3179] https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy
  — "Understanding local network privacy", retrieved 2026-11-03.
- [APPLE-NE] https://developer.apple.com/documentation/networkextension
- [APPLE-NE-ENT] https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.developer.networking.networkextension
- [APPLE-REVIEW] https://developer.apple.com/app-store/review/guidelines/
  — sections 2.5.1, 2.5.2, 2.5.4, 2.5.5, retrieved 2026-11-03.
- [APPLE-DTS] https://developer.apple.com/forums/thread/747499
  — Apple Developer Forums, "'fork' is no-no but why?", DTS answer that
  iOS apps are not allowed to spawn child processes.
- [APPLE-ENC] https://developer.apple.com/documentation/bundleresources/information-property-list/itsappusesnonexemptencryption
- [LOCAL-SDK] Local Xcode artifact:
  `/Applications/Xcode.app/Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs/iPhoneOS27.2.sdk/usr/include/unistd.h`
  (fork at line 459) and `.../usr/include/spawn.h` (`posix_spawn` at
  line 60), inspected 2026-11-03.
- [LOCAL-TEST] Local experiment, 2026-11-03: iPhoneOS27.2.sdk clang,
  `-target arm64-apple-ios17.0 -dynamiclib -O2 -fPIC -DNDEBUG -w` on a
  C++ file using `<iostream>`/`std::string`, failed with undefined
  `std::__1::...`; no `-lc++`.
- [ANDROID-BG] https://developer.android.com/about/versions/oreo/background
  — "Background execution limits", Android 8.0.
- [ANDROID-DOZE] https://developer.android.com/training/monitoring-device-state/doze-standby
  — "Optimize for Doze and App Standby".
- [ANDROID-FGS] https://developer.android.com/develop/background-work/services/fgs/service-types
  — "Foreground service types are required".
- [ANDROID-FGS-START] https://developer.android.com/develop/background-work/services/fgs/restrictions-bg-start
  — "Restrictions on starting foreground services from the background".
- [ANDROID-15] https://developer.android.com/about/versions/15/behavior-changes-15
  — "Data sync foreground service timeout behavior".
- [ANDROID-17] https://developer.android.com/about/versions/17/behavior-changes-17
  — "Local network permission required for apps targeting Android 17";
  also "Safer Native DCL — C".
- [ANDROID-10] https://developer.android.com/about/versions/10/behavior-changes-10
  — "Removed execute permission for app home directory".
- [ANDROID-14] https://developer.android.com/about/versions/14/behavior-changes-14
  — "Safer dynamic code loading".
- [ANDROID-16KB] https://developer.android.com/guide/practices/page-sizes
  — "16 KB page sizes", Google Play compatibility requirement.
- [ANDROID-WORK] https://developer.android.com/reference/androidx/work/PeriodicWorkRequest
  — "Periodic work has a minimum interval of 15 minutes."
- [ANDROID-NET] https://developer.android.com/reference/android/net/ConnectivityManager.NetworkCallback
  — network callbacks; `onAvailable`, `onLost`,
  `onCapabilitiesChanged`, `onLinkPropertiesChanged`.
- [MOSH-README] https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/README.md
- [MOSH-MAN] https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/man/mosh-server.1
- [MOSH-LICENSE] https://github.com/mobile-shell/mosh/blob/decd9b705eb81626f694335b8d5940538beb06da/COPYING
- [ET-README] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/README.md
- [ET-CMAKE] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/CMakeLists.txt
- [ET-VCPKG] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/vcpkg.json
- [ET-LICENSE] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/LICENSE
- [ET-CLIENT] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/src/terminal/TerminalClient.hpp
- [ET-CONSOLE] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/src/terminal/Console.hpp
- [ET-MAIN] https://github.com/MisterTea/EternalTerminal/blob/abaecea985cc98f90da0756211fedf4d78354a7c/src/terminal/TerminalClientMain.hpp
