# Pin libghostty-vt at Ghostty befcdfd2 with ABI 0.1.0-dev

The terminal binds one libghostty-vt ABI: the library built from Ghostty commit
`befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028`, reporting library version
`0.1.0-dev`, which is the ABI MyGo v0.10.0's Go bindings mirror. Its 93 binding
symbols, 38 layouts and 36 enums are the contract in
`ui/terminal/internal/vt/symbols.txt` and the type-manifest check in
`ui/terminal/internal/vt`. The pin is recorded once, in
`ui/terminal/artifacts.json` (commit, library version, Zig 0.16.0, NDK
30.0.16248370, and each target's filename, URL, sha256 and size); the shipped
Android ABIs are the list in `scripts/mobile/android-abis.txt` (`arm64-v8a`,
`armeabi-v7a`, `x86_64`). The build, staging and link gates read those files,
and `terminal.Load` fails the launch when the linked library reports anything
but `ui/terminal/mobile.Version`.

**Status**: accepted

**Considered Options**:
- Adopt the 1.0.x line now: rejected. It removes or renames 5 of the 93 symbols,
  so `load()` fails while resolving the symbol table before the layout check
  runs, and it changes 14 struct layouts (for example `GhosttyStyle` from 72 to
  16 bytes) that `checkLayouts()` would reject anyway. It also has no Android
  prebuilt.
- Track MyGo's plugin version dynamically: rejected. The phone loader and the
  ABI manifest would move under the app without a binding port.
- Let each platform float to a different library: rejected. Desktop and phone
  share one terminal core and one symbol table.
- Pin one commit and self-build every phone artifact (chosen).

**Consequences**:
- No native binary is committed. `scripts/mobile/build-ghostty-vt.sh build`
  builds and verifies every target from the sha256-pinned source and writes the
  manifest; `scripts/mobile/fetch-artifacts.sh --platform ios|android`
  downloads the release and verifies every file, or builds the same pinned
  source when an asset is unavailable.
- A future Ghostty upgrade is a binding port, not a version bump: update the
  symbol table, the type fixture and `mobile.Version`, rebuild every artifact,
  and pass the compile, link, symbol and 16 KB page gates.
- The trigger for that port is a stable first phone release, or an upstream fix
  that exists only in 1.x.
- The pinned release `libghostty-vt-befcdfd2-mobile-1` returns 404.
  `fetch-artifacts.sh` therefore falls back to building the same sha256-pinned
  Ghostty commit, and `ui/terminal/artifacts.json` records that local pinned
  build rather than an unpublished release. Cross-host builds are not
  byte-reproducible, so the fallback writes its own `SHA256SUMS`. The old
  release tag can no longer match the manifest: unless matching assets are
  published, CI always builds Ghostty.
