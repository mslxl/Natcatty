# mosh parity gate matrix

This directory provisions the stock mosh servers and stock mosh client the
parity gate (`internal/tools/moshparity`) runs against. The gate proves the
in-process client in `adapters/mosh` behaves like stock mosh against real
stock binaries: real SSH bootstrap, real `MOSH CONNECT` announcement, real UDP,
real terminal bytes. Nothing here is a mock, and a cell is never accepted on
trust.

This is the stock-server compatibility matrix required by ADR-0015. It is
separate from the rewrite feature-parity matrix in `docs/parity/matrix.yaml`
(ADR-0008): that one tracks reimplemented host-app features, while this one
pins stock `mosh-server`/`mosh-client` versions and platforms.

Stock mosh source is **not** stored in this repository. Docker images and VM
wrappers download a pinned upstream release tarball at provisioning time,
verify its SHA-256, and build it. The repository stores only provisioning
scripts and hashes. Nothing here links against or ships stock mosh code; see
`docs/adr/0015-pure-go-mosh-client.md` and
`docs/audit/mosh-provenance.md`.

## Required matrix

`go run ./internal/tools/moshparity list` prints the live matrix. Every
non-stock platform must cover both pinned server versions.

| Platform | Cell IDs | Provisioning | Stock source |
| --- | --- | --- | --- |
| Debian 12 | `debian-12/1.3.2`, `debian-12/1.4.0` | `Dockerfile.debian`, `BASE=debian:12` | pinned release tarball |
| Ubuntu 22.04 LTS | `ubuntu-22.04/1.3.2`, `ubuntu-22.04/1.4.0` | `Dockerfile.debian`, `BASE=ubuntu:22.04` | pinned release tarball |
| Fedora 41 | `fedora-41/1.3.2`, `fedora-41/1.4.0` | `Dockerfile.fedora`, `BASE=fedora:41` | pinned release tarball |
| macOS | `macos/1.3.2`, `macos/1.4.0` | `macos-setup.sh` on the CI runner | Homebrew for 1.4.0, pinned release tarball for 1.3.2 |
| FreeBSD 14 | `freebsd-14/1.3.2`, `freebsd-14/1.4.0` | `freebsd-setup.sh` in the VM runner | ports for 1.4.0, pinned release tarball for 1.3.2 |
| stock client | `stock-client/1.4.0` | `internal/mosh/terminal/testdata/parity/harness` | pinned release tarball |

Pinned release tarballs:

| Version | URL | SHA-256 |
| --- | --- | --- |
| 1.3.2 | `https://github.com/mobile-shell/mosh/releases/download/mosh-1.3.2/mosh-1.3.2.tar.gz` | `da600573dfa827d88ce114e0fed30210689381bbdcff543c931e4d6a2e851216` |
| 1.4.0 | `https://github.com/mobile-shell/mosh/releases/download/mosh-1.4.0/mosh-1.4.0.tar.gz` | `872e4b134e5df29c8933dff12350785054d2fd2839b5ae6b5587b14db1465ddd` |

The hashes are duplicated in `internal/tools/moshparity/matrix.go` so a report
records which source was used; `validateMatrix` and the Dockerfiles fail if a
cell is missing its pin. Container base images and distribution packages are
not digest-pinned, so two builds can differ; the upstream mosh source and
behavioral assertions are the gate, and every cell proves the exact server
version before its behaviors count.

## Running

```sh
# One Docker cell (builds the image from the pinned tarball first).
go run ./internal/tools/moshparity run -cell debian-12/1.4.0 -build

# Several cells, writing one JSON report per cell.
go run ./internal/tools/moshparity run \
  -cell debian-12/1.4.0 -cell fedora-41/1.3.2 \
  -out build/mosh-parity -fuzztime 10s

# A provisioned native cell. macOS sshd listens on 2457; the FreeBSD runner
# uses the base-system sshd on 22, so the address is overridden.
go run ./internal/tools/moshparity run -cell macos/1.4.0 -out build/mosh-parity
go run ./internal/tools/moshparity run -cell freebsd-14/1.4.0 \
  -lab-addr 127.0.0.1:22 -out build/mosh-parity

# Fail closed over every required report. The expected commit comes from
# -commit, NATCATTY_MOSH_PARITY_COMMIT, or git HEAD; it must resolve to a
# non-empty commit.
go run ./internal/tools/moshparity verify -dir build/mosh-parity

# Bounded fuzz run without a server cell.
go run ./internal/tools/moshparity fuzz -fuzztime 30s
```

Runners without git (the FreeBSD VM workspace sync may omit `.git`) pass the
commit explicitly:

```sh
NATCATTY_MOSH_PARITY_COMMIT="$GITHUB_SHA" \
  go run ./internal/tools/moshparity run -cell freebsd-14/1.4.0 \
  -lab-addr 127.0.0.1:22 -out build/mosh-parity
```

Host prerequisites: Go (from `go.mod`), Docker for Docker and stock-client
cells, network access to the pinned GitHub release URLs and the container
registries, and `sshd` plus password authentication for native cells (the
scripts require root; CI runners provide passwordless sudo).

Each `run -cell` starts a fresh container (or connects to the native sshd),
waits for the SSH endpoint, runs the `unit`, `lab`, and `fuzz` suites, writes
`<cell>.json` and, on failure, container logs, and stops the container. The
`stock-client/1.4.0` cell runs only the stock differential suite. A cell
command exits non-zero if any suite or required behavior fails; it still
writes the report so every failure is visible.

The fuzz suite runs five native `go test -fuzz` targets for `-fuzztime` each:
`FuzzDatagramReceive`, `FuzzFragmentReassembly`, `FuzzTransportInstruction`,
`FuzzTerminalParser`, and `FuzzAnnouncementParser`. `-fuzztime` must be at
least 5s per target (CI uses 10s); there is no fuzz waiver, and `verify`
rejects a report that records a shorter run. `verify` also requires the fuzz
suite's recorded elapsed time to cover one bounded run per target, so a
self-declared duration cannot stand in for a run that did not happen. A target
that was renamed or removed is a failure even though `go test -fuzz` exits
zero with "no targets to fuzz": the gate requires a real fuzz run in the
output.

## Report provenance and commit binding

Every report is schema 2 and must record where it was produced:

- `commit`: the gate commit. `run` and `verify` resolve it from `-commit`,
  then `NATCATTY_MOSH_PARITY_COMMIT`, then `git rev-parse HEAD`. There is no
  empty-commit path: if none of those yields a value the command fails, and
  `verify` also hard-fails when it cannot determine the expected commit.
  Reports must all carry the expected commit, so a stale green report cannot
  authorize a later tree. `verify` still writes a failed `summary.json` when
  the commit cannot be resolved or the reports cannot be read.
- `hostOS`, `hostArch`, `hostUname`: the harness runtime
  (`runtime.GOOS`/`GOARCH`) and `uname -s` (FreeBSD falls back to
  `sysctl -n kern.ostype`).
- `labOS`, `labArch`: the exercised endpoint platform. Docker and stock cells
  record the container image's OS/architecture from `docker image inspect`;
  native cells probe the SSH endpoint (`uname -s; uname -m`) before the
  suites and record that result.
- `labAddress`: the SSH endpoint the suites dialled.

`verify` requires `labOS` to be `linux` for Docker and stock cells, `darwin`
for macOS cells, and `freebsd` for FreeBSD cells. Native reports must also
agree on `hostOS` and `hostUname`. The harness enforces this at production
time as well: it refuses to run a native cell unless `runtime.GOOS` (and
`uname -s`, when available) matches the cell, then probes the actual SSH
endpoint it is about to use (`uname -s; uname -m`) and records and checks
that platform before any suite executes. `-lab-addr` is accepted only for
native cells, so a native cell cannot be pointed at a host of another OS and
Docker/stock cells always use the endpoint the harness published for the
image. Docker/stock `labOS` comes from `docker image inspect`, not from the
harness OS, so a developer's macOS Docker run is still valid evidence for a
Linux container cell.

The FreeBSD VM runner syncs the workspace into the VM and may not include
`.git`; its workflow job passes `${{ github.sha }}` through
`NATCATTY_MOSH_PARITY_COMMIT` and `-commit`. `freebsd-setup.sh` also installs
git, but the explicit override is authoritative and does not depend on the
sync carrying a checkout.

Reports are a CI artifact contract, not signed provenance: `verify` recomputes
every behavior from the recorded raw suite results and checks the commit and
platform fields, but an actor who can rewrite artifacts can also rewrite those
fields. The gate's trust boundary is the CI run that produced the artifacts.

## Required behaviors

`internal/tools/moshparity/behaviors.go` is the contract. A behavior passes
only when every referenced test actually ran and passed in the named suite;
`skip`, a renamed test, a missing suite, or a version-mismatched server fails
the cell. `behaviors` in a report is a derived summary for readers: `verify`
recomputes every behavior from `suites` (the raw `go test -json` results) and
never treats the stored list as authoritative, so zeroing `behaviors` alone
cannot make a failed cell green and editing it cannot add coverage that did
not run. The live-server reference set covers server version, bootstrap and
announcement, OCB3, SSP diff/ack/throwaway, resize, shutdown, locale
propagation, and (for 1.4.0) roaming. Client-model behavior (prediction,
EchoAck, terminal goldens) is proven against the stock oracle and committed
goldens, which is the only deterministic way to assert those bytes.

| Behavior | Real coverage |
| --- | --- |
| `server-version` | live `mosh-server --version` through the cell's SSH bootstrap |
| `bootstrap-announcement` | announcement parser, sniffer bounds, adapter errors, live handshake |
| `ocb3` | RFC 7253 vectors, tamper/replay rejection, close zeroization, live encryption |
| `fragmentation` | fragment round-trip and bounds, reordering, large host output, fuzz |
| `loss-reorder-duplication` | sustained loss, reordering, duplicate idempotence |
| `ssp-diff-ack-throwaway` | diff/ack/throwaway, stale-ack resend, shutdown retransmit, live session |
| `prediction-confirmation-correction` | prediction modes, confirmation/correction, EchoAck gating, resize reset |
| `echo-ack` | EchoAck recording and gating |
| `resize` | server-authoritative resize to remote PTY and renderer, live cell resize |
| `terminal-goldens` | committed stock `Display` golden screens and width table |
| `roaming` | address-change continuation, new server address replies, live 1.4.0 roaming |
| `timeout` | RTO retransmission, probe timeout classification, connecting notification |
| `shutdown` | sentinel send/receive/retransmit, zeroization, live server exit |
| `locale-environment` | locale env merge, remote command quoting, live remote `$LANG` |
| `route-errors` | typed route/bootstrap errors and renderer notification |
| `fuzz-targets` | all five bounded fuzz targets passed |

The stock-client cell covers byte-for-byte display parity for scripted
Sessions (`TestSessionMatchesStockMoshClient`, width-edge corpus) and the stock
terminal model (`TestStockParityTerminalModel`, `TestStockParityRandom`).

## Cell details

Docker cells copy `lab-entrypoint.sh`, which creates
`/usr/local/bin/mosh-lab-server` around the pinned binary and starts a
dedicated `sshd` using `sshd_config.lab`. The wrapper forces the cell's UDP
port with `-p` and binds `0.0.0.0` so the published port reaches it. The gate
publishes the SSH and UDP ports listed in `matrix.go`; the ports are fixed per
cell so tests and wrappers cannot drift.

The wrapper pins the server's native locale to `C`. Stock `mosh-server`
applies the client's `-l` locale variables only when the host's own locale is
not UTF-8; Fedora's default `C.UTF-8` would otherwise short-circuit the
adoption path and the locale test would not exercise the client handoff. The
same wrapper pattern is used by `macos-setup.sh` and `freebsd-setup.sh`.

`macos-setup.sh` installs Homebrew mosh for 1.4.0 and builds 1.3.2 from the
pinned tarball on a case-sensitive APFS image (macOS's default
case-insensitive filesystem would resolve the generated `VERSION` file inside
the C++ headers). It creates the `tester` account and starts sshd on
`127.0.0.1:2457`. The script must be run with `sudo`, but it invokes Homebrew
as the invoking runner user (`SUDO_USER`), because Homebrew refuses to run as
root; it uses the Homebrew prefix, not the `brew` executable path, for the
1.3.2 build's `PATH`.

`freebsd-setup.sh` installs git and Go, installs the ports mosh for 1.4.0,
builds 1.3.2 from the pinned tarball, creates `tester`, enables the base sshd,
and writes the two per-version wrappers. Inside the VM the matrix SSH address
is `127.0.0.1:22`, hence `-lab-addr`; the CI job also exports
`NATCATTY_MOSH_PARITY_COMMIT="${{ github.sha }}"` because the VM workspace
sync may not include `.git`.

## CI and the default-switch contract

`.github/workflows/mosh-parity.yml` runs one job per Docker cell, one stock
job, one macOS job, one FreeBSD job, and a final `verify` job. Every job
uploads its per-cell JSON report even when the cell fails. The `verify` job
downloads all reports, recomputes the required behavior matrix from the raw
suite results, and fails if any required cell, suite, test, version, platform,
commit, fuzz time, or behavior is missing or failed. It also writes
`summary.json`:

```json
{
  "schema": 2,
  "status": "pass",
  "generatedAt": "...",
  "commit": "...",
  "cells": [
    {
      "id": "debian-12/1.4.0",
      "platform": "debian-12",
      "version": "1.4.0",
      "status": "pass",
      "behaviorsPassed": 16,
      "behaviorsTotal": 16
    }
  ]
}
```

Ticket 34's default switch reads the gate only through
`go run ./internal/tools/moshparity verify -dir <reports> -commit <sha>`
(exit status 0) or the `status` field of `summary.json`; both are fail-closed.
A missing report, unknown report, schema mismatch, identity mismatch (cell,
image, endpoint), server-version mismatch, missing suite, skipped test, failed
behavior, platform-provenance mismatch, or a fuzz time below 5s makes `verify`
fail. All reports must carry the same commit, and that commit must be the one
being verified; `verify` also refuses to run when the expected commit cannot
be determined. A stale green report therefore cannot authorize a later tree.
There is no waiver, partial-green path, or per-platform opt-out. The workflow
runs as ordinary CI on pull requests and `main` pushes that touch the mosh
client, the session layer, or the parity tool, and on manual dispatch; its
`verify` job passes `${{ github.sha }}` explicitly.

## Supplementary local evidence

`go test ./adapters/mosh -run TestLocalMoshServer` drives the host's own stock
`mosh-server` directly (real UDP and terminal model, local exec replaces SSH):

```sh
NATCATTY_MOSH_LOCAL_SERVER=/opt/homebrew/bin/mosh-server \
NATCATTY_MOSH_LOCAL_VERSION=1.4.0 \
  go test ./adapters/mosh -run TestLocalMoshServer -v
```

This is developer evidence for the platform's stock binary, not a matrix cell;
it cannot substitute for a provisioned cell in `verify`.

## Known limits

- Native cells require the provisioned runner topology above. A developer
  without root/sshd cannot run them locally; CI is the evidence.
- Native provisioning can drift with the platform package sources. Both
  native paths fail closed instead of silently approving a new binary:
  `macos-setup.sh` asserts Homebrew provides `mosh 1.4.0` (there is no
  practical way to pin a Homebrew version without vendoring a bottle), and
  `freebsd-setup.sh` asserts the ports package `mosh-server --version` is
  `1.4.0`. If either assertion fails, setup exits before a report exists; if a
  drifted binary somehow reached a cell, the `server-version` lab behavior
  proves the exact version over SSH before any behavior counts. Repinning
  either path is a deliberate matrix and setup change, never a waiver.
- The FreeBSD 1.4.0 binary comes from the ports package tree at runner time;
  pinning a package revision across the quarterly branch is not practical, so
  the exact-version assertion above is the pin.
- Docker Hub anonymous pull limits can fail image builds; that is a CI
  infrastructure failure, not a green cell.
