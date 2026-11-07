# Supported versions and compatibility statement

Published: 2026-11-07
Feature: Atuin-compatible history sync in Netcatty Native
Status: published; subject to human/legal review.

## Statement

**Netcatty is an independent client for the Atuin v18 history sync wire
protocol. It speaks the protocol at the pinned Atuin v18.24.5 reference
(commit `ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e`), supports self-hosted OSS
Atuin v18.x servers, and is intended to support the atuin.sh hosted service
subject to the maintainers' third-party-client policy and rate limits. It fails
closed on an unknown major version or a capability set it cannot satisfy.
Netcatty is an independent client and is not affiliated with Atuin, Inc.**

Netcatty is not a fork of the Atuin application, does not bundle the Atuin
binary, and does not run Atuin on a Host. "Atuin-compatible" describes wire
interoperability only; it does not mean the feature is made, certified, or
endorsed by Atuin, Inc.

Implementation status: this statement is the contract for the Atuin-compatible
sync implementation. It is not a claim that the sync client is merged in this
repository or that the conformance suite has passed; both remain release gates.

The upstream wire protocol has no public stability contract: there is no
OpenAPI description, and capability and version headers are the agreement. The
pinned Atuin source and the conformance evidence listed below are therefore
this client's compatibility contract. This statement is a policy claim about
what the client may connect to, not a claim that every cell in the table below
has already been exercised in a release.

## Pinned reference

| Item | Value |
| --- | --- |
| Client wire version (`Atuin-Version` header) | `18.24.5` |
| Reference tag | v18.24.5 |
| Reference commit | `ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e` |
| Release date | 2026-11-04 |
| Upstream license | MIT, Copyright (c) 2026 Atuin, Inc. |
| Protocol line | Atuin v18 history sync |
| Record encoding | MessagePack history payload, V0/V1/V2 decode, V2 write (13 fields) |
| Encryption | PASETO v4.local with per-record CEK wrapped by PASERK `k4.local-wrap.pie` |

## Supported versions and components

| Component | Support | Policy |
| --- | --- | --- |
| Atuin OSS server v18.24.5, self-hosted | Supported; reference implementation | Must pass the conformance suite against the pinned server and official CLI. |
| Other Atuin OSS server v18.x | Conditionally supported | Accepted only when the server advertises the capabilities and page-size behavior the client requires. Not individually certified unless the conformance suite passes for that version. |
| Atuin hosted service (`https://api.atuin.sh`) | Intended supported endpoint | The hosted service's third-party-client policy and rate limits have **not** been verified; the self-hosted v18.24.5 server is the reference. |
| Atuin 19 or any unknown major version | Unsupported | Fail closed as `incompatible server`; no download and no upload. |
| Official Atuin CLI v18.24.5 | Not required by users | To be used only as a conformance oracle for golden vectors and interoperability tests. Netcatty does not shell out to it. |
| Atuin Desktop, Octavo/Hub, AI, threads, output capture | Out of scope | Never used or claimed. |
| Atuin packfile | Unsupported | The client reads and writes loose records only and never uses packfile. |
| Atuin record tags other than `history` | Not synced | `kv`, `scripts`, `dotfiles`, AI sessions, packfile, and unknown tags are ignored and never uploaded. |

## Protocol surface

Under the specification, the adapter implements the pinned client's login,
account, capabilities, record-status, record-download, and record-upload
endpoints. It uses `Authorization: Token <session>` or
`Authorization: Bearer <token>`, sends the pinned `Atuin-Version`, and
identifies Netcatty in its `User-Agent`.

| Endpoint | Use |
| --- | --- |
| `POST /login` (and the account endpoint) | Authenticate a session token; validate a pasted token |
| `GET /api/v0/me` | Account identity |
| `GET /api/v0/capabilities` | Capability negotiation |
| `GET /api/v0/record` | Record status (`host -> tag -> max(idx)`) |
| `GET /api/v0/record/next` | Paged loose-record download by `(host, tag, start, count)` |
| `POST /api/v0/record` | Ordered batch upload |
| `DELETE /api/v0/store` | **Never called.** The adapter does not expose it to the application. |

Authentication failures, key mismatch, and incompatible servers are terminal
for that sync attempt: the client stops and waits for the user. They are not
retried automatically.

## Fail-closed policy

Unknown or ambiguous inputs never trigger a guess, a downgrade, or a
best-effort write.

1. **Unknown server major version.** The response major version must be 18.
   Anything else is reported as `incompatible server` and stops the sync before
   any download or upload.
2. **Unknown or unsatisfiable capabilities.** If the server enforces a
   capability the client does not implement, or omits a capability or page-size
   behavior the client requires, the sync stops as `incompatible server`.
3. **Unknown record shape.** A record whose format version or field count is
   outside the pinned decoder's range is rejected instead of guessed or
   silently skipped; the sync stops.
4. **Decryption failure.** A failure to decrypt an existing history record is
   reported as a key mismatch. The client never silently skips the record, never
   marks the key valid, and never uploads with a key that failed validation.
5. **Authentication failure or expiry.** The client stops and requires the user
   to sign in again; it does not loop on credentials.
6. **No fallback.** There is no legacy-protocol fallback, no "continue on
   mismatch" mode, and no write when compatibility is uncertain.
7. **New Atuin releases.** A newer Atuin major is not claimed as supported until
   the reference pin moves and the conformance suite passes for the new pin. A
   minor or patch release inside v18 may interoperate, but it is not advertised
   as certified unless it has passed the suite.

## Product constraints

These constraints are part of the compatibility statement. They restate the
product boundary in the project's internal integration specification; the
specification governs implementation details, and this table states the public
commitment. They are not optional settings and do not vary by server.

| Constraint | Requirement |
| --- | --- |
| Local history at rest | Command History is plaintext at rest, matching Atuin's local model. The master key protects only sync payloads; it does not encrypt the local journal. |
| Master key scope | The Atuin master key is used only for sync encryption and decryption. It is held in memory as bytes only for the duration of a sync operation and zeroed afterwards. |
| No Atuin on a Host | Never upload, install, start, or run the Atuin binary on a Host. Never start or stop an Atuin daemon there. Never write the master key or session token to a Host. Never change remote shell keybindings or open a remote Atuin TUI. |
| Tag scope | Only the `history` record tag is downloaded or uploaded. No other tag is read, written, or deleted. |
| Credential boundary | The Atuin session token and master key never enter Vault cloud sync payloads, portable exports, settings JSON, workspace files, logs, crash reports, notifications, or HTTP request/response logs. They live only in the field secret store under a dedicated entity type. |
| Store deletion | Netcatty never calls `DELETE /api/v0/store`. The "delete everywhere" action uploads tombstones only for rows this device knows; it cannot reach rows this device has never downloaded, and the UI says so. |
| Vault sync | Existing Vault cloud sync is unchanged and carries no Command History. There is no Netcatty-native history sync family. |
| Account model | One Atuin account per install. No in-app registration and no multi-account switcher. Logout removes the token and key from the field store while keeping local history. |
| Existing Host Atuin | Detection and import of an Atuin database on a Host are read-only: the Host database is never written and the Atuin key file is never read. |
| Server visibility | The server sees record identifiers and metadata only; command text, cwd, exit code, and origin are encrypted payload. This limitation is stated in the account panel. |

## Conformance evidence required before release

The compatibility claim is gated by this evidence:

- Golden vectors generated from the official Atuin CLI at the pinned version:
  key parsing, MessagePack V0/V1/V2 records, create/delete wrappers,
  PASETO/PASERK encryption, implicit-assertion tamper cases, and boundary
  values.
- A conformance suite against a self-hosted Atuin server v18.24.5 and the
  official CLI: CLI upload then Netcatty download, Netcatty upload then CLI
  list, CLI delete then Netcatty apply, Netcatty delete then CLI apply, two
  Netcatty writers with independent series, wrong key, version mismatch,
  interrupted upload, and an unknown tag that must be ignored.
- The suite is release acceptance evidence, not a per-pull-request gate. It is
  also the evidence used to move the reference pin.
- Until that evidence exists, this document publishes the policy the
  implementation must satisfy; it is not a claim that the sync client already
  passed conformance.
