# Packaging and release artifacts

Natcatty builds its native artifacts with MyGo v0.10.0. `mygo.json` is the
single source of the product name, identifier, version, and packaging
configuration; `go tool mygo build` reads it directly.

## Artifacts

Every published asset follows the same
`natcatty-<version>-<os>-<arch>[-<kind>].<ext>` scheme. MyGo writes the DMG,
the Setup executable, and the `.deb` files under its own names; the build job
renames them with `scripts/release.nu rename-assets` before writing any
checksum.
The `.deb`'s own `Version` field is Debian-encoded, so a SemVer pre-release
`0.1.0-rc.1` becomes `0.1.0~rc.1` inside the package; the published file name
keeps the SemVer form.

| Platform | Artifact | Notes |
| --- | --- | --- |
| macOS arm64 (Apple Silicon) | `natcatty-0.1.0-macos-arm64.dmg` | Contains a native arm64 `Natcatty.app`. Ad hoc signed only. |
| macOS amd64 (Intel) | `natcatty-0.1.0-macos-amd64.dmg` | Contains a native amd64 `Natcatty.app`. Ad hoc signed only. |
| Windows amd64 | `natcatty-0.1.0-windows-amd64-setup.exe` | NSIS installer, unsigned. The raw `Natcatty.exe` and its resources are the build inputs. |
| Windows amd64 | `natcatty-0.1.0-windows-amd64-portable.zip` | The raw executable and resources, without the NSIS installer. |
| Linux amd64 | `natcatty-0.1.0-linux-amd64.tar.gz`, `natcatty-0.1.0-linux-amd64.deb` | The tarball holds the executable and resources. MyGo's standalone `install.sh` is removed and not published. |
| Linux arm64 | `natcatty-0.1.0-linux-arm64.tar.gz`, `natcatty-0.1.0-linux-arm64.deb` | Same shape. |
| Android (universal) | `natcatty-0.1.0-android.apk` | One APK with the `arm64-v8a`, `armeabi-v7a` and `x86_64` libraries, all 16 KB page compatible. Signed with the project's release keystore. Installed from the file, not a store. See [android-release.md](android-release.md). |
| iOS arm64 (unsigned) | `natcatty-0.1.0-ios-unsigned.ipa` | Device build with no signature and no provisioning profile. A sideloading tool signs it with the installer's Apple ID before installing. See [ios-unsigned.md](releases/ios-unsigned.md). |

Every desktop artifact copies the `resources/` tree next to the executable
(inside `Contents/Resources` on macOS). That includes `THIRD-PARTY-NOTICES.md`,
`LICENSE`, `NOTICE`, `SOURCE-OFFER.md`, `CHANGELOG.md`, the bundled terminal
font files and OFL license under `fonts/maple-mono-nf/`, `icon.png`, and the
`mygo.json` release manifest. `CHANGELOG.md` ships as product documentation.
`LICENSE`, `NOTICE`, and the third-party notices are also read by the in-app
**Settings → About & Licenses** area. The five locale catalogs (`en`, `es`,
`ru`, `zh-CN`, `zh-TW`) are compiled into the executable with `go:embed`; they
are not loose files. `SOURCE-OFFER.md` is generated for the release version by
`internal/tools/sourceoffer`; it is not committed.

## Building

Build the platform you can package on its own machine:

```sh
go tool mygo build -platform windows/amd64 -o dist
go tool mygo build -platform linux/amd64,linux/arm64 -o dist
go tool mygo build -platform darwin/arm64,darwin/amd64 -o dist
go tool mygo build -platform android -o dist
```

The DMG and its `hdiutil` steps need macOS. Windows NSIS needs Windows, where
MyGo downloads NSIS when it is not installed. Cross-compiling the executables
needs no cgo, but the installers are a convenience of the native host.

The Android build needs the pinned SDK/NDK in `nix develop .#android`, the
libghostty-vt archive for each ABI staged by `scripts/mobile/android-libs.sh`,
and (for a release) the keystore described in
[android-release.md](android-release.md). Without a keystore it produces a
debug-signed APK, which is enough for development.

No transport client binary needs to be fetched or placed before packaging:
every Mosh and EternalTerminal Session runs in process.

## Release workflow

`.github/workflows/release.yml` runs on `workflow_dispatch` with an explicit
tag or on a pushed `v*` tag. A pushed tag publishes a GitHub release; a
`workflow_dispatch` run validates and builds only, as a dry run. There is no
workflow input that selects a commit: every job checks out `github.sha`, the
commit of the pushed tag or the dispatched ref, and `validate` asserts that
`git rev-parse HEAD` equals it.

1. **validate** reads the version and pre-release flag from `mygo.json` with
   `nu scripts/release.nu`, computes the Android version code with
   `internal/tools/androidversion` and fails unless it is greater than the
   previous release tag's, refuses updater or desktop signing environment
   variables, rejects committed keystore files and any committed
   `android.keystore` path, and pins the release commit to `github.sha`.
2. **build** runs on `macos-latest`, `windows-latest`, and `ubuntu-latest`,
   writes `resources/SOURCE-OFFER.md` for the manifest version with
   `go run ./internal/tools/sourceoffer`, builds with MyGo, renames every
   release artifact to its canonical name with
   `nu scripts/release.nu rename-assets`, and removes MyGo's standalone Linux
   `install.sh`. Windows also gets the portable archive, built from the app
   directory without the Setup executable by
   `nu scripts/release.nu make-portable`. Every platform then verifies the
   artifact's layout and required resources:
   - macOS: `hdiutil verify`, mounts the DMG, checks `Info.plist`, the version,
     the resources (including `CHANGELOG.md` and the legal documents), and
     `codesign -dv` (ad hoc, no `Authority`).
   - Windows: `7z l` must identify the NSIS installer and list the app and
     resources (including `CHANGELOG.md` and the legal documents); it is
     extracted and `Get-AuthenticodeSignature` must report `NotSigned`. The
     portable zip is listed too and must hold the app and resources without
     the Setup.
   - Linux: `dpkg-deb -f`/`-c` and `tar -tzf` must list the app, resources
     (including `CHANGELOG.md` and the legal documents), and platform transport
     clients.

   A separate **build-android** job installs the pinned SDK, NDK and
   build-tools, builds the pinned libghostty-vt archives from the commit and
   Zig version in `ui/terminal/artifacts.json` with
   `scripts/mobile/build-ghostty-vt.sh`, stages them with
   `scripts/mobile/android-libs.sh`, decodes the release keystore from
   secrets into `$RUNNER_TEMP`, writes the computed version code into the
   disposable manifest, and builds the universal APK. It deletes the `.aab`,
   then verifies the version code with `apkanalyzer manifest version-code`
   and every ABI's 16 KB `LOAD` alignment with `zipalign -c -P 16` and
   `llvm-readelf`.

   A **build-ios-unsigned** job fetches and stages the iOS device slices, then
   builds the device app with `go tool mygo build -platform ios -unsigned`
   (patch 0009). It needs no Apple account: the package carries no signature
   and no provisioning profile, so a sideloading tool signs it later. The job
   verifies that the executable is device-targeted (`vtool -show-build`
   reports `platform IOS`) and that the archive holds neither a
   `_CodeSignature` directory nor an `embedded.mobileprovision`.
3. **checksums** downloads the platform artifacts, flattens the release set
   with `nu scripts/release.nu collect`, asserts the exact ten-file set, and
   generates one aggregate checksum manifest with relative names. Both the
   platform artifacts and the aggregate manifest are uploaded as workflow
   artifacts. It depends on `build`, `build-android` and `build-ios-unsigned`,
   so a failed build prevents the release set from being assembled.
4. **publish** runs only for a pushed tag, depends on `validate`, `build`,
   `build-android`, `build-ios-unsigned`, and `checksums`, and is the only job with
   `contents: write`. Its condition is
   `success() && github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')`;
   it never uses `if: always()`, so a failed or skipped prerequisite blocks
   publication even when the tag condition is true. It downloads the platform
   artifacts and flattens them again. It regenerates the aggregate checksum
   manifest and requires it to be byte-identical to the one the `checksums`
   job produced. It then creates a GitHub release titled
   `Natcatty <version>` for the tag, or edits the existing release when the
   tag is re-pushed or force-moved, and attaches the release assets with
   `--clobber`. The body is generated by GitHub's release-notes API from the
   tag's commits and merged pull requests; apart from the one-time
   identifier-migration note described under Release page, it is not read from
   the repository.
   A version with a SemVer pre-release suffix is marked as a GitHub pre-release
   and is never marked Latest. The tag and the release are
   never deleted. A `workflow_dispatch` run skips this job and publishes
   nothing.

### Transport parity

Transport parity runs as ordinary CI, not as part of a release.
`.github/workflows/mosh-parity.yml` and `.github/workflows/et-parity.yml` are
triggered by pull requests and pushes to `main` that touch the corresponding
client, the session layer, or the parity tool, and can also be started
manually with `workflow_dispatch`. Each workflow fails closed over its matrix
cells and uploads a machine-readable summary as `mosh-parity-summary` /
`et-parity-summary`, so a failed gate still leaves per-cell or per-fuzz
evidence on the run.

The same verdict can be checked locally over a report directory with:

```sh
go run ./internal/tools/moshparity verify -dir build/mosh-parity \
  -commit "$(git rev-parse HEAD)" -require-summary
go run ./internal/tools/etparity verify -dir build/et-parity -require-summary
```

ET summaries are bound to the CI run that produced them. To check a summary
downloaded from CI, set `GITHUB_RUN_ID` to the originating run ID; otherwise
the required-summary check fails closed because the verifier is not bound to
that run.

## Release page

A tag push creates a GitHub release for that tag. Its body is generated by
GitHub's release-notes API from the tag's commits and merged pull requests; it
is not written by hand and is not derived from `CHANGELOG.md`. Two checked-in
notes are appended. The first release whose `mygo.json` identifier differs
from the previous release's gets the checked-in
`docs/releases/identifier-migration.md` note, so a reader who sees only the
release page learns about the one-time reset; later releases do not repeat it.
Every release also appends the checked-in
`docs/releases/android-apk.md` note, which says the Android app is installed
from the APK file rather than a store, and the
`docs/releases/ios-unsigned.md` note, which says the iOS package must be
signed by a sideloading tool first. The assets are the ten installers and the
checksum manifest:

- `natcatty-<version>-macos-arm64.dmg`
- `natcatty-<version>-macos-amd64.dmg`
- `natcatty-<version>-windows-amd64-setup.exe`
- `natcatty-<version>-windows-amd64-portable.zip`
- `natcatty-<version>-linux-amd64.tar.gz`
- `natcatty-<version>-linux-arm64.tar.gz`
- `natcatty-<version>-linux-amd64.deb`
- `natcatty-<version>-linux-arm64.deb`
- `natcatty-<version>-android.apk`
- `natcatty-<version>-ios-unsigned.ipa`
- the checksum manifest, which lists all ten with relative names

A tag whose version has a pre-release suffix (for example
`v0.1.0-rc.1`) produces a GitHub pre-release instead of a normal release, and
is never marked Latest. The license, modification notice, complete third-party
notices, and corresponding-source offer are carried inside each installer next
to the executable. The workflow asserts the offer is in every extracted package
before publishing; the release body does not repeat it.

The desktop release stays unsigned apart from MyGo's ad hoc macOS signature
and carries no notarization or update metadata; publishing it does not relax
any of the checks above. The Android APK is signed with the project keystore
as described under Signing, notarization, and updates.

`scripts/release.nu` is the checked release tooling. It reads `mygo.json`,
renames the built packages, writes the Windows portable archive, and generates
and flattens the checksum manifests. The release workflow installs the pinned
Nushell release with `.github/actions/setup-nu`; install Nushell locally to run
the same commands:

```sh
nu scripts/release.nu version --manifest mygo.json
nu scripts/release.nu version --manifest mygo.json --debian
nu scripts/release.nu prerelease --manifest mygo.json
nu scripts/release.nu rename-assets --dir dist --version 0.1.0
nu scripts/release.nu make-portable --dir dist/windows-amd64 \
  --out dist/natcatty-0.1.0-windows-amd64-portable.zip \
  --exclude natcatty-0.1.0-windows-amd64-setup.exe
nu scripts/release.nu generate-sums --dir dist
nu scripts/release.nu collect --from downloads --to release
```

`make-portable` needs `7z`; the Windows runner provides it. The aggregate
manifest uses names relative to the collected artifact directory, so verify it
from that directory with `sha256sum -c SHA256SUMS`.

## Release notes and the source offer

`CHANGELOG.md` follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
The GitHub release body is generated by GitHub's release-notes API from the
commits and merged pull requests between the previous tag and the new tag. The
workflow calls `repos/<owner>/<repo>/releases/generate-notes` with the tag and
passes the result to `gh release create` or `gh release edit` with
`--notes-file`; a re-pushed tag gets the same generated notes regenerated for
its new commit. `CHANGELOG.md` remains the human-maintained changelog, but the
release body is not parsed from it.

The publish job then runs `internal/tools/releasebody`. It reads the identifier
from `mygo.json`, reads the manifest at the nearest tag reachable from the new
tag's parent, and appends `docs/releases/identifier-migration.md` only when
that previous identifier differs or no previous tag exists. An unreadable
previous manifest counts as different, so the note errs toward being shown;
running the tool again on existing notes is idempotent. Compose a body locally
with:

```sh
go run ./internal/tools/releasebody -notes NOTES.md -out NOTES.md \
  -manifest mygo.json -previous-manifest PREVIOUS-MYGO.json \
  -fragment docs/releases/identifier-migration.md
```

The GPL corresponding-source offer is separate from the release notes. The
checked tool in `internal/tools/sourceoffer` writes it for one version:

```sh
go run ./internal/tools/sourceoffer -version 0.1.0 -out SOURCE-OFFER.md
```

It uses `internal/legal.SourceOfferDocument`, the same legal text the packaged
application exposes, so the offer cannot drift from the in-app documents. The
document names the GPL-3.0-or-later license, the repository URL at the release
tag, the in-process Mosh and EternalTerminal attributions, and the warranty
disclaimer. The build job
writes it to `resources/SOURCE-OFFER.md` for the manifest version, and every
artifact check requires the file in the package.

## Signing, notarization, and updates

The desktop release is deliberately unsigned apart from MyGo's **ad hoc**
macOS signature, which only makes the bundle internally consistent; it is not
a Developer ID signature and Gatekeeper will not trust it. There is no
desktop notarization, no Authenticode certificate, and no `mygo keygen`.
`mygo.json` has no `updates` block, `macos.signingIdentity`,
`macos.notarize`, or Windows signing configuration, and the workflow refuses
those inputs and rejects `update-*.json`, `latest*.yml`, appcast files, and
delta archives in the built artifacts.

The Android APK is different: it is signed with natcatty's own release
keystore, whose material lives only in repository secrets and never in the
checkout; the build fails closed when a secret is missing. There is still no
store account: users install the APK from the release file. Development builds
with no keystore fall back to the Android debug key. Keystore creation, backup,
and the unrecoverable loss of the key are documented in
[android-release.md](android-release.md).

## Bundled terminal font and Linux CJK fonts

Natcatty bundles unmodified Maple Mono NF (`v7.9`, SIL OFL 1.1, Latin + Nerd
Font glyphs) under `resources/fonts/maple-mono-nf/` and registers it at startup
with `ui.RegisterFont` as the default terminal font on desktop and phone
builds.

MyGo still renders UI text and CJK fallback through the system font collection.
A Linux system with no CJK font draws no glyphs for `zh-CN` or `zh-TW`; the
window and layout still work, but the Chinese labels appear blank. Install a
CJK font before running the app, for example on Debian/Ubuntu:

```sh
sudo apt-get install fonts-noto-cjk
fc-cache -f
```

The CI images install `fonts-noto-cjk` for the same reason. The app does not
bundle a CJK font and the `.deb` does not declare a font dependency; shipping a
full CJK font would add tens of megabytes, so CJK glyphs rely on the system
font fallback.

## Reproducibility

The Go executables themselves are byte-reproducible for identical sources,
inputs, and toolchain; two local Linux builds produced identical `natcatty`
binaries. The tarballs and `.deb` packages
are not byte-reproducible because MyGo embeds build timestamps in their
archives, so two builds differ in those container files even when every payload
is identical. Treat the aggregate checksum manifest as identifying the
artifacts of one workflow run, not as a promise that rebuilding later gives the
same container bytes.
