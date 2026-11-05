# Packaging and release artifacts

Natcatty builds its native artifacts with MyGo v0.6.0. `mygo.json` is the
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
| macOS (universal) | `natcatty-0.1.0-macos-universal.dmg` | Contains `Natcatty.app` for arm64 and amd64. Ad hoc signed only. |
| Windows amd64 | `natcatty-0.1.0-windows-amd64-setup.exe` | NSIS installer, unsigned. The raw `Natcatty.exe` and its resources are the build inputs. |
| Windows amd64 | `natcatty-0.1.0-windows-amd64-portable.zip` | The raw executable and resources, without the NSIS installer. |
| Linux amd64 | `natcatty-0.1.0-linux-amd64.tar.gz`, `natcatty-0.1.0-linux-amd64.deb` | The tarball holds the executable and resources. MyGo's standalone `install.sh` is removed and not published. |
| Linux arm64 | `natcatty-0.1.0-linux-arm64.tar.gz`, `natcatty-0.1.0-linux-arm64.deb` | Same shape; the arm64 bundle selects the arm64 transport clients. |

Every artifact copies the `resources/` tree next to the executable (inside
`Contents/Resources` on macOS). That includes `THIRD-PARTY-NOTICES.md`,
`LICENSE`, `NOTICE`, `SOURCE-OFFER.md`, `CHANGELOG.md`, the ET and mosh
metadata and licenses, the platform transport clients fetched for the build,
`icon.png`, and the `mygo.json` release manifest. `CHANGELOG.md` ships as
product documentation. `LICENSE`, `NOTICE`, and the third-party notices are
also read by the in-app **Settings → About & Licenses** area. The five locale
catalogs (`en`, `es`, `ru`, `zh-CN`, `zh-TW`) are compiled into the executable
with `go:embed`; they are not loose files. `SOURCE-OFFER.md` is generated for
the release version by `internal/tools/sourceoffer`; it is not committed.

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
tag or on a pushed `v*` tag. A pushed tag publishes a GitHub release; a
`workflow_dispatch` run validates and builds only, as a dry run.

1. **validate** runs the legal package tests (`go test ./internal/legal`), reads
   the version and pre-release flag from `mygo.json` with
   `nu scripts/release.nu`, and refuses updater or signing environment
   variables.
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
3. **checksums** downloads the platform artifacts, flattens the release set
   with `nu scripts/release.nu collect`, asserts the exact seven-file set, and
   generates one aggregate checksum manifest with relative names. Both the
   platform artifacts and the aggregate manifest are uploaded as workflow
   artifacts.
4. **publish** runs only for a pushed tag, depends on `validate`, `build`, and
   `checksums`, and is the only job with `contents: write`. It downloads the
   platform artifacts and flattens them again. It regenerates the aggregate
   checksum manifest and requires it to be byte-identical to the one the
   `checksums` job produced. It then creates a GitHub release titled
   `Natcatty <version>` for the tag, or edits the existing release when the
   tag is re-pushed or force-moved, and attaches the release assets with
   `--clobber`. The body is generated by GitHub's release-notes API from the
   tag's commits and merged pull requests; it is not read from the repository.
   A version with a SemVer pre-release suffix is marked as a GitHub pre-release
   and is never marked Latest. The tag and the release are
   never deleted. A `workflow_dispatch` run skips this job and publishes
   nothing.

## Release page

A tag push creates a GitHub release for that tag. Its body is generated by
GitHub's release-notes API from the tag's commits and merged pull requests; it
is not written by hand and is not derived from `CHANGELOG.md`. The assets are
the seven installers and the checksum manifest:

- `natcatty-<version>-macos-universal.dmg`
- `natcatty-<version>-windows-amd64-setup.exe`
- `natcatty-<version>-windows-amd64-portable.zip`
- `natcatty-<version>-linux-amd64.tar.gz`
- `natcatty-<version>-linux-arm64.tar.gz`
- `natcatty-<version>-linux-amd64.deb`
- `natcatty-<version>-linux-arm64.deb`
- the checksum manifest, which lists all seven with relative names

A tag whose version has a pre-release suffix (for example
`v0.1.0-rc.1`) produces a GitHub pre-release instead of a normal release, and
is never marked Latest. The license, modification notice, complete third-party
notices, and corresponding-source offer are carried inside each installer next
to the executable. The workflow asserts the offer is in every extracted package
before publishing; the release body does not repeat it.

The release stays unsigned apart from MyGo's ad hoc macOS signature and carries
no notarization or update metadata; publishing it does not relax any of the
checks above.

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

The GPL corresponding-source offer is separate from the release notes. The
checked tool in `internal/tools/sourceoffer` writes it for one version:

```sh
go run ./internal/tools/sourceoffer -version 0.1.0 -out SOURCE-OFFER.md
```

It uses `internal/legal.SourceOfferDocument`, the same legal text the packaged
application exposes, so the offer cannot drift from the in-app documents. The
document names the GPL-3.0-or-later license, the repository URL at the release
tag, the bundled component sources, and the warranty disclaimer. The build job
writes it to `resources/SOURCE-OFFER.md` for the manifest version, and every
artifact check requires the file in the package.

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
binaries. The tarballs and `.deb` packages
are not byte-reproducible because MyGo embeds build timestamps in their
archives, so two builds differ in those container files even when every payload
is identical. Treat the aggregate checksum manifest as identifying the
artifacts of one workflow run, not as a promise that rebuilding later gives the
same container bytes.
