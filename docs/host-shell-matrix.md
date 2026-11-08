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

Evidence kinds in a cell:

- **observed** — a real shell ran on the named Host and architecture, and the
  cited test asserted the fidelity. The run is recorded in `observedRuns`.
- **ci** — the cell is exercised by a job in
  `.github/workflows/release.yml`. It has **not** run on this machine. A
  release-blocking cell's test must run in a required step of a job reachable
  from `publish`. The checker fails if the job is missing, has a job-level `if`
  that can skip it, is continue-on-error, is not reachable from `publish`, does
  not run the cited test in a required step, or does not name the cell's shell
  binding.
- **ci (best-effort)** — a nushell or xonsh cell's tests run in a step with
  `continue-on-error: true`, or in a whole continue-on-error job that is outside
  the publish dependency chain. The checker rejects optional evidence on a
  release-blocking cell, a gate test that appears only in an optional step,
  optional evidence that cites a required-step test, and a whole optional job in
  the publish chain. A failure in the best-effort step is visible in the step
  outcome but cannot fail the job, `checksums`, or `publish`.
- **manual** — a human procedure, recorded with exact commands and expected
  fidelity. It is never an observation. A manual cell must say when it has not
  been run on a real Host, and must not claim PASS.

An observed cell may also carry a `ci` object: the historical observation is
evidence, and the release job is the release-time re-run. A `ci` cell with no
observation makes no claim about this machine. A path-level `verification` only
covers the platforms in its `observedPlatforms`; per-platform truth lives in
the cells.

## Matrix

| Host/arch | zsh | bash | fish | nushell | xonsh | PowerShell | Tier 3 fallback |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Linux amd64 | T1 observed + CI | T1 observed + CI | T1 observed + CI | T1 observed + best-effort CI | T1 observed + best-effort CI | T1 observed (Linux) + CI | T3 observed + CI |
| Linux arm64 | T1 observed + CI | T1 observed + CI | T1 observed + CI | T1 observed + best-effort CI | T1 observed + best-effort CI | T1 observed (Linux) + CI | T3 observed + CI |
| macOS arm64 | T1 observed + CI | T1 observed + CI | T1 observed + CI | T1 observed + best-effort CI | T1 observed + best-effort CI | T1 observed + CI | T3 observed + CI |
| macOS x86_64 | T1 observed (Rosetta) + CI | T1 observed (Rosetta) + CI | T1 observed (Rosetta) + CI | T1 observed (Rosetta) + best-effort CI | T1 observed (Rosetta) + best-effort CI | T1 observed (Rosetta) + CI | T3 observed (Rosetta) + CI |
| Windows x86_64 | T3 manual | T3 manual | T3 manual | T2 manual (unverified) | T2 manual | T1 CI | T3 CI |
| Windows arm64 | T3 manual | T3 manual | T3 manual | T2 manual (unverified) | T2 manual | T1 CI | T3 CI |
| FreeBSD amd64 | T1 CI (SSH) | T1 CI (SSH) | T1 CI (SSH) | T2 best-effort CI (SSH) | T2 best-effort CI (SSH) | out of scope | T3 CI (SSH) |

Notes:

- PowerShell on BSD is out of scope: BSD is in scope for POSIX shells.
- nushell and xonsh are best-effort on every Host and are never release gates,
  whatever tier they reach. Their tests run in a continue-on-error step, so a
  failure cannot fail the job or block `checksums`/`publish`. They are Tier 1
  where the local hook takes, Tier 2 where only the native integration works,
  and Tier 3 on Windows POSIX shells.
- The Windows nushell procedure is **unverified**: it has not been run on a
  Windows machine. The test writes its isolated `config.nu` under
  `XDG_CONFIG_HOME/nushell`. Nushell honors an absolute `XDG_CONFIG_HOME` on
  Windows since nushell/nushell#12118, but a build without that change resolves
  `%APPDATA%\nushell` instead and fails the test's user-config assertion. The
  procedure starts with a preflight that makes this explicit; do not record a
  PASS unless that preflight and the test both pass on the real Host.
- Windows zsh, bash, and fish keep their POSIX startup and receive no Netcatty
  hook, so they are Tier 3. Their manual check is in the Windows section.
- macOS x86_64 was observed with an amd64 test binary and x86_64 shells under
  Rosetta 2 on an arm64 Mac; it is x86_64 userland evidence, not x86_64
  hardware. The release job re-runs the cells on the `macos-15-intel` runner.
- Linux PowerShell is Linux fidelity, not Windows fidelity. Only the Windows
  jobs establish the Windows cells.
- BSD has no local PTY implementation: `platform/pty/pty_other.go` returns
  `errors.ErrUnsupported` on FreeBSD/OpenBSD/NetBSD/DragonFly. A BSD Host is
  therefore reached over SSH, which is the product path, and the acceptance
  tests drive the real planner and Session tap on the client side while the
  capture hook runs remotely. The SSH test transport is `x/crypto/ssh` with a
  remote PTY; the `app` package separately covers the production transport's
  SFTP login-shell probe and wrapper withdrawal. No cgo or untested local PTY
  was added.

## Release gates

Every gate below is `releaseBlocking` in the fixture and is bound to a required
step of a release workflow job. The checker fails when a gate is missing, is
not marked release-blocking, is classified below the required tier, has only a
`planned` check, or cites a job that is absent, conditional, unreachable from
`publish`, does not run the cell's test in a required step, or does not name the
cell's shell binding. The full list is the `requiredHostShellGates` table in
`capture_matrix_test.go`.

The required Linux and macOS step runs
`TestRealShellSessionCapturesTierOneEndToEnd`,
`TestRealPowerShellLocalHookCapturesTierOne`,
`TestRealPowerShellSessionCapturesTierOneEndToEnd`,
`TestRealTierThreeFallbackCapturesTypedInput`, and
`TestRealPowerShellTierThreeFallback`. The PowerShell Session test drives the
real Session stack, asserts the exit code and `StartedAt`/`FinishedAt`, and
requires a measured duration above 250 ms, so the PowerShell Tier 1 cells'
`duration: exact` is backed by an assertion, not inferred from the raw hook
test. Nushell and xonsh run in the sibling `Run the best-effort shells` step
with `continue-on-error: true`; the Windows jobs run the same PowerShell Session
test in their required step, and the FreeBSD job runs the SSH native fallback in
a continue-on-error step.

| Gate cell | Tier | Evidence | Release job / runner | Exact startup | Expected fidelity (cwd/exit/duration) |
| --- | --- | --- | --- | --- | --- |
| linux-amd64-zsh | 1 | observed + CI | capture-linux-amd64 / ubuntu-latest | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| linux-amd64-bash | 1 | observed + CI | capture-linux-amd64 / ubuntu-latest | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| linux-amd64-fish | 1 | observed + CI | capture-linux-amd64 / ubuntu-latest | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| linux-amd64-powershell | 1 | observed + CI | capture-linux-amd64 / ubuntu-latest | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| linux-amd64-tier3-fallback | 3 | observed + CI | capture-linux-amd64 / ubuntu-latest | `zsh -l`, hook disabled | missing/missing/observed |
| linux-arm64-zsh | 1 | observed + CI | capture-linux-arm64 / ubuntu-24.04-arm | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| linux-arm64-bash | 1 | observed + CI | capture-linux-arm64 / ubuntu-24.04-arm | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| linux-arm64-fish | 1 | observed + CI | capture-linux-arm64 / ubuntu-24.04-arm | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| linux-arm64-powershell | 1 | observed + CI | capture-linux-arm64 / ubuntu-24.04-arm | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| linux-arm64-tier3-fallback | 3 | observed + CI | capture-linux-arm64 / ubuntu-24.04-arm | `zsh -l`, hook disabled | missing/missing/observed |
| macos-arm64-zsh | 1 | observed + CI | capture-macos-arm64 / macos-latest | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| macos-arm64-bash | 1 | observed + CI | capture-macos-arm64 / macos-latest | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| macos-arm64-fish | 1 | observed + CI | capture-macos-arm64 / macos-latest | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| macos-arm64-powershell | 1 | observed + CI | capture-macos-arm64 / macos-latest | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| macos-arm64-tier3-fallback | 3 | observed + CI | capture-macos-arm64 / macos-latest | `zsh -l`, hook disabled | missing/missing/observed |
| macos-amd64-zsh | 1 | observed (Rosetta) + CI | capture-macos-amd64 / macos-15-intel | `zsh -l` with `ZDOTDIR=<hook-dir>` | exact/exact/exact |
| macos-amd64-bash | 1 | observed (Rosetta) + CI | capture-macos-amd64 / macos-15-intel | `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| macos-amd64-fish | 1 | observed (Rosetta) + CI | capture-macos-amd64 / macos-15-intel | `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| macos-amd64-powershell | 1 | observed (Rosetta) + CI | capture-macos-amd64 / macos-15-intel | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| macos-amd64-tier3-fallback | 3 | observed (Rosetta) + CI | capture-macos-amd64 / macos-15-intel | `zsh -l`, hook disabled | missing/missing/observed |
| windows-amd64-powershell | 1 | CI | capture-windows-amd64 / windows-latest | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| windows-amd64-tier3-fallback | 3 | CI | capture-windows-amd64 / windows-latest | `pwsh`, no flags, hook disabled | missing/missing/observed |
| windows-arm64-powershell | 1 | CI | capture-windows-arm64 / windows-11-arm | `pwsh -NoProfile -NoLogo -ExecutionPolicy Bypass -Command <bootstrap>` | exact/exact/exact |
| windows-arm64-tier3-fallback | 3 | CI | capture-windows-arm64 / windows-11-arm | `pwsh`, no flags, hook disabled | missing/missing/observed |
| bsd-amd64-zsh | 1 | CI (SSH) | capture-freebsd-amd64 / ubuntu-latest | account login shell `zsh`; wrapper `zsh -l` | exact/exact/exact |
| bsd-amd64-bash | 1 | CI (SSH) | capture-freebsd-amd64 / ubuntu-latest | account login shell `bash`; wrapper `bash --rcfile <hook-dir>/bashrc -i` | exact/exact/exact |
| bsd-amd64-fish | 1 | CI (SSH) | capture-freebsd-amd64 / ubuntu-latest | account login shell `fish`; wrapper `fish -l --init-command 'source "$NETCATTY_HOOK_FILE"'` | exact/exact/exact |
| bsd-amd64-tier3-fallback | 3 | CI (SSH) | capture-freebsd-amd64 / ubuntu-latest | account login shell `zsh -l`, hook disabled | missing/missing/observed |

`<hook-dir>` is a session-scoped temporary directory. The user's own startup
files are sourced, never edited: zsh preserves the original `ZDOTDIR` in
`NETCATTY_USER_ZDOTDIR`, bash sources the user's profile and rc once, and fish
sources the user config before the hook. nushell uses
`nu -l -e 'source "<hook-dir>/hook.nu"'`; xonsh uses `xonsh -l` with
`XONSHRC_DIR` built as follows (this matches `xonshRCDir` in
`adapters/shellhook/shellhook.go`):

- if the user already set `XONSHRC_DIR`, Netcatty appends only the hook
  directory to that value;
- otherwise the list is
  `<XONSH_SYS_CONFIG_DIR or /etc/xonsh>/rc.d:<XONSH_CONFIG_DIR or
  $XDG_CONFIG_HOME/xonsh or ~/.config/xonsh>/rc.d:<hook-dir>`, for example
  `XONSHRC_DIR=/etc/xonsh/rc.d:${XDG_CONFIG_HOME:-$HOME/.config}/xonsh/rc.d:<hook-dir>`.

Neither nushell nor xonsh is release-blocking on a local Host.

For the FreeBSD SSH cells, the account login shell is started by sshd. For
zsh/bash/fish the planner's remote wrapper is sent and the wrapper invokes the
shell with the flags above. For nushell and xonsh the transport withdraws the
wrapper (`RemoteLoginShellSupported` rejects them) and opens the plain login
shell; their startup is the account shell with no added arguments. nushell is
Tier 2 (cwd exact, no Netcatty exit, native finish observed); xonsh is Tier 2
(cwd exact from OSC 7, no Netcatty exit, no finish).

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

These are the only runs recorded by this environment. The exact command
strings, including the nix-store PATH entries, are in the fixture's
`observedRuns`; the table below names the same runs.

| Run | Environment | Shells and versions | Result |
| --- | --- | --- | --- |
| macos-arm64-local-2026-11-08 | macOS 27.2, arm64 | zsh 5.9.2, bash 5.3.20, fish 4.9.3, nushell 0.116.1, xonsh 0.24.2, pwsh 7.6.9 | PASS 3/3, 53.295s, 51.105s, 52.470s |
| macos-arm64-bash32-local-2026-11-08 | same | `/bin/bash` 3.2.57 | PASS, 0.09s |
| macos-arm64-native-fallback-local-2026-11-08 | same | nushell 0.116.1, xonsh 0.24.2 | PASS, 5.147s |
| macos-amd64-rosetta-local-2026-11-08 | macOS 27.2, x86_64 userland under Rosetta 2 | zsh 5.9, bash 3.2.57, fish 4.7.1, nushell 0.116.1, xonsh 0.23.7, pwsh 7.6.9 | PASS, 57.323s |
| linux-arm64-docker-2026-11-08 | Linux arm64, Docker 29.8.2 (OrbStack), Debian 12.15 | zsh 5.9, bash 5.2.15, fish 3.6.0, nushell 0.116.1, xonsh 0.24.2 | PASS, 28.590s |
| linux-amd64-docker-2026-11-08 | Linux amd64, same runtime and image | zsh 5.9, bash 5.2.15, fish 3.6.0, nushell 0.116.1, xonsh 0.24.2, pwsh 7.6.9 | PASS 3/3, 53.064s, 43.656s, 51.183s |
| linux-amd64-fish-stress-2026-11-08 | Linux amd64, same runtime and image | fish 3.6.0 | PASS 10/10, 7.735s test time |
| linux-arm64-pwsh-docker-2026-11-08 | Linux arm64, same runtime and image | pwsh 7.6.9 | PASS, 1.783s |
| linux-amd64-pwsh-docker-2026-11-08 | Linux amd64, same runtime and image | pwsh 7.6.9 | PASS, 6.325s |
| ssh-linux-arm64-lab-2026-11-08 | Ubuntu 24.04.5 arm64 container `natcatty-ssh-lab`, OpenSSH | zsh 5.9, bash 5.2.21, fish 3.7.0, nushell 0.116.1, xonsh 0.24.2 | PASS, 5.848s |

Both Linux matrix runs used `golang:1.27-bookworm` pinned to
`sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0`.
The arm64 container was `natcatty-debug`; the amd64 container was
`natcatty-debug-amd64`. nushell was installed from the matching
`nu-0.116.1-<arch>-unknown-linux-gnu.tar.gz`; xonsh from `/opt/xonsh`;
PowerShell 7.6.9 was installed at `/usr/local/bin/pwsh` for the Linux
PowerShell runs.

The Linux amd64 and macOS arm64 full runs were executed three times each; the
three durations are recorded above and in the fixture result. The linux/amd64
fish subtest was additionally executed ten times in one `-count=10` invocation:
10 runs, 10 passes, 0 skips (the run is recorded as
`linux-amd64-fish-stress-2026-11-08` and cited by `linux-amd64-fish`). This is
the regression run for the teardown race: the local tests put `HOME` in
`t.TempDir()`, and the fish case pre-creates fish's empty
`generated_completions` cache directory so fish 3.6 does not start its disowned
background man-page generator after the shell exits. `localpty.Close` now hangs
up, waits a bounded time for the shell process to exit, then kills and reaps it
before the Session publishes `Done`; teardown therefore joins every process the
Session owns, and the temporary home is safe to remove.

The macOS x86_64 run built the test binary with
`GOARCH=amd64 CGO_ENABLED=0 go test -c`. The system `/bin/zsh` and `/bin/bash`
(universal binaries) were forced to x86_64 with `arch -x86_64`; fish 4.7.1,
nushell 0.116.1, xonsh 0.23.7, and pwsh 7.6.9 were x86_64 builds. The test
command is recorded verbatim in the fixture. This is Rosetta x86_64 userland
evidence; the release job uses the native Intel runner.

The SSH lab run used the product planner and Session tap against a Linux
container with one account per shell; only the transport was the lab's
x86_64/aarch64 OpenSSH. The fixture records `zshuser@127.0.0.1:2223` as the
target and the result notes the other accounts. The Tier 3 check reused
`zshuser` because that disposable lab did not create a separate `tier3user`.
This run validates the harness; it is **not** a FreeBSD observation and no BSD
cell cites it.

Observed fidelity:

- zsh, bash, and fish Tier 1: exact command, cwd, exit code, and duration.
  bash was checked with 5.3.20, 3.2.57, and 5.2.15/5.2.21.
- nushell and xonsh Tier 1: exact command, cwd, exit code, and duration.
- nushell Tier 2 without the hook: exact cwd, no exit code, observed finish.
- xonsh Tier 2 without the hook: exact cwd, no exit code, no finish; its
  prompt is not prompt-detected, so Session close leaves the finish unknown.
- zsh Tier 3 without the hook: typed command and a measured duration; no cwd
  and no exit code.
- pwsh 7.6.9 Tier 1 on macOS arm64, macOS x86_64 under Rosetta, and Linux
  arm64/amd64: exact command, cwd, exit code, and duration, with exactly one
  profile load. The duration and exit records are asserted by
  `TestRealPowerShellSessionCapturesTierOneEndToEnd`, which drives the real
  Session stack and requires a measured duration above 250 ms; the raw
  `TestRealPowerShellLocalHookCapturesTierOne` alone did not assert them. The
  macOS and Linux runs are not Windows evidence.
- pwsh 7.6.9 Tier 3 on macOS: typed command and duration; no cwd and no exit
  code.

## Manual acceptance procedures

These procedures are for cells that are not observed here. Run them for every
release that changes capture, startup, or the matrix. Record the runner or VM,
exact versions, commands, and result in the release log. Do not mark a cell
`observed` until a run exists in `observedRuns`.

### Windows PowerShell gate reproduction (manual)

The release evidence is `capture-windows-amd64` on `windows-latest` and
`capture-windows-arm64` on `windows-11-arm`, both required by the matrix
checker. This is the same procedure by hand. Use a Windows Server 2025 or
Windows 11 VM, or a GitHub Windows runner, with Go matching `go.mod`.

In PowerShell:

```
$ErrorActionPreference = 'Stop'
$pwsh = (Get-Command pwsh).Source
if (-not (Test-Path -LiteralPath $pwsh)) { throw 'pwsh is missing' }
pwsh --version
$PSVersionTable.PSVersion

go test ./adapters/shellhook -run '^TestRealPowerShellLocalHookCapturesTierOne$' -count=1 -v
go test ./adapters/shellhook -run '^TestRealPowerShellSessionCapturesTierOneEndToEnd$' -count=1 -v
go test ./adapters/shellhook -run '^TestRealPowerShellTierThreeFallback$' -count=1 -v
```

Expected: Tier 1 has the typed command, cwd, exit code, and exactly one
profile load; the Session test also asserts the exit code and a measured
duration above 250 ms; the fallback has no cwd and no exit code, finished by the
profile prompt. Both cells are release-blocking. These cells were **not** run on
this machine; the jobs are the provisioning mechanism and the release gate.

### Windows best-effort shells (manual)

These cells are not release-blocking and were not run here. Use the same
Windows runner or VM and PowerShell. The nushell procedure is explicitly
unverified: it has not been executed on Windows. It preflights the one Windows
behavior the test depends on, `XDG_CONFIG_HOME` support, so a failure is
distinguishable from a passing capture.

nushell (Tier 2):

```
winget install --id Nushell.Nushell --exact --accept-source-agreements --accept-package-agreements --silent
# Open a new PowerShell so the WinGet link directory is on PATH.
$nu = (Get-Command nu.exe).Source
if (-not (Test-Path -LiteralPath $nu)) { throw 'nu.exe is missing' }
& $nu --version

# The test writes its isolated config.nu under XDG_CONFIG_HOME/nushell. Nushell
# honors an absolute XDG_CONFIG_HOME on Windows since nushell/nushell#12118.
# Require the printed directory to end with netcatty-nu-xdg\nushell. If it
# prints %APPDATA%\nushell, this build ignores XDG_CONFIG_HOME, the test cannot
# read its isolated config, and the run is unverified rather than PASS.
$env:XDG_CONFIG_HOME = Join-Path $env:TEMP 'netcatty-nu-xdg'
New-Item -ItemType Directory -Force (Join-Path $env:XDG_CONFIG_HOME 'nushell') | Out-Null
'print "xdg-ok"' | Set-Content (Join-Path $env:XDG_CONFIG_HOME 'nushell\config.nu')
& $nu -c '$nu.default-config-dir'
Remove-Item Env:XDG_CONFIG_HOME -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force (Join-Path $env:TEMP 'netcatty-nu-xdg')

$env:NETCATTY_TEST_NU = $nu
go test ./adapters/shellhook -run '^TestRealNushellWithoutHookKeepsNativeCapture$' -count=1 -v
```

Record the printed `$nu.default-config-dir` and the `nu --version` output with
the result. Do not record a PASS if the preflight prints the `%APPDATA%`
directory or the test fails its user-config assertion.

xonsh (Tier 2):

```
py -3 -m pip install --user xonsh==0.24.2
$userBase = (py -3 -m site --user-base).Trim()
$xonsh = Join-Path $userBase 'Scripts\xonsh.exe'
if (-not (Test-Path -LiteralPath $xonsh)) { throw 'xonsh.exe is missing' }
& $xonsh --version
$env:NETCATTY_TEST_XONSH = $xonsh
go test ./adapters/shellhook -run '^TestRealXonshWithoutHookKeepsNativeFallback$' -count=1 -v
```

For cmd.exe the equivalent environment assignment is (per shell):

```
for /f "delims=" %I in ('where nu.exe') do set "NETCATTY_TEST_NU=%I"
for /f "delims=" %I in ('where xonsh.exe') do set "NETCATTY_TEST_XONSH=%I"
```

zsh, bash, and fish (Tier 3):

1. Install the shell: bash ships with Git for Windows; install zsh and fish
   from MSYS2 (`winget install MSYS2.MSYS2`, then in the MSYS2 shell
   `pacman -S --needed zsh fish`). Record each `--version`.
2. Put the shell, or the MSYS2 `usr/bin`, on PATH. For fish, in PowerShell:
   `$env:NETCATTY_TEST_FISH = (Get-Command fish.exe).Source`.
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

### macOS x86_64 reproduction (manual)

The observed run used Rosetta 2. To repeat that evidence on an arm64 Mac,
install the x86_64 builds of fish, nushell, xonsh, and PowerShell, put
`/bin/zsh` and `/bin/bash` behind wrappers that exec through
`arch -x86_64`, then build and run the amd64 test binary:

```
GOFLAGS=-buildvcs=false GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/shellhook-amd64.test ./adapters/shellhook
cd adapters/shellhook
NETCATTY_TEST_FISH=<x86_64-fish> \
NETCATTY_TEST_NU=<x86_64-nu> \
NETCATTY_TEST_XONSH=<x86_64-xonsh> \
NETCATTY_TEST_PWSH=<x86_64-pwsh> \
/usr/bin/arch -x86_64 /tmp/shellhook-amd64.test \
  -test.run '^(TestRealShellSessionCapturesTierOneEndToEnd|TestRealNushellSessionCapturesTierOneEndToEnd|TestRealXonshSessionCapturesTierOneEndToEnd|TestRealPowerShellLocalHookCapturesTierOne|TestRealPowerShellSessionCapturesTierOneEndToEnd|TestRealTierThreeFallbackCapturesTypedInput|TestRealPowerShellTierThreeFallback)$' \
  -test.count=1 -test.v
```

Expected: zsh, bash, fish, and PowerShell at Tier 1 with exact cwd, exit code,
and duration; the PowerShell Session test asserts the exit and duration;
nushell and xonsh at Tier 1; the Tier 3 fallback with no cwd and no exit. All
four zsh/bash/fish/PowerShell cells are release-blocking; nushell and xonsh are
best-effort and run in the workflow's continue-on-error step. The release
workflow runs the same matrix natively on `macos-15-intel`; that job is the
release-time x86_64 hardware evidence.

### FreeBSD amd64 over SSH (manual)

There is no FreeBSD local acceptance command. `platform/pty` returns
`errors.ErrUnsupported` there, so `adapters/localpty` and the local
`TestReal...` tests cannot start a FreeBSD shell. The product path for a BSD
Host is SSH: the client runs the planner and the Session tap, sends the remote
wrapper, and the FreeBSD account runs the shell. The release evidence is
`capture-freebsd-amd64`, which boots a FreeBSD 14.5 VM under QEMU and runs the
SSH tests. The following is the same procedure by hand on a Linux client; the
release job uses Ubuntu. `cloud-localds` is not a Homebrew formula, so on macOS
run the procedure in a Linux container or VM. Go runs on the client, not in the
VM.

Client prerequisites: `qemu-system-x86_64`, `qemu-img`, `cloud-localds`
(Ubuntu/Debian package `cloud-image-utils`), `ssh`, `ssh-keyscan`, `xz`,
`curl`, and Go matching `go.mod`.

1. Download and verify the image:

```
export LAB="$PWD/bsd-lab"
mkdir -p "$LAB"
curl -fL -o "$LAB/freebsd.qcow2.xz" \
  https://download.freebsd.org/releases/VM-IMAGES/14.5-RELEASE/amd64/Latest/FreeBSD-14.5-RELEASE-amd64-BASIC-CLOUDINIT-ufs.qcow2.xz
echo "a2a8cd84cd59077225a0e75ead852e8a08fe26f9ab251a96d5a4fd4fe1db4454  $LAB/freebsd.qcow2.xz" | sha256sum -c -
unxz -k "$LAB/freebsd.qcow2.xz"
```

2. Create the lab key and the nuageinit seed. FreeBSD's
   `BASIC-CLOUDINIT` images use nuageinit, which reads the same NoCloud
   `user-data`/`meta-data` files from a `cidata` CD. It accepts
   `#cloud-config`, `users[].ssh_authorized_keys`, `write_files`, and
   `runcmd` where each command is a **string**:

```
ssh-keygen -q -t ed25519 -N '' -f "$LAB/freebsd-key"
key="$(cat "$LAB/freebsd-key.pub")"
cat > "$LAB/user-data" <<EOF
#cloud-config
users:
  - name: freebsd
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
  - name: zshuser
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
  - name: bashuser
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
  - name: fishuser
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
  - name: tier3user
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
  - name: nuuser
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
  - name: xonshuser
    shell: /bin/sh
    ssh_authorized_keys: ["$key"]
write_files:
  - path: /root/netcatty-provision.sh
    permissions: '0755'
    content: |
      #!/bin/sh
      set -eux
      env ASSUME_ALWAYS_YES=yes pkg bootstrap -f
      env ASSUME_ALWAYS_YES=yes pkg install -y zsh bash fish nushell python3 py3-pip ca_root_nss curl
      mkdir -p /opt/xonsh/bin /opt/xonsh/lib
      /usr/local/bin/python3 -m pip install --quiet --break-system-packages --target /opt/xonsh/lib "xonsh==0.24.2"
      cat > /opt/xonsh/bin/xonsh <<'EOS'
      #!/bin/sh
      PYTHONPATH=/opt/xonsh/lib exec /usr/local/bin/python3 -m xonsh "\$@"
      EOS
      chmod 0755 /opt/xonsh/bin/xonsh
      for shell in /usr/local/bin/zsh /usr/local/bin/bash /usr/local/bin/fish /usr/local/bin/nu /opt/xonsh/bin/xonsh; do
        grep -qx "\$shell" /etc/shells || echo "\$shell" >> /etc/shells
      done
      pw usermod zshuser -s /usr/local/bin/zsh
      pw usermod bashuser -s /usr/local/bin/bash
      pw usermod fishuser -s /usr/local/bin/fish
      pw usermod tier3user -s /usr/local/bin/zsh
      pw usermod nuuser -s /usr/local/bin/nu
      pw usermod xonshuser -s /opt/xonsh/bin/xonsh
      touch /home/tier3user/.zshrc
      chown tier3user:tier3user /home/tier3user/.zshrc
      sysrc sshd_enable=YES
      service sshd restart
      touch /var/run/netcatty-provisioned
runcmd:
  - /bin/sh /root/netcatty-provision.sh
EOF
cat > "$LAB/meta-data" <<EOF
instance-id: netcatty-freebsd
local-hostname: netcatty-freebsd
EOF
cloud-localds "$LAB/seed.img" "$LAB/user-data" "$LAB/meta-data"
```

3. Boot the VM with SSH forwarded to local port 2222. KVM is used when
   available; otherwise QEMU falls back to multi-threaded TCG, which is
   slower but works:

```
accel="tcg,thread=multi"
if [ -e /dev/kvm ]; then accel="kvm"; fi
qemu-system-x86_64 -machine "accel=$accel" -m 2048 -smp 2 -cpu max \
  -drive "file=$LAB/freebsd.qcow2,if=virtio,format=qcow2" \
  -drive "file=$LAB/seed.img,media=cdrom,readonly=on,format=raw" \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:2222-:22 \
  -device virtio-net-pci,netdev=net0 \
  -display none -monitor none -serial "file:$LAB/console.log" \
  -daemonize -pidfile "$LAB/qemu.pid"
```

4. Wait until nuageinit has installed the shells and changed the account
   shells, then record the host key:

```
ssh_options=(-i "$LAB/freebsd-key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -p 2222)
for attempt in $(seq 1 180); do
  if ssh "${ssh_options[@]}" freebsd@127.0.0.1 'test -f /var/run/netcatty-provisioned' >/dev/null 2>&1; then
    break
  fi
  sleep 5
done
ssh "${ssh_options[@]}" freebsd@127.0.0.1 'test -f /var/run/netcatty-provisioned'
ssh-keyscan -p 2222 127.0.0.1 > "$LAB/known_hosts"
ssh "${ssh_options[@]}" freebsd@127.0.0.1 'uname -a; freebsd-version -ku; zsh --version; bash --version | head -1; fish --version; nu --version; /opt/xonsh/bin/xonsh --version'
```

If provisioning failed, the console log is at `$LAB/console.log`. Nushell
comes from the FreeBSD package and may be older than 0.116.1; record the
reported version. xonsh is pinned to 0.24.2 because older xonsh releases do
not emit OSC 7 at the first prompt.

5. Run the acceptance tests from the repository root with the SSH labs:

```
NETCATTY_TEST_SSH_REQUIRED=1 \
NETCATTY_TEST_SSH_KEY="$LAB/freebsd-key" \
NETCATTY_TEST_SSH_KNOWN_HOSTS="$LAB/known_hosts" \
NETCATTY_TEST_SSH_ZSH=zshuser@127.0.0.1:2222 \
NETCATTY_TEST_SSH_ZSH_LOGIN=/usr/local/bin/zsh \
NETCATTY_TEST_SSH_BASH=bashuser@127.0.0.1:2222 \
NETCATTY_TEST_SSH_BASH_LOGIN=/usr/local/bin/bash \
NETCATTY_TEST_SSH_FISH=fishuser@127.0.0.1:2222 \
NETCATTY_TEST_SSH_FISH_LOGIN=/usr/local/bin/fish \
NETCATTY_TEST_SSH_TIER3=tier3user@127.0.0.1:2222 \
NETCATTY_TEST_SSH_TIER3_LOGIN=/usr/local/bin/zsh \
NETCATTY_TEST_SSH_NU=nuuser@127.0.0.1:2222 \
NETCATTY_TEST_SSH_NU_LOGIN=/usr/local/bin/nu \
NETCATTY_TEST_SSH_XONSH=xonshuser@127.0.0.1:2222 \
NETCATTY_TEST_SSH_XONSH_LOGIN=/opt/xonsh/bin/xonsh \
go test ./adapters/shellhook \
  -run '^(TestRealSSHRemoteHookCapturesTierOne|TestRealSSHRemoteTierThreeFallback|TestRealSSHRemoteNativeFallback)$' \
  -count=1 -v
```

`NETCATTY_TEST_SSH_REQUIRED=1` makes a missing lab a failure instead of a
skip, so a green run proves every shell was exercised. The test opens the
account's login shell (or the planner's `RemoteCommand`) over SSH, requests a
PTY, and asserts the Session tap's Command History row.

Expected fidelity: zsh, bash, fish exact cwd/exit/duration (Tier 1); nushell
exact cwd, no Netcatty exit, observed native finish (Tier 2); xonsh exact cwd,
no Netcatty exit, no finish (Tier 2); Tier 3 typed command and measured
duration with no cwd and no exit. The zsh/bash/fish and fallback cells are
release-blocking; nushell and xonsh are best-effort. In `release.yml` the
zsh/bash/fish and fallback SSH tests run in the required step, while
`TestRealSSHRemoteNativeFallback` runs in a separate continue-on-error step, so
a nushell or xonsh problem cannot make `capture-freebsd-amd64` fail.

6. Clean up:

```
kill "$(cat "$LAB/qemu.pid")"
rm -rf "$LAB"
```

This procedure was **not** run on this machine. The FreeBSD cells are
`ci`-provisioned by `capture-freebsd-amd64`; the job is the required release
gate and the matrix checker fails if it is deleted, conditional, or unreachable
from `publish`.

### Promoting a manual or ci cell to observed

1. Run the cell on its real Host and architecture with the exact startup flags
   recorded in the cell. For BSD, use the SSH procedure above; a local FreeBSD
   `go test` is not an acceptance path.
2. Add an `observedRuns` entry with `harness` (`local`, `docker`,
   `github-actions`, `vm`, or `ssh`), `hostOS`, `arch`, `platform`, `runtime`,
   `runner` or `image` + `imageDigest`, `target` for SSH, `shellVersions`
   keyed by the shell, the exact `command`, the `result` beginning with
   `PASS`, and `recordedAt`.
3. Change the cell's `verification` to `observed`, fill `observed.runs` with
   the run ID, `observed.test` with a test that exists in this package and
   covers that shell, and `observed.fidelity` with what the test asserted.
   Keep or add the release `ci` object if a release job also exercises the
   cell; remove `manual` from an observed cell.
4. Keep `startup` exact. The checker rejects a mismatched Host/architecture,
   a platform that disagrees with the recorded host/arch, a result that is not
   `PASS`, a missing shell version, a Docker run without a sha256 digest, an
   invented test name, a test or run that selects another shell, a manual cell
   that carries observation or CI evidence, and a machine without a required
   release job.

The run command must contain the cited test name. A macOS, Linux, or Rosetta
pwsh run can never support a Windows cell.

## CI

`ci.yml` runs the pure Go/JSON checker in the `checks` job on every pull
request and push to main. `release.yml` runs it in `validate` before building:

```
go test ./adapters/shellhook -run '^(TestCaptureMatrix|TestHostShellMatrix|TestHostShellReleaseGates|TestHostShellMatrixRejectsBadFixtures|TestValidateCellCIRejectsOptionalEvidenceThatCouldGate)$' -count=1 -v
```

The same workflow contains the capture jobs:

| Job | Runner | Required step | Best-effort step |
| --- | --- | --- | --- |
| capture-linux-amd64 | ubuntu-latest | Linux amd64 zsh/bash/fish/PowerShell/fallback | nushell/xonsh |
| capture-linux-arm64 | ubuntu-24.04-arm | Linux arm64 zsh/bash/fish/PowerShell/fallback | nushell/xonsh |
| capture-macos-arm64 | macos-latest | macOS arm64 zsh/bash/fish/PowerShell/fallback | nushell/xonsh |
| capture-macos-amd64 | macos-15-intel | macOS x86_64 zsh/bash/fish/PowerShell/fallback | nushell/xonsh |
| capture-windows-amd64 | windows-latest | Windows x86_64 PowerShell Tier 1 (hook and Session) and fallback | none |
| capture-windows-arm64 | windows-11-arm | Windows arm64 PowerShell Tier 1 (hook and Session) and fallback | none |
| capture-freebsd-amd64 | ubuntu-latest (QEMU FreeBSD 14.5 VM) | BSD zsh/bash/fish/fallback over SSH | nushell/xonsh SSH native fallback |

`checksums` needs every capture job, and `publish` needs `checksums`, so a
failed or skipped required step prevents publishing. A failure in a
continue-on-error best-effort step does not fail its job and therefore does not
block `checksums` or `publish`; the step's own outcome still records the
failure. The checker parses `release.yml` itself, including step-level
`continue-on-error`: deleting a job, adding a job-level `if`, changing
`runs-on`, moving a required test into an optional step, removing a cited test
or `NETCATTY_TEST_SSH_*` binding, or moving a job out of the publish dependency
chain fails `TestHostShellMatrix`. The Windows and FreeBSD jobs are `ci`
evidence in the cells and were not run on this machine; the Linux, macOS, and
SSH-lab observations are attached as `observedRuns`. The Linux and macOS
`observed` cells also carry the release job that re-runs them, so observed
history and release provisioning are both recorded.

`TestCaptureMatrix` still replans every path on each recorded platform, so a
startup-file or argument change cannot drift from the fixture.
`TestHostShellMatrix` validates the cells; `TestHostShellMatrixRejectsBadFixtures`
proves the checker rejects a missing gate, a missing best-effort cell, a wrong
tier, a false observed claim, a FAIL result, a wrong shell binding, a platform
mismatch, a Tier 2 exit code, a BSD cell that points at a local PTY path, and a
CI job that does not exist or does not run the cell.
`TestValidateCellCIRejectsOptionalEvidenceThatCouldGate` separately proves the
optional path cannot smuggle a failure into a release: optional evidence on a
gate, a gate test that only runs in a continue-on-error step, optional evidence
citing a required-step test, and a whole optional job in the publish chain are
all rejected.
