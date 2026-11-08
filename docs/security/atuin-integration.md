# Atuin integration security review

Ticket: 20 (Security review and hardening)
Branch: `ticket20-manual`
Related ADRs: ADR-0001 (layered packages), ADR-0010 (field-secret key
hierarchy), ADR-0011 (cloud-sync wire format)

Status: review and hardening complete for this change set; the release gate at
the end is **not closed** because production sync wiring and on-device keychain
verification remain outside this worktree.

Reviewed: 2026-11-08, baseline `9a9dff5f7` plus the hardening in this change
set.
This review covers tickets 12-18 as integrated at that baseline: the domain
history model, the field-secret store, the history-sync engine and account
service, the pinned Atuin HTTP adapter, the read-only Atuin importer, Vault
cloud sync, portable export, and Local Backup.

## Scope and method

The review used three kinds of evidence, labelled throughout:

- **Code inspection**: the behaviour is visible in the named files.
- **Test-proven**: a named test exercises the boundary and fails if it regresses.
- **Not verified here**: the claim cannot be exercised in this environment. It
  is recorded as an open item rather than closed.

The review considered the non-negotiable constraints for this feature:

1. Atuin credentials never enter Vault cloud sync or a portable export.
2. A Local Backup may contain the encrypted secret, with the documented restore
   caveat.
3. The master key is only for Atuin sync; it is never written to a remote Host,
   and neither the Atuin binary nor an Atuin TUI is uploaded or run on a Host.
4. Local history stays plaintext at rest; no command text, token, key,
   request/response body, or decrypted payload appears in logs, crash reports,
   notifications, status strings, or error details.
5. A locked field-secret store or App Lock fails closed; no secret is read while
   locked.
6. `DELETE /api/v0/store` is never called; only the `history` record tag is used.
7. One Atuin account per install.

## Assets

| Asset | Where it lives | Sensitivity |
|---|---|---|
| Atuin session token | `secret_values` under `atuin_account/default/session_token`; plaintext only during an operation | Credential |
| Atuin master key | `secret_values` under `atuin_account/default/master_key`; parsed bytes only during a sync | Credential; decrypts all synced history |
| Atuin password | Entered in the account panel only; used for one login request; never stored | Credential |
| Command text and record payloads | Local `history_entries` (plaintext by design), record log, and encrypted wire records | Private user data |
| Vault credentials | `secret_values` ciphertext under Vault entity types | Credentials |
| Local Backup file and key | Encrypted file in `<userData>/vault-backups`; 32-byte key in the OS keychain | Contains credential ciphertext |
| Device-local sync metadata | `meta` rows: endpoint, user id, writer UUID, key fingerprint, checkpoints | Non-secret but device-local |

## Trust boundaries

| Boundary | Direction | Rule |
|---|---|---|
| Account panel -> services | in-process | Raw error text is never rendered; credentials leave bounded input fields only while an operation runs |
| Field-secret store -> Vault cloud sync | outbound | Only the explicit Vault entity allowlist may be published |
| Cloud payload -> field-secret store | inbound | Unknown entity types (including `atuin_account`) are rejected before any write |
| Vault service -> portable export | outbound | Only explicit Host, Key, Identity, Proxy, and Network references are read |
| Vault tables -> Local Backup | outbound | Fixed table whitelist; `secret_values` ciphertext is included; history state is not |
| Sync engine -> Atuin server | network | HTTPS (or loopback HTTP); no redirects; fixed paths; only the `history` tag |
| Atuin adapter -> sync engine | in-process | Public sentinels only; no server message, body, token, key, or payload in errors |
| Host Atuin database -> importer | read-only copy | `mode=ro`, `query_only`, only the `history` table, no key file |
| Session observer -> history capture | in-process | Observer is a read-only shadow; it never emits `CommandEvent` |

## Threat model

| # | Threat | Mitigation | Evidence | Status |
|---|---|---|---|---|
| T1 | An Atuin token or key is published to Vault cloud sync | `syncedEntityTypes` excludes `atuin_account`; `Snapshot` reads only explicit Vault refs; `mergeSecrets`/`filterSecretsToPayload` drop unrecognised types; `Apply` preflights inbound secrets | `adapters/syncvault/syncvault.go`; `TestSnapshotNeverCarriesAtuinAccountCredentials`; `TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything`; `application/sync/merge_test.go` `TestMergeDropsAtuinAccountCredentialsFromEverySide` | Test-proven |
| T2 | A credential is written by a portable export | Export reads credentials only through explicit Vault refs; no `atuin_account` path exists | `application/vault/export.go`; `TestPortableExportNeverCarriesAtuinCredentials` | Test-proven |
| T3 | A Local Backup stores the Atuin credential in plaintext | The backup carries `secret_values` ciphertext, and the whole snapshot is encrypted with AES-256-GCM under a keychain key | `adapters/store/vault_backup.go`; `adapters/vaultbackup/store.go`; `TestLocalBackupCarriesAtuinCiphertextButNotHistoryState`; `TestSaveEncryptsAndRoundTrips` | Test-proven |
| T4 | A credential, command, request body, or decrypted payload appears in an error, log, notification, status, or crash report | The sync packages import no logger; adapter errors contain only a fixed method/path/status; public errors are sentinels; status `Detail` is always empty; the account panel renders localized problem codes; no application crash reporter exists; history notifications do not exist | Code inspection of `application/historysync`, `adapters/atuin`, `ui/settings/atuin*.go`, `app/notifications.go`; existing no-leak and sanitized-status tests | Verified by inspection and tests; crash-dump caveat below |
| T5 | A credential is read while the field-secret store is locked or App Lock is engaged | `localStatus` returns `SyncLocked` before any `Has`/`Get`; `stopOrPause` runs before the token/key read; the account service now checks App Lock before every credential-touching operation | `TestLockedStoresNeverDecryptCredentials`; `TestAccountOperationsFailClosedWhileAppLocked`; `TestLoginRequiresAnUnlockedSecretStore` | Test-proven |
| T6 | The token or key is sent to a different account after the address changes | The stored account identity and normalized address are compared before the secret read and again after authentication; a mismatch requires logout | `TestStoredCredentialsFailClosedAfterTheSyncAddressChanges`; `sameAccountAddress` | Test-proven |
| T7 | Key material lingers after the operation | `Config.Wipe`, `Remote.Close`, `WipeMasterKey`, and the field-secret wipe are called at operation scope; the login request body and operation-scoped key copies are now zeroed | `TestCloseWipesCredentialsAndKeyAndIsIdempotent`; `TestAuthenticateWipesTheLoginRequestBody`; `TestSealSecretValueWipesTheTransientPlaintext`; `TestOpenSecretValueWipesTheDecryptedPlaintext` | Login body and store plaintext test-proven; caller key-copy wipes verified by inspection (no observable test); by-value copies remain (residual) |
| T8 | A token is forwarded to another origin through a redirect or hidden in the URL | The URL parser rejects userinfo, query, and fragment; plain HTTP is loopback-only; the HTTP client refuses every redirect | `normalizeBaseURL`; transport tests | Test-proven |
| T9 | The Atuin server learns command text | The server sees record identity and metadata, not payload plaintext; this is now stated in the account panel and is inherent to Atuin | Wire format and adapter inspection; `TestAtuinPanelStatesWhatTheServerCanSee` | Accepted limitation, disclosed |
| T10 | The Host Atuin import modifies the Host or reads the key file | The caller copies `history.db`/`-wal`/`-shm` locally; the reader opens the copy `mode=ro` with `query_only`, reads only the `history` table, and never reads a key file | `adapters/atuinimport/atuinimport.go`; importer tests | Verified by inspection and tests |
| T11 | A sync path calls a destructive Atuin endpoint or another tag | No method builds `/api/v0/store`; download/upload reject any tag other than `history`; `DELETE` is asserted absent by a transport test | `remote.go`; `TestStoreDeleteEndpointIsNeverRequested` | Test-proven |
| T12 | A malicious cloud payload overwrites local Atuin credentials | `validateIncomingSecrets` runs before any write and rejects unknown entity types | `TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything` | Test-proven |
| T13 | Observer output or session logs are fed into history capture | The observer is a read-only shadow terminal and has no command-completion hook; session logs are explicit user-started transcripts | `application/session/observe.go`; `observe_test.go`; `session_tap.go` | Verified by inspection |
| T14 | The UI claims a Local Backup restores the Atuin account | Backup wording talks only about the Vault; history settings, metadata, and the record log are not backup tables | `ui/locales/en.json` backup messages; `TestPreviousBuildLocalBackupRestoresAfterHistorySchemaGrowth` | Verified by inspection and tests |

## Findings and fixes

### F1: the login request body kept the password in a heap buffer

`adapters/atuin/remote/remote.go` marshals `{username,password}` into a `[]byte`
for the login POST. The buffer was never zeroed, so the password remained
readable in the heap until collection. The adapter now defers `zeroBytes(body)`
after the request completes.

Evidence: `adapters/atuin/remote/login_wipe_test.go`
`TestAuthenticateWipesTheLoginRequestBody` replays the body from the request's
`GetBody` closure after `Authenticate` and requires every byte to be zero.

### F2: field-secret plaintext buffers were not zeroed

`adapters/store/secrets.go` converted the decrypted plaintext to a string and
discarded the `[]byte`; `Set` passed an unnamed `[]byte(value)` temporary to
`Seal`. Both buffers stayed in the heap. `openSecretValue` and
`sealSecretValue` now own those buffers and wipe them with a deferred `wipe`.

Evidence: `adapters/store/secrets_zeroing_test.go`
`TestOpenSecretValueWipesTheDecryptedPlaintext` and
`TestSealSecretValueWipesTheTransientPlaintext` use a recording AEAD that keeps
the actual buffer the adapter handed to the cipher and assert it is zero after
the call. The existing round-trip test still proves the value survives.

### F3: the account service did not enforce App Lock

The sync engine already paused on App Lock, but `AccountService` had no App Lock
predicate. If a future caller or a non-UI path invoked `Login`, `SetMasterKey`,
`ConfirmUnverifiedKey`, `ForgetMasterKey`, `Logout`, or `Check` while App Lock
was engaged, the service could read or delete credentials. The service now
accepts `WithAccountAppLocked`; every credential-touching operation returns
`ErrAppLocked` before touching the secret store, and `localStatus` reports the
locked state without even a presence (`Has`) check. `app.go` wires the real App
Lock predicate, and the panel maps `ErrAppLocked` to the existing locked problem
code.

Evidence: `application/historysync/account_test.go`
`TestAccountOperationsFailClosedWhileAppLocked` asserts zero `Has`, `Get`, `Set`,
and `Delete` calls, unchanged credential values, no remote opens, and the locked
status; `ui/settings/atuin_test.go`
`TestAppLockMapsToTheLockedProblem` pins the localized mapping.

### F4: operation-scoped key copies were not wiped

`Download`, `Upload`, and `ValidateMasterKey` copy the remote key into a local
`wire.Key`. The copies are now wiped with a deferred `key.Wipe()`. Copies made
inside wire helpers and by-value parameters remain (see residual risks); Go has
no mechanism to enumerate them.

### F5: the account panel did not state what the Atuin server can see

The spec requires the account panel to state that the server sees record
identifiers and metadata but not command text, working directory, exit status,
or origin. The panel rendered only the local credential-storage description.
The description in all five catalogs now carries the limitation.

Evidence: `ui/settings/atuin_test.go`
`TestAtuinPanelStatesWhatTheServerCanSee` asserts the rendered English notice
contains "record identifiers and metadata" and "not command text";
`ui/locales/*.json` carry the translated strings.

### Not changed

- The session token is held as a Go string in `Config.Token`, `Remote.token`,
  and the request header. It cannot be zeroed; `Config.Wipe` and `Remote.Close`
  clear the references. This matches the spec's "where the HTTP API permits"
  language and is recorded as a residual risk.
- The spec also asks that sanitized error details be shown behind a details
  control. No details control exists: `Status.Detail` is never populated and
  the panel renders only localized problem codes. This is safe today because
  there is no raw error surface, but the literal spec surface is missing. If a
  details control is added, it must render only sanitized, non-secret text.
  Recorded as release-gate item 5 rather than silently claimed complete.
- `ErrAppLocked` and a passphrase-locked store both map to
  `AtuinProblemLocked`, whose copy says to unlock the field-secret store. App
  Lock hides the settings UI, so the path is not normally reachable; the copy
  is nonetheless imprecise for App Lock.
- App Lock is sampled at operation entry. If App Lock engages after the gate
  and before the secret read, an in-flight operation can still complete that
  read. Operations are short-lived and the next entry fails closed; closing the
  window fully would require the secret store itself to enforce the predicate.
- `Check` returns `ErrAppLocked` while a passphrase-locked store returns the
  locked status with a nil error. Both fail closed, and the panel maps the
  error to the same problem code.
- Server messages and bodies are never copied into errors. The adapter
  classifies status codes and reports only the fixed method and path.
- Imported Atuin rows are projection-only: `createRecord` returns nil for
  `SourceAtuinImport` and `SourceAtuinSync`, so an import never creates an
  upload record. Deleting an imported row appends a history delete tombstone,
  which is intended: the deletion must propagate to other devices.
- `SecretValue` merge and upload paths keep command text out of errors; command
  payloads are encrypted before upload and the upload body is ciphertext only.

## Boundary evidence

### Credential storage and lifetime

- The field-secret store encrypts each field with AES-256-GCM and AAD binding
  entity type, entity id, field, and key version. The field key is 32 random
  bytes kept in the OS store, an Argon2id-wrapped key for passphrase mode, or an
  in-memory session map (ADR-0010).
- Atuin credentials use the dedicated `atuin_account` entity. `Has` reads the
  row without decrypting, so the account panel can show presence without pulling
  the key into memory.
- A sync holds the token and parsed master-key bytes for one operation. The
  engine wipes the key bytes and the config; the remote wipes its stored key on
  `Close`. A login also wipes the request body (F1).
- The UI keeps password, token, and master-key drafts in bounded strings and
  clears them after a successful login, key save, or logout. The panel never
  renders or offers to reveal them.

### Logs, errors, notifications, and status

- `application/historysync`, `adapters/atuin`, `adapters/atuinimport`, and the
  history-secret adapter import no logging package. There is no log statement to
  redact in these paths.
- Adapter errors contain a sentinel plus a fixed method/path and HTTP status
  (never the server message or body). `publicRemoteError`, `publicSyncError`,
  `publicSecretError`, and `publicRemoteError` reduce internal errors to known
  sentinels before they reach callers.
- `account.Status.Detail` is only ever copied from a cached status and is
  cleared by `statusForRemoteError`; no code populates it with an error string.
  The history panel renders a localized `AtuinProblem` code, never `err.Error()`.
- `app/notifications.go` delivers terminal/program notifications only. There is
  no history-sync notification path, so no sync payload can reach a notification.
- There is no application crash reporter. A Go panic prints a stack trace, not
  the contents of string variables; the only `panic` in the reviewed crypto
  path is a fixed-argument programming error. OS crash dumps and hibernation
  images are outside the application's control (residual risk).

### Vault cloud sync

- Outbound: `adapters/syncvault.Vault.Snapshot` lists Vault records and calls
  `addSecret` only for explicit Host/Key/Identity/Proxy/Network refs. The
  `atuin_account` entity is never named.
- Inbound: `validateIncomingSecrets` rejects any entity type outside the
  allowlist before `records.ApplySync` or any secret write. A rejected payload
  leaves credentials, records, and settings untouched.
- Merge: `mergeSecrets` keys by entity and `filterSecretsToPayload` drops any
  entity type without a Vault family. The new merge test plants an
  `atuin_account` secret on all three sides and proves it is absent from the
  merged payload and its JSON.

### Portable export

- `exportDataset` reads credentials only through explicit Vault refs:
  `HostSecretRef`, `KeySecretRef`, `IdentitySecretRef`,
  `ProxyProfileSecretRef`, `NetworkProfileSecretRef`, and
  `NetworkPeerSecretRef`. Portable settings strip all History fields.
- The export spy test plants a token and master key under `atuin_account` and
  exports every format with `IncludeSecrets: true`. It asserts that no export
  content contains either value and that no export requested an
  `atuin_account` secret ref. It also requires at least one ordinary secret read,
  so the absence of an Atuin read is meaningful.

### Local Backup and restore caveat

- A Local Backup is a schema-versioned snapshot of a fixed Vault table
  whitelist. It includes `secret_values` and `crypto_keys`, so the Atuin
  credential is present as ciphertext, and `secret_values` contains no plaintext.
- The backup envelope is AES-256-GCM under a 32-byte key in the OS keychain;
  AAD binds the backup id, timestamp, fingerprint, and format. If the keychain
  is unavailable, the store refuses to write plaintext rather than degrading.
- History projections (`history_entries`), the record log (`history_records`),
  connection logs, and `meta` (account identity, writer UUID, checkpoints, key
  fingerprint, import checkpoints) are not backup tables.
- Restore caveat, as implemented and tested: the encrypted Atuin credential is
  restored and readable on the same device under the same field key, while the
  device-local account settings/identity and record log are not restored. A
  restore can therefore leave an orphan credential until the user re-enters the
  endpoint and username. The backup UI speaks only about restoring the Vault and
  does not claim the Atuin account is restored. The test also proves the record
  log and metadata rows survive a restore, because restore rewrites only the
  Vault table set.

### App Lock and locked secret stores

- `Service.localStatus` checks for a passphrase-locked store and App Lock before
  any `Has`/`Get`; `stopOrPause` checks again immediately before `secrets.Get`
  and key loading. A locked store reports `SyncLocked` and a zero retry time.
- `AccountService` now has the same gate (F3). Its `localStatus` returns the
  locked state without a presence check; every credential-touching operation
  returns `ErrAppLocked` before touching the store.
- Capture is independent of credentials. A locked store or App Lock pauses sync
  but does not pause local history capture.
- Tests cover the sync path (`TestLockedStoresNeverDecryptCredentials`,
  `TestLockedSecretsPauseSyncWithoutOpeningARemote`,
  `TestAppLockPausesEveryAutomaticTrigger`) and the account path
  (`TestAccountOperationsFailClosedWhileAppLocked`).

### Observer and session logs

- The Session observer is a read-only shadow terminal (`Observation`); it has
  no command-completion hook and cannot write to the Session. History capture is
  emitted only by the Session tap's `CommandEvent`, after a command finishes or
  the Session ends. An observer therefore cannot inject a command into history,
  and observing a Session does not duplicate commands.
- Session logs are user-initiated transcripts of terminal output. They are
  separate from history capture and from sync; they are not uploaded and are not
  part of a Local Backup. Credentials entered in the account panel are not
  terminal input and never enter a session log.

### Imported Atuin records

- The Host import copies `history.db`, `history.db-wal`, and `history.db-shm`
  into a local temporary directory and opens the copy read-only with
  `query_only=1`. It reads only the `history` table, requires the `id`,
  `timestamp`, and `command` columns, and rejects an unknown schema or an
  inconsistent copy. It never reads the Atuin key file and never writes to the
  copy.
- Rows map to local projections only. A valid Atuin UUID is preserved; otherwise
  a stable derived UUID is used. Unknown columns are preserved and bounded.
  Malformed rows fail the whole copy before any checkpoint or row is written.
  Row-validation errors name the history row id, never the command text.
  Wrapped SQLite driver errors (`query`/`scan`/`rows.Err`) still use `%v`;
  inspection shows they carry diagnostics such as column names, SQL text, and
  file paths rather than row values, but no test proves every future driver
  diagnostic is value-free (residual risk).
- Imports create no upload records, so an import cannot push Host data to an
  Atuin server. Deletes of imported rows append tombstones, as required for
  convergent deletion. The import checkpoint advances only in the same
  transaction as the projections.

### Destructive endpoint and tag scope

- No code path builds `DELETE /api/v0/store`. The only mutation is `POST` to
  `/api/v0/record`, and downloads/status use `/api/v0/record` and
  `/api/v0/record/next`. Every record path rejects a tag other than `history`.
- The transport test asserts no request uses a DELETE method or a path containing
  `store`.

## Acceptance checklist

| Criterion | Status | Notes |
|---|---|---|
| Token and key storage, memory lifetime, zeroing, and lock behaviour are reviewed and documented | **Reviewed and documented** | F1, F2, F4 fixed; Go memory limits recorded below |
| Logs, crash reports, notifications, and error details never contain command text, tokens, keys, request bodies, or decrypted payloads | **Verified for application-controlled outputs** | No logger or crash reporter in the sync paths; sentinel errors; problem codes; OS crash dumps are outside application control. The spec's details-control surface is absent (release-gate item 5) |
| Atuin credentials never enter Vault cloud sync or portable export; Local Backup behaviour and its restore caveat match the spec | **Verified by tests** | Sync snapshot/apply/merge, export spy, and Local Backup tests; production sync wiring not present (release gate) |
| Observer/session-log interaction and imported-record behaviour are reviewed | **Reviewed and documented** | Observer has no command hook; imports are read-only, projection-only, and tombstone on delete |
| The security checklist is closed before release | **Not closed** | The in-scope checklist is closed; release-gate items below remain |

## Release gate (not closed)

These items cannot be closed in this worktree and must be completed before a
release claim:

1. **Production remote wiring.** `app/app.go` still constructs the account and
   sync services with a nil `Factory`; the comment states that the HTTP factory
   arrives with the sync engine's transport ticket. The pinned adapter is tested
   against `httptest` servers and the engine against a fake factory, but no
   production build in this worktree can reach the real Atuin server. End-to-end
   redaction and credential lifetime through the composition root are therefore
   **not verified here**. This is blocked by the transport/integration ticket
   (ticket 19 in the implementation plan), not by this review.
2. **Real-platform keychain, passphrase, session-only, and App Lock behaviour.**
   Unit tests use fakes (`platform/secrets` is not exercised against a live
   macOS Keychain, Windows Credential Manager, or Linux secret service here).
   The field-key and backup-key lifecycle on each target OS still needs an
   on-device check.
3. **Crash-dump and swap review on each target OS.** The application has no crash
   reporter, but OS crash dumps, hibernation files, and swap may capture process
   memory. No core-dump or mlock behaviour was verified here.
4. **Conformance and release acceptance.** The pinned-server conformance suite
   and the Host/Shell matrix are separate release tasks (ticket 19 and the
   hardening phase). They were not run for this review.
5. **Account-panel details control.** Spec §11 asks for sanitized error details
   behind a details control. The panel intentionally has no detail surface and
   `Status.Detail` is never populated. Adding one is UI work outside this
   hardening change; until then the literal spec requirement is open, although
   no raw error can leak.

The checklist above must not be reported as fully closed while any of these
items is open.

## Residual risks

- **Go strings cannot be wiped.** Passwords, tokens, base64 master keys, command
  text, server URLs, and usernames exist as immutable strings; the runtime may
  retain copies until GC and the OS may page them. `Config.Wipe` and
  `Remote.Close` clear references; the field-secret `Get` must return a string.
  Zeroing is best effort.
- **Compiler dead-store elimination.** The wipe loops are not backed by an
  explicit compiler barrier such as `runtime.KeepAlive`. The buffers involved
  escape to interfaces or are shared, so the observed tests see the zeroing, but
  Go does not guarantee that an unobserved zero store survives optimisation.
- **Unwiped copies remain.** `wire.Key` is a 32-byte array passed by value into
  wire helpers; `DecryptData`, `EncryptData`, JSON marshal buffers for uploads,
  and some map/interface boxing create transient copies that are not all wiped.
  Operation-scoped copies in `Download`, `Upload`, and `ValidateMasterKey` are
  wiped; a fully copy-free design would require a larger wire-API change.
- **Decrypted record payloads** are transient `[]byte` values used to build
  journal rows. They are not wiped after mapping. Local history is plaintext at
  rest by design, so this is a lifetime hardening gap rather than a new
  disclosure boundary.
- **No mlock / no crash-dump exclusion.** Process memory, swap, hibernation
  images, and OS crash dumps may contain credentials and command text.
- **OS keychain trust.** The field key and backup key are only as protected as
  the user's OS session. Session-only mode keeps credentials in process memory
  for the run; that is the documented trade-off when no OS store exists.
- **Local Backup is only as strong as the keychain key.** The backup file
  contains credential ciphertext; anyone who can read both the backup file and
  the backup key (or the same user's keychain) can decrypt it. The backup is
  device-local by design.
- **Restore caveat.** A restore may leave an orphan `atuin_account` credential
  because account settings and history metadata are not Vault tables. The user
  must re-enter the endpoint and username; the UI does not claim otherwise.
- **App Lock is enforced by an injected predicate.** `app.go` supplies the real
  predicate to both services. A future composition that omits it would not gate
  the service layer; the sync engine and account service tests pin the predicate
  behaviour, but a constructor audit remains a code-review responsibility. The
  predicate is sampled at operation entry, so a lock engaging mid-operation can
  let an already-started secret read finish; the window is small and the next
  operation fails closed.
- **Importer diagnostics.** SQLite driver errors wrapped with `%v` and history
  row ids may appear in import errors; these are not command text or
  credentials, but they are identifiers. No test proves every future SQLite
  diagnostic is value-free.
- **Server-visible metadata.** The Atuin server sees record id, host, tag,
  index, timestamp, and version. This is inherent to the protocol and is now
  stated in the account panel (F5); it is not a defect in this client.

## Test evidence

Hardening tests added in this change:

- `adapters/atuin/remote/login_wipe_test.go`: `TestAuthenticateWipesTheLoginRequestBody`
- `adapters/store/secrets_zeroing_test.go`: `TestSealSecretValueWipesTheTransientPlaintext`, `TestOpenSecretValueWipesTheDecryptedPlaintext`, `TestOpenSecretValueReportsAnAuthFailure`
- `application/vault/export_atuin_boundary_test.go`: `TestPortableExportNeverCarriesAtuinCredentials`
- `application/sync/merge_test.go`: `TestMergeDropsAtuinAccountCredentialsFromEverySide`
- `adapters/store/vault_backup_atuin_test.go`: `TestLocalBackupCarriesAtuinCiphertextButNotHistoryState`
- `adapters/atuinimport/atuinimport_test.go`: `TestMalformedRowErrorNeverContainsTheCommand`
- `application/historysync/account_test.go`: `TestAccountOperationsFailClosedWhileAppLocked`; added no-credential-read assertions to `TestLoginRequiresAnUnlockedSecretStore` and `TestCheckDoesNothingWhileDisabledLockedOrUnconfigured`
- `application/historysync/service_test.go`: `TestLockedStoresNeverDecryptCredentials`
- `ui/settings/atuin_test.go`: `TestAppLockMapsToTheLockedProblem`;
  `TestAtuinPanelStatesWhatTheServerCanSee`
- `ui/locales/*.json`: the account description in all five catalogs now states
  the server-visible metadata limitation

Existing tests relied on for boundaries:

- `adapters/syncvault/syncvault_test.go`: `TestSnapshotNeverCarriesAtuinAccountCredentials`, `TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything`
- `adapters/atuin/remote/transport_test.go`: `TestCloseWipesCredentialsAndKeyAndIsIdempotent`, no-DELETE/no-store assertions, redirect and error-sanitization tests
- `adapters/vaultbackup/store_test.go`: `TestSaveEncryptsAndRoundTrips` and keychain-unavailable refusal
- `application/history/service_test.go`: `TestRecordDoesNotCreateWireRecordsForRemoteOrLegacyRows`, `TestDeleteEntryOfAnImportedRowAppendsATombstone`, import checkpoint and malformed-row tests
- `domain/preferences/history_test.go`: `TestPortableStripsHistoryAccountSettings`
- `application/session/observe_test.go`: observer read-only tests

The quality gate for this change is the full Go suite
(`nix develop --command env GOFLAGS=-buildvcs=false go test ./... -p 1 -count=1`),
`go vet ./...`, and `gofmt -l` on the changed files. Targeted package runs and
the full gate are reported with this change set; they do not substitute for the
open release-gate items above.
