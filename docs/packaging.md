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
| macOS arm64 (Apple Silicon) | `natcatty-0.1.0-macos-arm64.dmg` | Contains a native arm64 `Natcatty.app`. Ad hoc signed only. |
| macOS amd64 (Intel) | `natcatty-0.1.0-macos-amd64.dmg` | Contains a native amd64 `Natcatty.app`. Ad hoc signed only. |
| Windows amd64 | `natcatty-0.1.0-windows-amd64-setup.exe` | NSIS installer, unsigned. The raw `Natcatty.exe` and its resources are the build inputs. |
| Windows amd64 | `natcatty-0.1.0-windows-amd64-portable.zip` | The raw executable and resources, without the NSIS installer. |
| Linux amd64 | `natcatty-0.1.0-linux-amd64.tar.gz`, `natcatty-0.1.0-linux-amd64.deb` | The tarball holds the executable and resources. MyGo's standalone `install.sh` is removed and not published. |
| Linux arm64 | `natcatty-0.1.0-linux-arm64.tar.gz`, `natcatty-0.1.0-linux-arm64.deb` | Same shape. |

Every artifact copies the `resources/` tree next to the executable (inside
`Contents/Resources` on macOS). That includes `THIRD-PARTY-NOTICES.md`,
`LICENSE`, `NOTICE`, `SOURCE-OFFER.md`, `CHANGELOG.md`, the ET metadata and
licenses, `icon.png`, and the `mygo.json` release manifest. `CHANGELOG.md`
ships as product documentation. `LICENSE`, `NOTICE`, and the third-party notices are
also read by the in-app **Settings → About & Licenses** area. The five locale
catalogs (`en`, `es`, `ru`, `zh-CN`, `zh-TW`) are compiled into the executable
with `go:embed`; they are not loose files. `SOURCE-OFFER.md` is generated for
the release version by `internal/tools/sourceoffer`; it is not committed.

## Building

Build the platform you can package on its own machine:

```sh
go tool mygo build -platform windows/amd64 -o dist
go tool mygo build -platform linux/amd64,linux/arm64 -o dist
go tool mygo build -platform darwin/arm64,darwin/amd64 -o dist
```

The DMG and its `hdiutil` steps need macOS. Windows NSIS needs Windows, where
MyGo downloads NSIS when it is not installed. Cross-compiling the executables
needs no cgo, but the installers are a convenience of the native host.

No transport client binary needs to be fetched or placed before packaging:
every Mosh and EternalTerminal Session runs in process.

## Packaged artifact tests

Ticket 34 Phase 3A adds a hidden packaged-artifact test mode to the normal,
untagged app. The binary that ships is the binary CI exercises; there is no
`launchsmoke`-style
build tag for this path. The mode is dormant unless `NATCATTY_SMOKE` is exactly
`1`; the boolean flags below are exact for the same reason.

`scripts/artifact-tests.nu` unwraps the shipped artifact, launches the app,
waits for its exit, and validates the result JSON. Phase 3B added
`.github/workflows/artifact-tests.yml`, which runs this driver on macOS, Linux,
and Windows arm64. See [Packaged artifact tests in CI](ci/artifact-tests.md)
for the six jobs, pinned lab servers, dispatch instructions, and residual
CI-only risks. The workflow is not wired into `release.yml` and publishes
nothing; its evidence is recorded in
[transport-rollout.md](releases/transport-rollout.md#flip-evidence).

### Environment contract

All values are read once, before MyGo opens a window. The first four are
required. Session-only values are ignored in launch mode.

| Variable | Meaning |
| --- | --- |
| `NATCATTY_SMOKE=1` | The only activation trigger. Any other value leaves the normal app completely inert. |
| `NATCATTY_SMOKE_MODE=launch\|session` | `launch` proves a visible main window. `session` also opens real in-process Mosh and ET Sessions. |
| `NATCATTY_SMOKE_RESULT=<path>` | Atomic result file. The result file and the process exit code are authoritative; a Windows GUI build has no usable stdout. |
| `NATCATTY_SMOKE_PROFILE=<dir>` | Isolated MyGo user-data directory. The app calls `mygo.App.SetPath(PathUserData, ...)` before opening the database or preferences. |
| `NATCATTY_SMOKE_SSH_PORT=<port>` | Lab sshd port for session mode; default `22`. |
| `NATCATTY_SMOKE_ET_PORT=<port>` | Lab etserver port for session mode; default `2022`. |
| `NATCATTY_SMOKE_PASSWORD=<value>` | Password for the lab SSH account. Required in session mode, passed only through the environment, and never written to the result or logs. |
| `NATCATTY_SMOKE_SSH_USER=<name>` | Lab SSH account for session mode; defaults to the current OS user. Set it when the local account cannot authenticate, for example a macOS runner account whose Secure Token blocks a scripted password reset. |
| `NATCATTY_SMOKE_SESSION_SECRETS=1` | Puts the Vault in session-only credential mode. Use it on a headless Linux runner whose OS credential store is unavailable. |
| `NATCATTY_SMOKE_ALLOW_UNSUPPORTED_SESSION=1` | Lab-only escape hatch for session mode on a platform outside the Phase 3A gate. It changes no check or result semantics. |

The isolated profile covers user-data files and the Vault database, not every
OS integration. When an OS credential store is available the app still opens
it, using a profile-scoped service name so the run does not read or write the
user's real entries; session-only credential mode is available only when that
store is missing. The test mode skips the real shell-history import and does not
register the `ssh` URL handler, so a temporary artifact cannot change the
user's handler.

Session mode uses the current OS user as the SSH account. It creates two Hosts
in the temporary profile (`127.0.0.1`, the configured SSH port, the current
user, the lab secret) with `TransportImplementation=in_process` explicitly,
so the per-Host choice wins regardless of the release default the build
ships with.
It dials the lab sshd with `x/crypto/ssh`, captures the host key before
authenticating, inserts a `domainconn.KnownHost` through the normal database
`UpsertKnownHost` path, then opens each Host through the shell's
`OpenHostForMCP` and runs one fixed command:

```text
printf 'NATCATTY_SMOKE_MOSH_OK\n'
printf 'NATCATTY_SMOKE_ET_OK\n'
```

Opening the Session proves the transport handshake; the command proves a live
remote shell. Both Sessions are closed afterwards. The only commands the mode
can run are those two compile-time constants: it reads no command, executable,
or script path from the environment, and it opens no listener. An internal 90
second watchdog writes a failure result and exits non-zero so a wedged dial,
trust prompt, or remote server cannot hang CI. A successful run sets
`Security.ConfirmCloseSessions=false` and quits through the normal MyGo quit
sequence.

The host-key capture probe must offer the dialer's preference order
(`sshadapter.DefaultHostKeyAlgorithms()`), not x/crypto's default order: the
seeded Known Host is only useful if it names the key the live session actually
negotiates. On macOS, session runs omit `--session-secrets`; session-only
credential mode applies only when the OS credential store is unavailable (for
example, a headless Linux runner).

### Result and exit codes

The result is written atomically in the destination directory:

```json
{
  "mode": "launch",
  "platform": "darwin/arm64",
  "checks": [
    { "name": "window_visible", "ok": true },
    { "name": "close_confirmation_disabled", "ok": true }
  ]
}
```

A failed check includes an `error` string. Session mode adds
`session_secrets` (when requested), `host_key`, `known_host_trust`, and
`<kind>_host_create`, `<kind>_session_open`, `<kind>_command`, and
`<kind>_session_close` for `mosh` and `et`. The app exits `0` only when every
check passed; a failure exits `1` without entering the quit sequence. The
driver also accepts a result file written for an invalid contract (for example,
session mode on Windows) with a failed `activation` check.

The driver is strict about the result it calls green. It rejects a missing or
empty `checks` list, a `mode` that differs from `--mode`, a missing `platform`,
and a platform that does not match the driver host's GOOS/GOARCH (`macos` maps
to `darwin`, `aarch64` to `arm64`, and `x86_64` to `amd64`). It also requires
every check the selected mode must produce to be present and true: launch
requires `window_visible`; session requires `host_key`, `known_host_trust`,
`mosh_host_create`, `mosh_session_open`, `mosh_command`, `mosh_session_close`,
`et_host_create`, `et_session_open`, `et_command`, `et_session_close`, and
`close_confirmation_disabled`, plus `session_secrets` when `--session-secrets`
was requested. A missing result, malformed JSON, non-zero exit, or any failed
check still fails as before.

### What each platform proves

| Artifact | Launch | Session | Notes |
| --- | --- | --- | --- |
| macOS DMG (CI universal build; releases publish `natcatty-<version>-macos-arm64.dmg` and `natcatty-<version>-macos-amd64.dmg`) | Yes | Yes, against local stock `sshd`/`etserver` (and `mosh-server` for Mosh) | `hdiutil attach`, run `Natcatty.app/Contents/MacOS/Natcatty`, detach. |
| Linux `.tar.gz` (`natcatty-<version>-linux-<arch>.tar.gz`) | Yes | Yes, with a lab server on the runner | Extracts the tarball and runs under `xvfb-run` when no display is available. |
| Windows portable zip (`natcatty-<version>-windows-amd64-portable.zip`; the artifact-tests workflow builds a windows-arm64 artifact that is not published) | Yes | No by default | Launch mode asserts the window; session mode fails closed with an `activation` result unless the lab escape hatch is set. |

Windows arm64 is **launch-only**: hosted arm64 runners cannot provide native
arm64 mosh/ET servers, and the Windows GUI gives CI no stdout. The
`launch-windows-arm64` job in `.github/workflows/artifact-tests.yml` asserts
that the packaged app reaches a visible window; the result file plus exit code
are the evidence. Full Windows arm64 session coverage remains a ticket-35 gap. Do
not treat a green launch result as session evidence. The Windows arm64 package
is CI-only and is not part of the release asset set.

### Running the driver locally

```sh
# Launch test on a packaged artifact.
nu scripts/artifact-tests.nu dist/natcatty-0.1.0-macos-arm64.dmg \
  --mode launch --result build/artifact-tests/launch.json

# Session test against a lab server (macOS/Linux).
NATCATTY_SMOKE_PASSWORD=... \
nu scripts/artifact-tests.nu dist/natcatty-0.1.0-linux-arm64.tar.gz \
  --mode session --result build/artifact-tests/session.json \
  --ssh-port 2222 --et-port 2022 --session-secrets
```

The app binary is discovered inside the artifact (`*.app/Contents/MacOS/*`,
`natcatty`/`Natcatty`, or `Natcatty.exe`). An unexpected layout fails with the
paths the driver searched. The driver reads the secret only from
`NATCATTY_SMOKE_PASSWORD`; it is not a command-line argument.

## Release workflow

`.github/workflows/release.yml` runs on `workflow_dispatch` with an explicit
tag or on a pushed `v*` tag. A pushed tag publishes a GitHub release; a
`workflow_dispatch` run validates and builds only, as a dry run. There is no
workflow input that selects a commit: every job checks out `github.sha`, the
commit of the pushed tag or the dispatched ref, and `validate` asserts that
`git rev-parse HEAD` equals it.

1. **validate** reads the version and pre-release flag from `mygo.json` with
   `nu scripts/release.nu`, refuses updater or signing environment variables,
   and pins the release commit to `github.sha`.
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
   with `nu scripts/release.nu collect`, asserts the exact eight-file set, and
   generates one aggregate checksum manifest with relative names. Both the
   platform artifacts and the aggregate manifest are uploaded as workflow
   artifacts. It depends on `build`, so a failed build prevents the release set
   from being assembled.
4. **publish** runs only for a pushed tag, depends on `validate`, `build`, and
   `checksums`, and is the only job with `contents: write`. Its condition is
   `success() && github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')`;
   it never uses `if: always()`, so a failed or skipped prerequisite blocks
   publication even when the tag condition is true. It downloads the platform
   artifacts and flattens them again. It regenerates the aggregate checksum
   manifest and requires it to be byte-identical to the one the `checksums`
   job produced. It then creates a GitHub release titled
   `Natcatty <version>` for the tag, or edits the existing release when the
   tag is re-pushed or force-moved, and attaches the release assets with
   `--clobber`. The body is generated by GitHub's release-notes API from the
   tag's commits and merged pull requests; it is not read from the repository.
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
is not written by hand and is not derived from `CHANGELOG.md`. The assets are
the eight installers and the checksum manifest:

- `natcatty-<version>-macos-arm64.dmg`
- `natcatty-<version>-macos-amd64.dmg`
- `natcatty-<version>-windows-amd64-setup.exe`
- `natcatty-<version>-windows-amd64-portable.zip`
- `natcatty-<version>-linux-amd64.tar.gz`
- `natcatty-<version>-linux-arm64.tar.gz`
- `natcatty-<version>-linux-amd64.deb`
- `natcatty-<version>-linux-arm64.deb`
- the checksum manifest, which lists all eight with relative names

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
tag, the in-process Mosh and EternalTerminal attributions, and the warranty
disclaimer. The build job
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
