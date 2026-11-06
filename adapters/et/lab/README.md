# ET parity lab and gate

This directory builds stock [Eternal Terminal](https://github.com/MisterTea/EternalTerminal)
servers from pinned source commits and runs the in-process Natcatty ET client
against them. `internal/tools/etparity` turns that lab into the parity gate:
one self-reporting pass/fail report per matrix cell, and a `verify` command
that fails closed.

Stock ET is Apache-2.0, matching the bundled client's upstream provenance. It
is cloned and built inside disposable containers or on a throwaway CI runner.
No stock source is vendored, committed, or shipped; the adapter's own
GPL-3.0-or-later licensing and the vendored et-go core's Apache-2.0 provenance
stay unchanged.

## Matrix

| Cell | Platform | Stock etserver | Kind |
| --- | --- | --- | --- |
| `debian-12/7.1.1` | Debian 12 | 7.1.1 | Docker |
| `debian-12/6.2.10` | Debian 12 | 6.2.10 | Docker |
| `ubuntu-24.04/7.1.1` | Ubuntu 24.04 LTS | 7.1.1 | Docker |
| `ubuntu-24.04/6.2.10` | Ubuntu 24.04 LTS | 6.2.10 | Docker |
| `fedora-41/7.1.1` | Fedora 41 | 7.1.1 | Docker |
| `fedora-41/6.2.10` | Fedora 41 | 6.2.10 | Docker |
| `arch-linux/7.1.1` | Arch Linux | 7.1.1 | Docker |
| `arch-linux/6.2.10` | Arch Linux | 6.2.10 | Docker |
| `macos/7.1.1` | macOS runner | 7.1.1 | Native |
| `macos/6.2.10` | macOS runner | 6.2.10 | Native |

Versions and base image digests are pinned in `compose.yaml` and mirrored by
`internal/tools/etparity/matrix.go`, which pins the full `sha256` digest per
platform and makes the unit test compare the complete digest, not a prefix.
After starting a cell, `run` also inspects the running image's description
label and refuses the cell unless it names the same pinned base digest, so a
`-compose` override or environment interpolation cannot bypass the pin. This
is drift detection, not attestation: the trust boundary is the checked-out
repository and the Docker daemon that builds and runs the cell. The source tag
is cloned shallowly and `git rev-parse HEAD` is compared with the full commit,
so a moved tag cannot change what a cell tests. Each cell
publishes SSH on a fixed loopback port (2322-2329); the runner verifies the
published port rather than assuming it. Each matrix cell also declares the
expected harness platform (linux/amd64 for Docker cells, darwin/arm64 for the
native macOS cells), and every report records the actual `runtime.GOOS` and
`runtime.GOARCH` of the process that ran the suites.

The macOS cells run both stock versions natively. They are provisioned by
`macos-provision.sh` and built from the same pinned sources by
`.github/workflows/et-parity.yml`; there is no Homebrew etserver formula, so a
source build is the only way to pin the exact server version.

## Running the gate

```sh
go run ./internal/tools/etparity list
go run ./internal/tools/etparity run -cell debian-12/7.1.1 -verbose
go run ./internal/tools/etparity run -all -out build/et-parity
go run ./internal/tools/etparity fuzz \
  -target ./adapters/et/internal/etgo/wire:FuzzReadFrame \
  -target ./adapters/et/internal/etgo/seal:FuzzOpen \
  -target ./adapters/et/internal/etgo/wire:FuzzParsePacket \
  -target ./adapters/et/internal/etgo/etcp:FuzzReplayHandling \
  -out build/et-parity -fuzztime 30s
go run ./internal/tools/etparity verify -dir build/et-parity
go run ./internal/tools/etparity report -dir build/et-parity
```

`run` builds and starts the cell (Docker compose, or a native endpoint), waits
for its SSH socket, runs two `go test -json` suites, maps every required
behavior to named tests, and writes `<cell>.json` plus container logs. A test
that is skipped, missing after a rename, or failed is a failure; a missing cell
report or a version mismatch is a failure. There is no waiver flag.

Every report is bound to the commit, the git tree hash, the harness OS/arch,
the generation time, and (in CI) `GITHUB_RUN_ID`. `run` refuses to produce a
report from a dirty worktree; `verify` rejects stale reports, reports from
another tree or run, and reports whose recorded cell metadata or host platform
does not match the matrix. The serialized `status` and behavior list are never
trusted: verify recomputes every behavior from the recorded suites, requires
both required suites to have passed, and requires the reported results to
match the recomputation exactly. The expected behavior-ID set is pinned per
stock version, so deleting a behavior and its tests fails verification.

`fuzz` is the artifact half of the fuzzing gate. For each `-target PKG:NAME`
it requires the target to be in the compiled-in `requiredFuzzTargets` table and
to exist as `func Name(f *testing.F)` in the package's test source, runs
`go test PKG -list '^Name$'` and requires an exact line match, then runs
`go test PKG -run '^$' -fuzz '^Name$' -fuzztime DUR -timeout DUR` and captures
combined output and timing. It atomically writes `fuzz-<Name>.json` under
`-out` even when a step fails. The receipt carries a schema version, target,
package, name, status, command, bounded captured output (256 KiB, like suite
output), start/end timestamps, fuzztime, host OS/arch, git commit/tree, and
`GITHUB_RUN_ID` when present. `-fuzztime` defaults to 30s and must be at least
the documented minimum of 1s. `fuzz` refuses to produce receipts from a dirty
worktree, for the same reason `run` does.

`verify` requires the exact four receipts. Missing, extra, duplicate, renamed,
or wrong-package/name receipts fail; so do failed status, empty or evidence-free
output, a commit/tree/run-ID mismatch, stale or future timestamps, a fuzztime
below 1s, an elapsed time shorter than the fuzztime (recomputed from the
receipt timestamps), and a target that no longer exists in source. Status is
not trusted on its own: verify recomputes success from the captured output,
which must show `fuzz: elapsed:`, `PASS`, and a successful `go test` line, and
must not show a failure or panic.

The lab suite runs `./adapters/et` with `NATCATTY_ET_LAB=1` against the cell's
one server. The unit suite runs `./adapters/et/...` hermetically with
`NATCATTY_ET_LAB` unset. Both suites get an explicit environment allowlist
(PATH, HOME, Go toolchain, ssh-agent, locale); ambient `NATCATTY_*` variables
are never inherited, so a shell that sets `NATCATTY_ET_UPDATE_GOLDEN=1` cannot
rewrite the golden and hide a regression. Version-specific tests are excluded
with an exact `-skip` pattern and the corresponding behavior is required from
that version's own test.

### Requirements and ports

- Docker with the Compose plugin (or a native lab endpoint for a native cell).
- Free loopback TCP ports 2322-2329 for the Docker matrix.
- No SSH daemon or ET installation on the host is used by Docker cells.

Images can be built ahead of time:

```sh
docker compose -f adapters/et/lab/compose.yaml build debian12-et711
```

`run` removes the cell containers after it finishes unless `-keep` is passed.
Use `-lab-addr host:port` to point a single native cell at an already
provisioned endpoint; the override is rejected for Docker cells and must be a
loopback address, so a report can never claim a Docker cell while testing a
different server.

## What the gate covers

`internal/tools/etparity/behaviors.go` is the authoritative list. Every
behavior maps to at least one real-server test, plus hermetic unit/fuzz tests
where useful:

- server identity and the pinned stock version
- SSH bootstrap and INITIAL_PAYLOAD
- password, selected-key, agent, certificate, and Vault-identity auth
- HTTP CONNECT proxy and real SSH jump-host routing
- framing, secretbox streams, large output, scrollback
- keepalive, network-change reconnect, app-restart reattach
- replay window and the 64 MiB boundary
- v7 challenge, reset/attach, exit status, RETRY_LATER, displacement
- resize, golden terminal output, and typed error paths

The golden tests run a scripted byte sequence through the real server. The
in-process client must match `testdata/golden/terminal-output.golden`; on
7.1.1 the stock `et` client is the byte-for-byte oracle, and on 6.2.10 the
stock client is driven interactively under tmux (its `-u` option aborts with
`std::bad_cast`, so the oracle uses the supported `user@host` spelling) and
its rendered screen is compared. Regenerate the golden only for an intentional
change:

```sh
NATCATTY_ET_LAB=1 NATCATTY_ET_LAB_VERSION=7.1.1 \
  NATCATTY_ET_UPDATE_GOLDEN=1 go test ./adapters/et -run TestLabGoldenTerminalOutput -count=1
```

## CI and ticket 34

`.github/workflows/et-parity.yml` runs one Docker job per Linux cell, one
native macOS job per stock version, and a bounded fuzz job for framing,
secretbox, packet decoding, and replay handling. The fuzz job has four steps,
one per required target:

```sh
go run ./internal/tools/etparity fuzz -target 'PKG:Name' -out "${ET_PARITY_OUT}" -fuzztime 30s
```

Each step produces `fuzz-<Name>.json`; the job uploads those receipts as the
`et-parity-fuzz` artifact (if-no-files-found: error, if: always()). The verify
job downloads every `et-parity-*` artifact into the same directory, requires
the fuzz job result to be success, and runs:

```sh
go run ./internal/tools/etparity verify -dir build/et-parity
```

The trust boundary is the artifact, not the workflow text. `verify` requires
the exact four receipts and validates them, so a workflow edit that removes,
comments out, shadows with an anchor or template, adds a step-level `if`, or
otherwise makes a fuzz step not execute produces no receipt and fails the gate
closed. There is deliberately no YAML or shell parser in the gate to keep in
sync with the workflow; the receipt set is the contract. Like cell reports,
receipts are unsigned JSON and a determined attacker with write access to the
workflow could forge them. The real control is the repository/workflow review
boundary plus the GitHub Actions artifact chain: treat workflow changes as
security-relevant changes, and do not treat a receipt as cryptographic
attestation.

`GITHUB_RUN_ID` binds every report and every fuzz receipt to the workflow run
that produced it; an artifact from an earlier run fails verification even if
its commit matches. Outside CI, `verify` enforces the fixed 24-hour freshness
window and rejects artifacts that claim a CI run. There is no flag to widen
either check.

Docker-cell reports carry the platform of the process that ran the suites.
The matrix expects `linux/amd64` (the CI runner) for Docker cells, so a local
`run` on another host (for example Apple silicon macOS) prints a warning and
`verify` rejects those reports by design. Local runs remain useful behavior
smoke checks; a green `verify` requires the real Linux and macOS CI cells.

The `et parity gate` job is the required status check. Ticket 34 must consume
this exact result before changing or including the default transport: green
`verify` for every matrix cell, no waivers, no partial matrices. A skipped
platform or a missing report is a failed gate, not a green one.

## Development notes

The lab containers need a real shell, tmux (for the 6.2.10 stock oracle), and
a writable home. `setup-lab.sh` creates `tester`/`tester2` with the same lab
password the gate exports, passwordless sudo, and a trusted-CA slot the
certificate test fills at runtime. The macOS script creates the same accounts
and enables the system sshd; it must run with sudo.

Behaviors and matrix cells are ordinary Go constants and are unit-tested in
`internal/tools/etparity/main_test.go`. Adding a behavior without a real
lab test, naming a test that does not exist, or dropping a version from a
platform fails those tests and `verify`.
