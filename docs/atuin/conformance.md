# Atuin v18.24.5 conformance suite

The conformance suite is release-acceptance evidence for Atuin-compatible
history sync. It runs the official pinned Atuin CLI and a local, self-hosted,
pinned Atuin server against Netcatty's real HTTP adapter, sync engine, history
service, and SQLite store. It does not use a fake `Remote` and it does not test
only wire codecs in isolation.

This is not a per-pull-request gate. The suite lives behind the `conformance`
build tag, so a normal `go test ./...` neither compiles nor runs it and needs no
external binaries.

## What the suite proves

| Acceptance criterion | Test |
| --- | --- |
| Official CLI upload, then Netcatty download decrypts and projects it | `TestCLIUploadThenNetcattyDownload` |
| Netcatty upload, then the official CLI lists it | `TestNetcattyUploadThenCLIList` |
| CLI delete uploads a tombstone that Netcatty applies | `TestCLIDeleteThenNetcattyApply` |
| Netcatty delete uploads a tombstone that the CLI applies | `TestNetcattyDeleteThenCLIApply` |
| Two Netcatty writers with independent series converge without duplicates | `TestTwoNetcattyWritersConverge` |
| A wrong master key fails closed and does not corrupt or upload local data | `TestWrongMasterKeyFailsClosed` |
| Unknown major version, missing capability, and unknown enforced capability fail closed before any upload | `TestVersionAndCapabilityMismatchFailsClosed` |
| An upload whose response is lost does not falsely advance a checkpoint, and retry converges with one copy of every record | `TestInterruptedUploadRetryCompletesWithoutDuplicates` |
| A non-`history` tag is never downloaded, uploaded, applied, or checkpointed | `TestUnknownTagDoesNotAffectHistorySync` |

The suite deliberately does not cover hosted Atuin, packfile, `kv`, `scripts`,
`dotfiles`, AI sessions, Postgres or MySQL server backends, App Lock, account
login/refresh flows, or platform UI. Those are outside this ticket.

## Pinned release

| Item | Value |
| --- | --- |
| Atuin server | `atuin-server 18.24.5` |
| Official client | `atuin 18.24.5 (ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e)` |
| Reference tag | `v18.24.5` |
| Reference commit | `ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e` |
| Artifacts used for the evidence run | `atuin-aarch64-apple-darwin.tar.gz`, `atuin-server-aarch64-apple-darwin.tar.gz`, `source.tar.gz` |

Published SHA-256 checksums of the downloaded artifacts, matched against the
release `.sha256` sidecars:

```text
644e92d5667e2ebbc00758c71d5974bc907d238eaa6cdc32b24eb73dce7f9a5a  atuin-aarch64-apple-darwin.tar.gz
1a7eff2cd667b14632754217602a6418d67cca984e866bdbda95a02f02bc2344  atuin-server-aarch64-apple-darwin.tar.gz
83f32a21916fb8d10d903a26fc0f3f5462367f81c18377b30efcbce7d8649c09  source.tar.gz
```

Extracted binaries actually executed for the evidence run:

```text
2d65dc20e268e435404f6763ba0c5a89b18dd12d59e9b881cb58fcea9b399ece  atuin
569865a9719c3f2621a9d181712eefefbeaa97a3aa8bbc57d189596353f87aee  atuin-server
```

The harness refuses to run if `ATUIN_CLI_BIN --version` is not exactly
`atuin 18.24.5` followed by the pinned commit string, or if
`ATUIN_SERVER_BIN --version` is not exactly `atuin-server 18.24.5`. It does not
silently substitute a locally installed 18.24.2 or any other release. The
runner checks the same pin before invoking `go test`.

## Running locally

1. Download the v18.24.5 client and server archives for your platform from the
   upstream GitHub release, plus the `source.tar.gz` used to review the
   protocol. Verify each archive against its published `.sha256` sidecar.
2. Extract them into a directory outside this repository.
3. From the repository root:

   ```sh
   ATUIN_CLI_BIN=/absolute/path/to/atuin \
   ATUIN_SERVER_BIN=/absolute/path/to/atuin-server \
   ./scripts/atuin-conformance.sh
   ```

   The script prints both version strings and SHA-256 values, then runs:

   ```sh
   go test -tags conformance ./conformance/atuin -v -count=1
   ```

   If `go` is not on `PATH`, the script falls back to
   `nix develop --command env GOFLAGS=-buildvcs=false go ...`. Extra arguments
   are passed to `go test`, for example
   `./scripts/atuin-conformance.sh -run TestUnknownTagDoesNotAffectHistorySync`.

The suite needs no repository configuration and no secrets. Each test creates
its own temporary server directory, SQLite database, CLI home, and Netcatty
database under the test temporary directory, picks an ephemeral loopback port,
and stops the server in test cleanup. No server, process, or database should
remain after a run.

## Running in CI

The suite is intended for a release-acceptance job, not every pull request.
A CI job must:

1. Download the same pinned archives and verify the checksums above (or the
   sidecars from the release). Cache them by version and checksum.
2. Extract them, mark the binaries executable, and set `ATUIN_CLI_BIN` and
   `ATUIN_SERVER_BIN`.
3. Run `./scripts/atuin-conformance.sh`.
4. Publish the version lines, checksums, and `go test` PASS/FAIL output as
   release evidence.

The job needs no network service other than the loopback connections the
tests make. Do not expose the server port, do not point `sync_address` at a
hosted Atuin service, and do not add credentials. A CI run has not been
observed in this evidence run; the local run below is the observed evidence.

## Evidence observed on 2026-11-08

Environment:

- macOS 27.2.0, Darwin arm64 (`uname -sm` = `Darwin arm64`)
- `go version go1.27.3 darwin/arm64`
- Official `atuin 18.24.5 (ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e)`
- Official `atuin-server 18.24.5`
- Local server: `sqlite://<temporary path>/atuin-server.db`,
  `open_registration = true`, bound to `127.0.0.1` on ephemeral ports
- Netcatty code under test: branch `ticket19-manual`, based on `9a9dff5f7`,
  with the conformance suite and this document committed together

Command (the runner prints versions and checksums, then runs `go test` through
the repository dev shell):

```sh
ATUIN_CLI_BIN=/tmp/natcatty-atuin-conformance/bin/atuin-aarch64-apple-darwin/atuin \
ATUIN_SERVER_BIN=/tmp/natcatty-atuin-conformance/bin/atuin-server-aarch64-apple-darwin/atuin-server \
./scripts/atuin-conformance.sh
```

Observed result: `PASS`, exit status 0, total 2.536s.

```text
PASS: TestCLIUploadThenNetcattyDownload (0.27s)
PASS: TestNetcattyUploadThenCLIList (0.20s)
PASS: TestCLIDeleteThenNetcattyApply (0.30s)
PASS: TestNetcattyDeleteThenCLIApply (0.20s)
PASS: TestTwoNetcattyWritersConverge (0.16s)
PASS: TestWrongMasterKeyFailsClosed (0.21s)
PASS: TestVersionAndCapabilityMismatchFailsClosed (0.24s)
  PASS: unknown_major_version (0.01s)
  PASS: missing_page-size_capability (0.01s)
  PASS: unknown_enforced_capability (0.01s)
PASS: TestInterruptedUploadRetryCompletesWithoutDuplicates (0.15s)
PASS: TestUnknownTagDoesNotAffectHistorySync (0.21s)
ok  	github.com/mslxl/natcatty/conformance/atuin	2.536s
```

An earlier full run of the same scenario set before the final review cleanup
also passed in 6.125s.

Observed per-scenario behavior worth recording:

- CLI upload then Netcatty download: two CLI creates downloaded and applied;
  the second sync downloaded and applied nothing and left two live rows.
- Netcatty upload then CLI list: two uploads acknowledged; the CLI stored both
  with exit code 42 and 40ms duration, and `atuin history list --cmd-only`
  showed both.
- CLI delete then Netcatty apply: the CLI series advanced from index 1 to 2,
  Netcatty downloaded one record, soft-deleted the target, kept the unrelated
  row, and stored exactly one remote tombstone for that history id.
- Netcatty delete then CLI apply: the CLI removed the target row and kept the
  unrelated row after syncing the Netcatty tombstone.
- Two writers: each writer synced twice; both ended with four live rows (the
  union of two two-record series), no duplicate row ids, one server series per
  writer, and the peer checkpoint applied at index 1. A further sync uploaded
  nothing.
- Wrong key: sync returned `ErrKeyMismatch`, status became `key_mismatch`, the
  pending local capture was not sent, no local checkpoint advanced, the parsed
  server record status was unchanged, and the local row survived. Replacing
  the key with the account key recovered, uploaded that same row, and kept the
  writer UUID.
- Mismatch subtests: each returned `ErrIncompatibleServer` with state
  `incompatible_server`, left the pending capture unuploaded, added no
  checkpoint, and did not change the server status. The faults were injected
  by a loopback proxy in front of the pinned server; no non-pinned server was
  contacted.
- Interrupted upload: the proxy forwarded three records, let the server
  commit, then aborted the response. The client reported `ErrOffline`, kept
  three pending uploads, marked none uploaded, and stored no advanced
  checkpoint. The retry's download phase reconciled the server's committed
  copies as duplicates, acknowledged all three without counting them as
  downloaded rows, and finished with `pending=0`, uploaded index 2, three
  server records at indexes 0,1,2, exactly one copy of each command, and no
  duplicate live rows.
- Unknown tag: after seeding one raw `conformance-unknown` record with its own
  host series, the adapter requested only `tag=history`; the payload never
  became a command, no checkpoint was created for the unknown series, and the
  seeded record remained byte-decryptable with its original payload.

## Fault injection

The pinned server cannot be made to advertise Atuin 19 or a broken capability
set, so the version and capability scans put a `net/http/httptest` reverse proxy
on loopback in front of the pinned server. The proxy only rewrites the response
`Atuin-Version` header, removes the page-size capability, or adds an unknown
enforced capability. For the interrupted-upload case it forwards the upload,
reads the server's complete response so the commit is durable, and then aborts
the client connection with `http.ErrAbortHandler`. All backends remain the
pinned local server on `127.0.0.1`.

## Security boundaries

- The suite starts only the local pinned `atuin-server` on `127.0.0.1` with an
  ephemeral port and a temporary SQLite database.
- It never contacts `atuin.sh`, `api.atuin.sh`, or any hosted Atuin service.
- It never calls `DELETE /api/v0/store`.
- It never uploads credentials or history to a remote host. The only account
  is a fresh test account with a fixed test-only username and password on an
  ephemeral local server, and a key generated by the CLI inside the test
  temporary directory.
- The official CLI always runs with a stripped environment (`ATUIN_*`, `HOME`,
  `XDG_*` removed and replaced), so a developer's real Atuin session, key, and
  history cannot reach a test.
- Every child process and database is scoped to a test and removed by cleanup;
  the server is interrupted and, if needed, killed after a bounded wait.

## Residual risks

- The evidence run used the official darwin-arm64 artifacts on macOS. Other
  platforms and architectures were not observed.
- Only the SQLite server backend was exercised. PostgreSQL and MySQL server
  backends were not.
- Version and capability incompatibility was simulated with a loopback proxy,
  not by running a real future Atuin release. This proves the client's
  fail-closed behavior, not any future server's actual behavior.
- The interrupted upload simulates a lost response after a committed POST. It
  does not simulate a partial request body, a mid-connection reset during the
  request, or a server crash between commit and acknowledgement.
- The two Netcatty writers run in one test process with independent stores.
  Cross-machine concurrency and scheduler timing races are not exercised.
- The suite covers the `history` tag only. It proves that one unknown tag is
  isolated, but does not enumerate every tag the upstream ecosystem may add.
