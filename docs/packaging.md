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

## Packaged smoke tests

Ticket 34 Phase 3A adds a hidden smoke mode to the normal, untagged app. The
binary that ships is the binary CI exercises; there is no `launchsmoke`-style
build tag for this path. The mode is dormant unless `NATCATTY_SMOKE` is exactly
`1`; the boolean flags below are exact for the same reason.

`scripts/packaged-smoke.nu` unwraps the shipped artifact, launches the app,
waits for its exit, and validates the result JSON. Phase 3B adds
`.github/workflows/packaged-smoke.yml`, which runs this driver on macOS, Linux,
and Windows arm64. See [Packaged smoke tests in CI](ci/packaged-smoke.md) for
the six jobs, pinned lab servers, dispatch instructions, and residual CI-only
risks. The workflow is not wired into `release.yml`, publishes nothing, and
this document makes no claim that it has run.

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
store is missing. Smoke mode skips the real shell-history import and does not
register the `ssh` URL handler, so a temporary artifact cannot change the
user's handler.

Session mode uses the current OS user as the SSH account. It creates two Hosts
in the temporary profile (`127.0.0.1`, the configured SSH port, the current
user, the lab secret) with `TransportImplementation=in_process` explicitly,
so the per-Host choice wins even while the release default is still `bundled`.
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
| macOS DMG (`natcatty-<version>-macos-universal.dmg`) | Yes | Yes, against local stock `sshd`/`etserver` (and `mosh-server` for Mosh) | `hdiutil attach`, run `Natcatty.app/Contents/MacOS/Natcatty`, detach. |
| Linux `.tar.gz` (`natcatty-<version>-linux-<arch>.tar.gz`) | Yes | Yes, with a lab server on the runner | Extracts the tarball and runs under `xvfb-run` when no display is available. |
| Windows portable zip (`natcatty-<version>-windows-amd64-portable.zip`; the packaged-smoke workflow builds a windows-arm64 artifact that is not published) | Yes | No by default | Launch mode asserts the window; session mode fails closed with an `activation` result unless the lab escape hatch is set. |

Windows arm64 is **launch-only**: hosted arm64 runners cannot provide native
arm64 mosh/ET servers, and the Windows GUI gives CI no stdout. The
`launch-windows-arm64` job in `.github/workflows/packaged-smoke.yml` asserts
that the packaged app reaches a visible window; the result file plus exit code
are the evidence. Full Windows arm64 session smoke remains a ticket-35 gap. Do
not treat a green launch result as session evidence. The Windows arm64 package
is CI-only and is not part of the release asset set.

### Running the driver locally

```sh
# Launch smoke on a packaged artifact.
nu scripts/packaged-smoke.nu dist/natcatty-0.1.0-macos-universal.dmg \
  --mode launch --result build/packaged-smoke/launch.json

# Session smoke against a lab server (macOS/Linux).
NATCATTY_SMOKE_PASSWORD=... \
nu scripts/packaged-smoke.nu dist/natcatty-0.1.0-linux-arm64.tar.gz \
  --mode session --result build/packaged-smoke/session.json \
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

1. **validate** runs the legal package tests (`go test ./internal/legal`), reads
   the version and pre-release flag from `mygo.json` with
   `nu scripts/release.nu`, refuses updater or signing environment
   variables, and pins the release commit to `github.sha`.
2. **mosh-parity** and **et-parity** call the reusable
   `.github/workflows/mosh-parity.yml` and `.github/workflows/et-parity.yml`
   workflows for the release commit. Reusable workflow calls inherit the
   caller's `GITHUB_RUN_ID`, so every cell report and fuzz receipt is bound to
   this run; the called workflows also use the caller's `github.sha` for
   checkout. Neither call job accepts inputs, so a release cannot point the
   gates at another commit. See [Release parity gate](#release-parity-gate).
3. **build** runs on `macos-latest`, `windows-latest`, and `ubuntu-latest`,
   writes `resources/SOURCE-OFFER.md` for the manifest version with
   `go run ./internal/tools/sourceoffer`, builds with MyGo, renames every
   release artifact to its canonical name with
   `nu scripts/release.nu rename-assets`, and removes MyGo's standalone Linux
   `install.sh`. Windows also gets the portable archive, built from the app
   directory without the Setup executable by
   `nu scripts/release.nu make-portable`. Build and package jobs remain
   independent of the parity gates; their Actions artifacts are internal and
   cannot be published before the gate below passes. Every platform then
   verifies the artifact's layout and required resources:
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
4. **parity-verify** belongs to the release commit and requires both reusable
   gate jobs to report `success`. It downloads the `mosh-parity-*` and
   `et-parity-*` artifacts, requires both `summary.json` files, asserts the
   checkout is `github.sha`, and runs
   `go run ./internal/tools/moshparity verify -require-summary` and
   `go run ./internal/tools/etparity verify -require-summary` over the merged
   report directories. The job runs even after a failed gate so it can report
   which gate failed, but its first step exits non-zero for any non-`success`
   gate result; there is no `always()` waiver on a downstream release job.
5. **checksums** downloads the platform artifacts, flattens the release set
   with `nu scripts/release.nu collect`, asserts the exact seven-file set, and
   generates one aggregate checksum manifest with relative names. Both the
   platform artifacts and the aggregate manifest are uploaded as workflow
   artifacts. It depends on `build` and `parity-verify`, so a failed gate
   prevents the release set from being assembled.
6. **publish** runs only for a pushed tag, depends on `validate`, `build`,
   `checksums`, and `parity-verify`, and is the only job with
   `contents: write`. Its condition is
   `success() && github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')`;
   it never uses `if: always()`, so a failed or skipped parity gate blocks
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

### Release parity gate

Ticket 34 may switch the in-process transports to the default only after both
parity gates are green. The release pipeline enforces that contract fail
closed:

- Both `mosh-parity` and `et-parity` are required. A skipped, cancelled, or
  failed gate has a non-`success` result and blocks `parity-verify`; there is
  no waiver, partial-green, or `if: always()` path around it.
- Both gates consume the same release commit (`github.sha`) and the same CI
  run (`GITHUB_RUN_ID`). ET reports and receipts carry the commit, tree, and
  run ID explicitly; the mosh gate relies on the caller's run-scoped Actions
  artifacts and verifies every report against `-commit github.sha`.
- `parity-verify` requires a `summary.json` from each gate before it runs
  either verifier, then passes `-require-summary` so the summary's schema,
  status, commit, tree (ET), run ID (ET), cell verdicts, and fuzz-target
  verdicts are checked against this run. A missing or mismatched artifact,
  cell, fuzz receipt, commit, tree, or run ID fails the job.
- `etparity verify` writes `summary.json` on both pass and fail; it always
  records the schema, status, commit, tree, run ID, per-cell verdicts with
  behavior counts and problems, per-fuzz-target verdicts with problems, and
  gate-level problems. The ET gate workflow runs verify unconditionally and
  passes `needs.<job>.result` with `-job-result`, so a cell or fuzz job that
  failed after uploading apparently green evidence still fails the gate and is
  recorded in the summary. `moshparity verify` writes its existing summary on
  both pass and fail. Each gate workflow uploads its summary as
  `mosh-parity-summary` / `et-parity-summary` with `if: always()`, so a failed
  gate still leaves a machine-readable verdict on the run.
- `checksums` and `publish` both depend on `parity-verify`. Build/package
  jobs may run in parallel, but no artifact reaches the GitHub release until
  the gate passes; the publish job's explicit `success()` condition is the
  final no-waiver check.

The summaries are downloaded into `build/mosh-parity/summary.json` and
`build/et-parity/summary.json` (and are attached to the run as
`mosh-parity-summary` and `et-parity-summary`). Locally, the same contract can
be checked over a locally produced report directory with:

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
