# Packaged artifact tests in CI

`.github/workflows/artifact-tests.yml` runs the app's hidden packaged-artifact
test mode in the artifact users receive. It covers macOS, Linux, and Windows
arm64. The workflow is deliberately separate from
`.github/workflows/release.yml`: it does not publish anything, does not change
the release asset set, and is not a release gate.

The driver and the environment contract are documented in
[Packaged artifact tests](../packaging.md#packaged-artifact-tests). This page
covers the CI wiring, the pinned lab servers, and the risks that only a real
hosted run can settle.

## Triggers and scope

| Trigger | Behavior |
| --- | --- |
| `workflow_call` | Reusable with no inputs. Nothing in `release.yml` calls it yet. |
| `pull_request` | Runs only when the app, the driver, `scripts/release.nu`, the legal texts, the source-offer tool, `resources/**`, `mygo.json`, `go.mod`, `go.sum`, or this workflow changes. A docs-only change does not run it. |
| `push` to `main` | Runs the full matrix only when one of the same paths changes; an unrelated main push skips the workflow. |
| `workflow_dispatch` | Manual run from the Actions tab or `gh`; it bypasses the path filters. |

The workflow has `permissions: contents: read` and a concurrency group
(`artifact-tests-<ref>`) that cancels an older run for the same ref. Every
checkout is pinned to `${{ github.sha }}`.

The workflow never fetches transport clients and no client binary is bundled:
both Mosh and ET run in process, and the session jobs exercise those in-process
transports.

## Jobs

| Job | Runner | Timeout | Needs | Actions artifact |
| --- | --- | --- | --- | --- |
| `package-macos` | `macos-latest` | 45 min | - | `artifact-tests-macos` (DMG) |
| `session-macos` | `macos-latest` | 60 min | `package-macos` | `artifact-tests-macos-session` (result, driver log, etserver log) |
| `package-linux` | `ubuntu-latest` | 45 min | - | `artifact-tests-linux` (tarball and `.deb`) |
| `session-linux` | `ubuntu-latest` | 75 min | `package-linux` | `artifact-tests-linux-session` (result, driver log, container log) |
| `package-windows-arm64` | `windows-latest` | 45 min | - | `artifact-tests-windows-arm64` (portable zip) |
| `launch-windows-arm64` | `windows-11-arm` | 20 min | `package-windows-arm64` | `artifact-tests-windows-arm64-launch` (result and profile) |

### `package-macos`

Writes `resources/SOURCE-OFFER.md` for the `mygo.json` version, builds with
`go tool mygo build -platform darwin/universal -o dist -sign -` (ad hoc
signature only), renames the DMG to its canonical name with
`scripts/release.nu`, and verifies both the built `Natcatty.app` and the
mounted DMG. Both must carry `LICENSE`, `NOTICE`,
`THIRD-PARTY-NOTICES.md`, `SOURCE-OFFER.md`, `CHANGELOG.md`, `mygo.json`, the
ET provenance/license/README files, and must not contain an
`et/darwin-universal` client binary.

### `session-macos`

Downloads the DMG and provisions a native lab on the runner:

- Stock EternalTerminal 7.1.1, cloned at tag `et-v7.1.1` and checked against
  commit `a8318ca4598756ca3c5f4711cce12a65faeff57d`, built with the same
  Homebrew dependencies and CMake flags as the `macos-native` job in
  `et-parity.yml`. `etserver`, `et`, and `etterminal` are installed into
  `/usr/local/bin`.
- The system sshd is provisioned by `adapters/et/lab/macos-provision.sh`.
  Session mode authenticates as the provisioned `tester` account
  (`NATCATTY_SMOKE_SSH_USER`) with `NATCATTY_SMOKE_PASSWORD`: the runner
  account carries a Secure Token, so macOS refuses a scripted password reset
  even from root. The app itself still runs as the runner user.
- The Homebrew `mosh-server` is linked into `/usr/local/bin`, which is always
  on the remote PATH, so a shell startup file cannot hide it.
- `etserver` listens on port 2022; the driver uses SSH port 22 and ET port
  2022.

The driver runs without `--session-secrets`: macOS runners have an OS
credential store, and session-only mode deliberately fails closed when the OS
store is available. The result must report `mode=session`, a non-empty
platform, and true `mosh_command` and `et_command` checks.

### `package-linux`

Writes the source offer, builds `linux/amd64`, renames the `.deb` with
`scripts/release.nu`, and verifies the tarball carries `natcatty`, `mygo.json`,
`CHANGELOG.md`, `THIRD-PARTY-NOTICES.md`, `LICENSE`, `NOTICE`,
`SOURCE-OFFER.md`, and the ET provenance, licenses, and README. It must not
contain an `et/linux-x64` client. The tarball and `.deb` are uploaded.

### `session-linux`

Downloads the tarball, installs `xvfb`, `xauth`, and `fonts-noto-cjk`, and
builds the pinned Debian 12 ET 7.1.1 lab image with
`docker compose -f adapters/et/lab/compose.yaml build debian12-et711`.
The image is run directly from its deterministic name,
`natcatty-et-parity-debian12-et711:latest`, with
`127.0.0.1:2322:22` and `127.0.0.1:24607:24607/udp`; ET port 2022 stays
container-internal because the ET client reaches it through SSH direct-tcpip
forwarding.

Inside the container the job installs mosh 1.4.0 from Debian, asserts the
version, creates an account matching `$USER` with the lab secret, and installs
`/usr/local/bin/mosh-server` as a wrapper that forces `-p 24607 -i 0.0.0.0`
while passing the client's first `new` argument through. The driver runs under
`xvfb-run` (the driver selects it automatically when no display exists) with
`--ssh-port 2322 --et-port 2022 --session-secrets`.

### `package-windows-arm64`

Reads the version from `mygo.json` and writes the source offer, then builds
`windows/arm64`. NSIS may be present on the runner; the installer is irrelevant
to this job.

The job asserts `dist/windows-arm64/Natcatty.exe` exists and the compliance
files are present. It then creates
`natcatty-<version>-windows-arm64-portable.zip` with PowerShell, excluding any
installer (both a canonical `*-setup.exe` and MyGo's raw
`Natcatty Setup <version> arm64.exe`). The archive is verified to contain the
app, resources, and compliance files, and no installer.

This artifact is CI-only. It is not added to `release.yml` or the published
asset set.

### `launch-windows-arm64`

Runs on `windows-11-arm`, downloads and extracts the portable zip with
PowerShell `Expand-Archive`, and asserts `Natcatty.exe` is present. It starts
`Natcatty.exe` with `Start-Process -PassThru`, waits with
`Wait-Process -Timeout 180`, and requires a result with `mode=launch`,
`platform=windows/arm64`, a true `window_visible` check, and exit code 0. The
result and isolated profile are uploaded even when the step fails.

Nushell and 7z are not assumed on the arm64 runner; the launch job uses only
PowerShell and .NET.

## Pinned lab servers and versions

| Component | Pin | Where |
| --- | --- | --- |
| EternalTerminal | tag `et-v7.1.1`, commit `a8318ca4598756ca3c5f4711cce12a65faeff57d` | macOS source build; Debian 12 lab image |
| Debian ET lab base | `debian:12-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3` | `adapters/et/lab/compose.yaml` |
| mosh (macOS) | Homebrew stable at run time (expected 1.4.0) | `brew install mosh` |
| mosh (Linux lab) | Debian 12 `mosh` package, asserted to be 1.4.0 | container `apt-get` |
| sshd | macOS system sshd; Debian `openssh-server` from the lab image | `macos-provision.sh`; lab entrypoint |
| UDP port | 24607 | Linux mosh wrapper; Homebrew `mosh-server` on macOS |
| ET server port | 2022 | `etserver` |

The ET source commit check fails the job if the tag moves. The Debian base
digest is the same pin the ET parity matrix uses.

## Why Windows arm64 is launch-only

There are no native arm64 stock `mosh-server` or `etserver` binaries in the
lab, and a hosted Windows arm64 runner cannot execute the x64 lab servers.
Windows GUI builds also have no usable stdout, so the result file and exit
code are the evidence. A green `launch-windows-arm64` run proves that the
arm64 package starts and reaches a visible main window; it is not session
evidence. Full Windows arm64 session coverage remains a ticket-35 gap.

## Dispatching on a branch

After the workflow exists on the default branch, dispatch it on another branch
with:

```sh
gh workflow run artifact-tests.yml --ref <branch>
gh run list --workflow artifact-tests.yml
```

`workflow_dispatch` only appears for a workflow GitHub has registered from the
default branch. A `pull_request` run starts automatically when one of the
path-filtered changes above lands, and a manual dispatch runs on any branch.
The `workflow_call` trigger has no inputs and is reserved for a future release
integration; `release.yml` does not call it now.

## Local reproduction

The same driver runs locally. A launch test needs only a built package:

```sh
go tool mygo build -platform darwin/universal -o dist -sign -
nu scripts/artifact-tests.nu "dist/darwin-universal/Natcatty 0.1.0-beta.1.dmg" \
  --mode launch --result build/artifact-tests/launch.json
```

A session test needs a lab endpoint. The CI jobs show the exact macOS and
Linux provisioning. A local container can mirror `session-linux` on different
ports, for example SSH 2522 and UDP 25607:

```sh
docker compose -f adapters/et/lab/compose.yaml build debian12-et711
docker run -d --name natcatty-artifact-tests-local \
  -p 127.0.0.1:2522:22 -p 127.0.0.1:25607:24607/udp \
  natcatty-et-parity-debian12-et711:latest
# install mosh 1.4.0, create a user matching $USER, install the wrapper
# The wrapper forces UDP 24607 inside the container, hence 25607:24607 above.
# The pinned Debian lab ships only C/C.utf8; set a locale it can serve:
export LANG=C.UTF-8 LC_ALL=C.UTF-8
NATCATTY_SMOKE_PASSWORD=<lab-secret> \
  nu scripts/artifact-tests.nu <package> --mode session \
  --result build/artifact-tests/session.json \
  --ssh-port 2522 --et-port 2022 --session-secrets
```

`--session-secrets` is for a headless Linux host with no OS credential store;
on macOS omit it, because session-only mode fails closed when the OS store is
available. The session jobs set `LANG`/`LC_ALL` explicitly for the same reason.

## Host-key capture is pinned to the dialer preference

The capture probe now offers `sshadapter.DefaultHostKeyAlgorithms()`, the same
order the live SSH dialer uses. Without that pin it could record the server's
ECDSA key while the dialer negotiated Ed25519, so the seeded Known Host would
not match and the first session open would wait on a trust prompt until the
dial timeout. The regression test starts a local server with both key types
and fails if the captured type is not the dialer's preferred one.

## Known CI-only residual risks

These are not proven until a real hosted run happens.

- **Runner desktop availability.** A macOS or Windows hosted runner may not
  expose a usable interactive desktop/WindowServer to a GUI app. The test
  would then fail before the window becomes visible. That failure is real
  evidence about the runner, not a package defect, but it blocks the launch
  and session jobs.
- **macOS login keychain.** Session mode uses the OS credential store because
  the runner has one. The test scopes it to a per-profile service name, so the
  app's real entries are never read or written, but a locked or prompt-gated
  keychain can still block the app's main thread. Each local run can leave one
  orphan scoped keychain entry; hosted runners are disposable. During local
  verification, a concurrent test plus a rebuilt ad-hoc-signed app produced a
  `SecItemCopyMatching` wait; that is why the driver supervises the app and
  kills it at `--timeout` instead of relying on a signal a Go process ignores.
- **Lab build time and network.** The Linux session job compiles stock ET
  inside Debian and installs packages; the macOS job clones and compiles ET
  and uses Homebrew. Registry, apt, Homebrew, and GitHub availability can push
  a job past its timeout. The timeouts are deliberately generous but are an
  operational risk on a slow day.
- **Artifact-name immutability.** `actions/upload-artifact@v4` refuses to
  overwrite an artifact with the same name inside one run. Re-running a failed
  job attempt with the static names above can fail closed with a 409. A
  re-run of the entire workflow (new run ID) is unaffected.
- **Signing is not tested.** macOS uses only MyGo's ad hoc signature. There is
  no Developer ID, notarization, or Gatekeeper check. The Windows arm64
  portable app is unsigned and no Authenticode check is performed.
- **Ephemeral account mutation.** The macOS session job changes the current
  runner account's secret because session mode authenticates as the current OS
  user. This is safe only because the hosted runner is disposable; it is not a
  pattern for a persistent machine.
- **Windows arm64 runner preview.** `windows-11-arm` availability and its
  PowerShell/GUI behavior are hosted-runner properties; the job fails closed
  if the label is unavailable.
- **No release wiring.** This workflow is not a release gate. The historical
  default-flip evidence is recorded in
  [transport-rollout.md](../releases/transport-rollout.md#flip-evidence).
  Nothing here publishes a Windows arm64 asset.
