# Host/Shell capture matrix and release gates

This is the release-acceptance view of ticket 21. "Host" here is the operating
system in the Host/Shell matrix, not a saved Host target in the Vault. The
executable source of truth is
`adapters/shellhook/testdata/capture-matrix.json`, checked by
`TestHostShellMatrix`, `TestHostShellReleaseGates`, and
`TestHostShellMatrixRejectsBadFixtures` in
`adapters/shellhook/capture_matrix_test.go`. The fixture extends the ticket 07
capture matrix; it is not a second matrix.

Capture tiers:

- **Tier 1** — the session-scoped Netcatty hook supplies the command, cwd, exit
  code, and duration.
- **Tier 2** — the shell's own OSC 133/7 integration supplies what it can
  without a Netcatty hook; usually the cwd, never an exit code from Netcatty.
- **Tier 3** — typed input plus prompt detection. The command and a finish are
  captured; cwd and exit code are absent.

`observed` in a cell means a real shell ran on the named Host and architecture
and the cited test asserted the fidelity. `manual` means this environment could
not run the cell; the cell records the runner, exact steps, startup flags, and
expected fidelity, but never claims a result. A path-level `verification` only
covers the platforms in its `observedPlatforms`; per-platform truth lives in
the cells.

## Matrix

| Host/arch | zsh | bash | fish | nushell | xonsh | PowerShell | Tier 3 fallback |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Linux amd64 | T1 observed, gate | T1 observed, gate | T1 observed, gate | T1 observed | T1 observed | T1 manual, gate | T3 observed, gate |
| Linux arm64 | T1 observed, gate | T1 observed, gate | T1 observed, gate | T1 observed | T1 observed | T1 manual, gate | T3 observed, gate |
| macOS arm64 | T1 observed, gate | T1 observed, gate | T1 observed, gate | T1 observed | T1 observed | T1 observed, gate | T3 observed, gate |
| macOS x86_64 | T1 manual, gate | T1 manual, gate | T1 manual, gate | T1 manual | T1 manual | T1 manual, gate | T3 manual, gate |
| Windows x86_64 | T3 manual | T3 manual | T3 manual | T2 manual | T2 manual | T1 manual, gate | T3 manual, gate |
| Windows arm64 | T3 manual | T3 manual | T3 manual | T2 manual | T2 manual | T1 manual, gate | T3 manual, gate |
| FreeBSD amd64 | T1 manual, gate | T1 manual, gate | T1 manual, gate | T1 manual | T1 manual | out of scope | T3 manual, gate |

Notes:

- PowerShell on BSD is out of scope: BSD is in scope for POSIX shells.
- nushell and xonsh are best-effort on every Host and are never release gates,
  whatever tier they reach. They are Tier 1 where the hook takes, Tier 2 where
  only the native integration works, and Tier 3 on Windows POSIX shells.
- Windows zsh, bash, and fish keep their POSIX startup and receive no Netcatty
  hook, so they are Tier 3. Their manual check is in the Windows section below.
- macOS arm64 PowerShell is an observed POSIX-Host cross-check. It is **not**
  Windows fidelity; only the Windows cells establish that.

## Release gates

Every gate below is `releaseBlocking` in the fixture. The checker fails when a
gate is missing, is not marked release-blocking, is classified below the
required tier, or has only a `planned` check. The full list is the
`requiredHostShellGates` table in `capture_matrix_test.go`.

| Gate cell | Tier | Verification | Exact startup | Expected fidelity (cwd/exit/duration) |
| --- | --- | --- | --- | --- |
| linux-amd64-zsh | 1 | observed | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| linux-amd64-bash | 1 | observed | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| linux-amd64-fish | 1 | observed | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| linux-amd64-powershell | 1 | manual | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| linux-amd64-tier3-fallback | 3 | observed | `zsh -l`, hook disabled | missing/missing/observed |
| linux-arm64-zsh | 1 | observed | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| linux-arm64-bash | 1 | observed | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| linux-arm64-fish | 1 | observed | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| linux-arm64-powershell | 1 | manual | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| linux-arm64-tier3-fallback | 3 | observed | `zsh -l`, hook disabled | missing/missing/observed |
| macos-arm64-zsh | 1 | observed | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| macos-arm64-bash | 1 | observed | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| macos-arm64-fish | 1 | observed | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| macos-arm64-powershell | 1 | observed | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| macos-arm64-tier3-fallback | 3 | observed | `zsh -l`, hook disabled | missing/missing/observed |
| macos-amd64-zsh | 1 | manual | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| macos-amd64-bash | 1 | manual | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| macos-amd64-fish | 1 | manual | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| macos-amd64-powershell | 1 | manual | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| macos-amd64-tier3-fallback | 3 | manual | `zsh -l`, hook disabled | missing/missing/observed |
| windows-amd64-powershell | 1 | manual | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| windows-amd64-tier3-fallback | 3 | manual | `pwsh`, no flags, hook disabled | missing/missing/observed |
| windows-arm64-powershell | 1 | manual | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| windows-arm64-tier3-fallback | 3 | manual | `pwsh`, no flags, hook disabled | missing/missing/observed |
| bsd-amd64-zsh | 1 | manual | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| bsd-amd64-bash | 1 | manual | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| bsd-amd64-fish | 1 | manual | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| bsd-amd64-tier3-fallback | 3 | manual | `zsh -l`, hook disabled | missing/missing/observed |

`<hook-dir>` is a session-scoped temporary directory. The user's own startup
files are sourced, never edited: zsh preserves the original `ZDOTDIR` in
`NETCATTY_USER_ZDOTDIR`, bash sources the user's profile and rc once, and fish
sources the user config before the hook. nushell uses
`nu -l -e 'source "<hook-dir>/hook.nu"'`; xonsh uses `xonsh -l` with
`XONSHRC_DIR=/srv/xonsh/rc.d:<hook-dir>`. Neither is release-blocking.

PowerShell's `<bootstrap>` is the outer `-NoProfile` process. It validates the
hook in a short-lived child:

```
pwsh -NoProfile -NoLogo -NonInteractive -ExecutionPolicy Bypass -File <hook-dir>/hook.ps1
```

with `NETCATTY_HOOK_VALIDATE=1`. On success it starts the interactive child:

```
pwsh -NoLogo -NoExit -ExecutionPolicy Bypass -File <hook-dir>/hook.ps1
```

with `NETCATTY_HOOK_START=1` and
`NETCATTY_HOOK_STATE=<hook-dir>/hook-loaded`. If validation fails (for example
a machine policy refuses `-File`), it writes the skip marker and starts
`pwsh -NoLogo -NoExit` without the hook, which leaves capture at Tier 2/3
instead of ending the Session.

## Observed runs

These are the only runs recorded by this environment. They ran on 2026-11-08.
The exact command strings, including the nix-store PATH entries, are in the
fixture's `observedRuns`; the table below names the same runs.

| Run | Host | Shells and versions | Result |
| --- | --- | --- | --- |
| macos-arm64-local-2026-11-08 | macOS 27.2, arm64 | zsh 5.9.2, bash 5.3.20, fish 4.9.3, nushell 0.116.1, xonsh 0.24.2, pwsh 7.6.9 | PASS, 45.884s |
| macos-arm64-bash32-local-2026-11-08 | same | `/bin/bash` 3.2.57 | PASS, 0.09s |
| macos-arm64-native-fallback-local-2026-11-08 | same | nushell 0.116.1, xonsh 0.24.2 | PASS, 5.512s |
| linux-arm64-docker-2026-11-08 | Linux arm64, Docker 29.8.2 (OrbStack), Debian 12.15 | zsh 5.9, bash 5.2.15, fish 3.6.0, nushell 0.116.1, xonsh 0.24.2 | PASS, 28.075s |
| linux-amd64-docker-2026-11-08 | Linux amd64, same runtime and image | zsh 5.9, bash 5.2.15, fish 3.6.0, nushell 0.116.1, xonsh 0.24.2 | PASS, 30.395s |

Both Linux runs used `golang:1.27-bookworm` pinned to
`sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0`.
The arm64 container was `natcatty-debug`; the amd64 container was
`natcatty-debug-amd64`. nushell was installed from the matching
`nu-0.116.1-<arch>-unknown-linux-gnu.tar.gz`; xonsh from a pip install under
`/opt/xonsh`.

The exact commands were:

```
nix develop --command bash -c 'export PATH=/nix/store/89xsm9bg4z03cgbbxlcz036df1n1jzzk-fish-4.9.3/bin:/nix/store/sg4qjfh6qyj58zgcrscwp2bg18lb2a64-xonsh-0.24.2/bin:/nix/store/hx0gqai3qzgah05nmzdv1d87vnz1qkf9-powershell-7.6.9/bin:$PATH; GOFLAGS=-buildvcs=false go test ./adapters/shellhook -run "^(TestRealShellSessionCapturesTierOneEndToEnd|TestRealNushellSessionCapturesTierOneEndToEnd|TestRealXonshSessionCapturesTierOneEndToEnd|TestRealPowerShellLocalHookCapturesTierOne|TestRealTierThreeFallbackCapturesTypedInput|TestRealPowerShellTierThreeFallback)$" -count=1 -v'
```

```
nix develop --command bash -c 'GO=$(command -v go); export PATH=/bin:/usr/bin:/usr/sbin:/sbin; GOFLAGS=-buildvcs=false "$GO" test ./adapters/shellhook -run "TestRealShellSessionCapturesTierOneEndToEnd/bash" -count=1 -v'
```

```
nix develop --command bash -c 'export PATH=/nix/store/sg4qjfh6qyj58zgcrscwp2bg18lb2a64-xonsh-0.24.2/bin:$PATH; GOFLAGS=-buildvcs=false go test ./adapters/shellhook -run "^(TestRealNushellWithoutHookKeepsNativeCapture|TestRealXonshWithoutHookKeepsNativeFallback)$" -count=1 -v'
```

```
docker exec natcatty-debug bash -c 'export PATH=/opt/xonsh/bin:$PATH; GOFLAGS=-buildvcs=false go test ./adapters/shellhook -run "^(TestRealShellSessionCapturesTierOneEndToEnd|TestRealNushellSessionCapturesTierOneEndToEnd|TestRealXonshSessionCapturesTierOneEndToEnd|TestRealTierThreeFallbackCapturesTypedInput)$" -count=1 -v'
```

```
docker exec natcatty-debug-amd64 bash -c 'export PATH=/opt/xonsh/bin:$PATH; GOFLAGS=-buildvcs=false go test ./adapters/shellhook -run "^(TestRealShellSessionCapturesTierOneEndToEnd|TestRealNushellSessionCapturesTierOneEndToEnd|TestRealXonshSessionCapturesTierOneEndToEnd|TestRealTierThreeFallbackCapturesTypedInput)$" -count=1 -v'
```

Observed fidelity:

- zsh, bash, and fish Tier 1: exact command, cwd, exit code, and duration.
  bash was checked with both 5.3.20 and macOS `/bin/bash` 3.2.57.
- nushell and xonsh Tier 1: exact command, cwd, exit code, and duration.
- nushell Tier 2 without the hook: exact cwd, no exit code, observed duration.
- xonsh Tier 2 without the hook: exact cwd, no exit code, no duration; its
  prompt is not prompt-detected, so Session close leaves the finish unknown.
- zsh Tier 3 without the hook: typed command and a measured duration; no cwd
  and no exit code.
- pwsh 7.6.9 Tier 1 on macOS arm64: exact command, cwd, exit code, and
  duration, with exactly one profile load. This is not Windows evidence.
- pwsh 7.6.9 Tier 3 on macOS arm64: typed command and duration; no cwd and no
  exit code.

## Manual acceptance procedures

These procedures are release acceptance steps. Run them for every release
that changes capture, startup, or the matrix. Record the runner/image, exact
versions, commands, and result in the release log. Do not mark a cell
`observed` until a run exists in `observedRuns`.

### Linux PowerShell (manual)

Runner: Docker `golang:1.27-bookworm` pinned to
`sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0`,
for `linux/amd64` and `linux/arm64`, plus PowerShell 7.6.9 from the official
`linux-<arch>` tarball.

```
docker run --platform linux/amd64 --rm -it -v "$PWD":/work -w /work golang:1.27-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 bash
# inside the container, after installing pwsh 7.6.9:
pwsh --version
GOFLAGS=-buildvcs=false go test ./adapters/shellhook -run '^TestRealPowerShellLocalHookCapturesTierOne$' -count=1 -v
```

Repeat with `--platform linux/arm64` and the `linux-arm64` PowerShell tarball.
Startup flags are the PowerShell bootstrap above. Expected fidelity is
exact/exact/exact. This cell is release-blocking; the hook is the same one
observed on macOS arm64, but the Linux run is the acceptance evidence.

### macOS x86_64 (manual)

Runner: GitHub Actions `macos-15-intel` (x86_64). Record `sw_vers` and
`uname -m`. Install fish 4.9.3, nushell 0.116.1, xonsh 0.24.2, and PowerShell
7.6.9 (`osx-x64` tarball); record every `--version`.

```
go test ./adapters/shellhook -run '^(TestRealShellSessionCapturesTierOneEndToEnd|TestRealNushellSessionCapturesTierOneEndToEnd|TestRealXonshSessionCapturesTierOneEndToEnd|TestRealTierThreeFallbackCapturesTypedInput)$' -count=1 -v
go test ./adapters/shellhook -run '^TestRealPowerShellLocalHookCapturesTierOne$' -count=1 -v
```

Expected: zsh, bash, fish, and PowerShell at Tier 1 with exact cwd, exit code,
and duration; nushell and xonsh at Tier 1; the Tier 3 fallback with no cwd and
no exit. Startup flags are the POSIX and PowerShell entries in the gate table.
All six zsh/bash/fish/PowerShell cells are release-blocking.

### Windows runners (manual)

Runners: GitHub Actions `windows-2025` (x86_64) and `windows-11-arm` (arm64).
Record `$env:ImageOS`, `$env:ImageVersion`, `$PSVersionTable.PSVersion`, and
the PSReadLine version. Use `actions/setup-go@v5` with
`go-version-file: go.mod`.

PowerShell Tier 1:

```
go test ./adapters/shellhook -run '^TestRealPowerShellLocalHookCapturesTierOne$' -count=1 -v
```

PowerShell Tier 3 fallback (hook disabled):

```
go test ./adapters/shellhook -run '^TestRealPowerShellTierThreeFallback$' -count=1 -v
```

Expected: Tier 1 has the typed command, cwd, exit code, and exactly one
profile load; the fallback has no cwd and no exit code, finished by the profile
prompt. Both cells are release-blocking. Startup flags: the PowerShell
bootstrap for Tier 1; plain `pwsh` with no flags for the fallback.

nushell and xonsh (best-effort, non-blocking):

```
winget install --id Nushell.Nushell --exact
nu --version
set NETCATTY_TEST_NU to the installed nu.exe
go test ./adapters/shellhook -run '^TestRealNushellWithoutHookKeepsNativeCapture$' -count=1 -v

py -3 -m pip install xonsh==0.24.2
xonsh --version
set NETCATTY_TEST_XONSH to the xonsh executable
go test ./adapters/shellhook -run '^TestRealXonshWithoutHookKeepsNativeFallback$' -count=1 -v
```

Expected Tier 2 with the native cwd and no exit code. The Windows planner does
not hook either shell.

zsh, bash, and fish (best-effort, non-blocking, Tier 3):

1. Install the shell: bash ships with Git for Windows; install zsh and fish
   from MSYS2 (`winget install MSYS2.MSYS2`, then
   `pacman -S --needed zsh fish`). Record each `--version`.
2. Put the shell, or the MSYS2 `usr/bin`, on PATH. Set `NETCATTY_TEST_FISH`
   when fish is not on PATH.
3. In Netcatty open **Settings > Logs** and confirm **History capture hook**
   is off. The Windows planner only installs the hook for PowerShell, so
   zsh/bash/fish run unhooked either way; the setting makes the check explicit.
4. Open a local session that starts the shell with `-l`.
5. Type `echo netcatty_tier3_matrix`, press Enter, and require a Command
   History row with the typed command, no cwd, no exit code, and a measured
   duration (Tier 3).
6. Turn the capture hook back on before the next cell if you turned it off.

Startup flags: `zsh -l`, `bash -l`, `fish -l`. Expected fidelity:
missing/missing/observed. These cells are not release gates.

### BSD VM (manual)

Runner: FreeBSD 14.2-RELEASE amd64, in bhyve, UTM, or QEMU. Record the image
name and checksum, `uname -a`, and `freebsd-version -ku`.

```
pkg install -y zsh bash fish
# install Go 1.27.3 or newer (the pkg version may be older; use the upstream
# freebsd-amd64 tarball when needed), then nushell 0.116.1 and xonsh 0.24.2
go version
go test ./adapters/shellhook -run '^(TestRealShellSessionCapturesTierOneEndToEnd|TestRealNushellSessionCapturesTierOneEndToEnd|TestRealXonshSessionCapturesTierOneEndToEnd|TestRealTierThreeFallbackCapturesTypedInput)$' -count=1 -v
```

Set `NETCATTY_TEST_FISH`, `NETCATTY_TEST_NU`, or `NETCATTY_TEST_XONSH` when the
shell is not on PATH. Expected: zsh, bash, and fish Tier 1 with exact cwd, exit
code, and duration; nushell and xonsh Tier 1; the Tier 3 fallback with no cwd
and no exit. Startup flags are the POSIX entries in the gate table. PowerShell
is out of scope for BSD. The zsh/bash/fish and fallback cells are
release-blocking.

### Promoting a manual cell to observed

1. Run the cell on its real Host and architecture with the exact startup flags
   recorded in the cell.
2. Add an `observedRuns` entry with `harness` (`local`, `docker`,
   `github-actions`, or `vm`), `hostOS`, `arch`, `platform`, `runtime`,
   `runner` or `image` + `imageDigest`, `shellVersions` keyed by the shell,
   the exact `command`, the `result`, and `recordedAt`.
3. Change the cell's `verification` to `observed`, fill `observed.runs` with
   the run ID, `observed.test` with a test that exists in this package, and
   `observed.fidelity` with what the test asserted. Delete `manual`.
4. Keep `startup` exact. The checker rejects a mismatched Host/architecture, a
   missing shell version, a Docker run without a sha256 digest, an invented
   test name, and a manual cell that carries observation data.

The run command must contain the cited test name. A macOS pwsh run can never
support a Windows cell.

## CI

`ci.yml` runs the checker in the `checks` job on every pull request and push
to main, and `release.yml` runs it in the `validate` job before building:

```
go test ./adapters/shellhook -run '^(TestCaptureMatrix|TestHostShellMatrix|TestHostShellReleaseGates|TestHostShellMatrixRejectsBadFixtures)$' -count=1 -v
```

This step is pure Go and JSON, so it runs on Linux, macOS, and Windows runners
without any real shell. It fails when a release-blocking cell is missing, is
not marked release-blocking, is classified below its required tier, has only a
planned check, claims an observation that its run does not support, records
startup flags that disagree with its path, or claims a fidelity its tier
cannot produce. The real-shell tests remain in `go test ./...` and in the
manual acceptance procedures above; the release pipeline does not pretend the
manual cells ran.

`TestCaptureMatrix` still replans every path on each recorded platform, so a
startup-file or argument change cannot drift from the fixture.
`TestHostShellMatrix` validates the cells; `TestHostShellMatrixRejectsBadFixtures`
proves the checker rejects a missing cell, a wrong tier, and a false observed
claim.
