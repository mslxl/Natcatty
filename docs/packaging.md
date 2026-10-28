# Packaging and release artifacts

Natcatty builds its native artifacts with MyGo v0.5.5. `mygo.json` is the
single source of the product name, identifier, version, and packaging
configuration; `go tool mygo build` reads it directly.

## Artifacts

| Platform | Artifact | Notes |
| --- | --- | --- |
| macOS (universal) | `Natcatty 0.1.0.dmg` | Contains `Natcatty.app` for arm64 and amd64. Ad hoc signed only. |
| Windows amd64 | `Natcatty Setup 0.1.0 amd64.exe` | NSIS installer, unsigned. The raw `Natcatty.exe` and its resources are the build inputs. |
| Linux amd64 | `natcatty-0.1.0-linux-amd64.tar.gz`, `natcatty_0.1.0_amd64.deb`, `install.sh` | The tarball holds the executable and resources; `install.sh` installs either arch for the current user. |
| Linux arm64 | `natcatty-0.1.0-linux-arm64.tar.gz`, `natcatty_0.1.0_arm64.deb`, `install.sh` | Same shape; the arm64 bundle selects the arm64 transport clients. |

Every artifact copies the `resources/` tree next to the executable (inside
`Contents/Resources` on macOS). That includes `THIRD-PARTY-NOTICES.md`,
`CHANGELOG.md`, the ET and mosh metadata and licenses, the platform transport
clients fetched for the build, `icon.png`, and the `mygo.json` release
manifest. `CHANGELOG.md` ships so the packaged application's ticket 60
self-test can generate release notes from the file it was built with. The five
locale catalogs (`en`, `es`, `ru`, `zh-CN`, `zh-TW`) are compiled into the
executable with `go:embed`; they are not loose files.

## Building

Build the platform you can package on its own machine:

```sh
go tool mygo build -platform windows/amd64 -o dist
go tool mygo build -platform linux/amd64,linux/arm64 -o dist
go tool mygo build -platform darwin/universal -o dist
```

The DMG and its `hdiutil` steps need macOS. Windows NSIS needs Windows, where
MyGo downloads NSIS when it is not installed. Cross-compiling the executables
needs no cgo, but the installers are a convenience of the native host.

The transport clients for the target being packaged must exist under
`resources/et/<target>` and `resources/mosh/<target>`. `win32-x64` is committed;
the other targets are fetched and digest-verified by:

```sh
go run ./internal/tools/fetchtransportbins -kind all -target linux-x64,linux-arm64,darwin-universal -out resources
```

The release workflow fetches only the target it builds and removes the others
from the ephemeral workspace so each package carries only runnable clients.

## Release workflow

`.github/workflows/release.yml` runs on `workflow_dispatch` with an explicit
tag or on a pushed `v*` tag.

1. **validate** checks the Go release tooling tests (including the changelog
   parser and release-note generator), runs the parity release gate
   (`go run ./internal/tools/paritygen -release`), requires the tag to be
   strict SemVer (`vMAJOR.MINOR.PATCH[-pre]`) exactly matching `mygo.json`
   `version`, rejects signing/notarization/update configuration, refuses
   updater or signing environment variables, and generates `RELEASE_NOTES.md`
   from `CHANGELOG.md` for that version and uploads it as the
   `natcatty-release-notes` workflow artifact. A missing changelog entry, a
   duplicate version, or an empty section fails validation.
2. **build** runs on `macos-latest`, `windows-latest`, and `ubuntu-latest`,
   builds with MyGo, and verifies the artifact itself:
   - macOS: `hdiutil verify`, mounts the DMG, checks `Info.plist`, the version,
     the resources (including `CHANGELOG.md`), the embedded catalogs, and
     `codesign -dv` (ad hoc, no `Authority`).
   - Windows: `7z l` must identify the NSIS installer and list the app and
     resources (including `CHANGELOG.md`); it is extracted, the embedded
     catalogs are checked, and `Get-AuthenticodeSignature` must report
     `NotSigned`.
   - Linux: `dpkg-deb -f`/`-c` and `tar -tzf` must list the app, resources
     (including `CHANGELOG.md`), and platform transport clients; the extracted
     executable must embed all five catalogs.
   Every job also runs `releasecheck assert-no-updates`, then generates and
   verifies a per-platform `SHA256SUMS`.
3. **checksums** downloads every `natcatty-*` workflow artifact, including
   `natcatty-release-notes`, then runs `scripts/verify-downloads.sh`, which
   asks `releasecheck classify` which directories are platform artifact sets
   and which is the release notes. It verifies each platform's list again
   after transfer, skips the notes directory (it has no `SHA256SUMS` and is
   not a release artifact), flattens the release set, and generates one
   aggregate `SHA256SUMS` with relative names. It also asserts the exact
   seven-file set. Both the platform artifacts and the aggregate checksum
   file are uploaded as workflow artifacts. **No GitHub release is created or
   published.**

`releasecheck` is checked Go tooling in `internal/tools/releasecheck`; its
rules live in `internal/release`. Run it locally with:

```sh
go run ./internal/tools/releasecheck validate-tag -tag v0.1.0 -manifest mygo.json
go run ./internal/tools/releasecheck generate-sums -dir dist
go run ./internal/tools/releasecheck verify-sums -dir dist
```

`SHA256SUMS` is compatible with `sha256sum -c` when run from the directory the
names are relative to. The aggregate file is flat, so run it from the directory
holding the collected artifacts.

## Release notes

`CHANGELOG.md` follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Release notes are generated from it, never written by hand, with the library in
`internal/releasenotes` and the checked tool in
`internal/tools/releasenotes`:

```sh
go run ./internal/tools/releasenotes -version 0.1.0 -out RELEASE_NOTES.md
```

The tool reads the version section from `CHANGELOG.md` (`-changelog` overrides
the default), accepts `v`-prefixed tags, and fails when the version has no
section, appears twice, or has an empty body. The output is deterministic for
the same changelog and version: the title is `# Natcatty <version>`, then the
release date, then the section body. The release workflow writes
`RELEASE_NOTES.md` for the validated manifest version and uploads it beside the
build artifacts; it does not create a GitHub release.

## Signing, notarization, and updates

The release is deliberately unsigned apart from MyGo's **ad hoc** macOS
signature, which only makes the bundle internally consistent; it is not a
Developer ID signature and Gatekeeper will not trust it. There is no
notarization, no Authenticode certificate, and no `mygo keygen`. `mygo.json`
has no `updates` block, `macos.signingIdentity`, `macos.notarize`, or Windows
signing configuration, and the workflow refuses those inputs and rejects
`update-*.json`, `latest*.yml`, appcast files, and delta archives in the built
artifacts.

## Linux CJK fonts

MyGo renders text through the system font collection. A Linux system with no
CJK font draws no glyphs for `zh-CN` or `zh-TW`; the window and layout still
work, but the Chinese labels appear blank. Install a CJK font before running
the app, for example on Debian/Ubuntu:

```sh
sudo apt-get install fonts-noto-cjk
fc-cache -f
```

The CI images install `fonts-noto-cjk` for the same reason. The app does not
bundle a CJK font and the `.deb` does not declare a font dependency. MyGo does
expose `ui.RegisterFont`, but shipping a full CJK font would add tens of
megabytes and licensing review; the prerequisite is documented instead.

## Reproducibility

The Go executables themselves are byte-reproducible for identical sources,
inputs, and toolchain; two local Linux builds produced identical `natcatty`
binaries and identical `install.sh` scripts. The tarballs and `.deb` packages
are not byte-reproducible because MyGo embeds build timestamps in their
archives, so two builds differ in those container files even when every payload
is identical. Treat `SHA256SUMS` as identifying the artifacts of one workflow
run, not as a promise that rebuilding later gives the same container bytes.
