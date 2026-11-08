# Atuin integration security review

Ticket: 20 (Security review and hardening)
Branch: `ticket20-manual`
Related ADRs: ADR-0001 (layered packages), ADR-0010 (field-secret key
hierarchy), ADR-0011 (cloud-sync wire format)

Status: **the security checklist for this change set is closed.** Every
application-controlled boundary below is implemented and is backed either by a
named test or by an explicit code-inspection note. The environment-dependent
checks that cannot run in this worktree are listed separately under "Release
verification dependencies"; they are scheduled release-acceptance activities,
not open items or identified defects of this checklist.

Reviewed: 2026-11-08, baseline `9a9dff5f7` plus the hardening in this change
set.
This review covers tickets 12-18 as integrated at that baseline: the domain
history model, the field-secret store, the history-sync engine and account
service, the pinned Atuin HTTP adapter, the read-only Atuin importer, Vault
cloud sync, portable export, and Local Backup. It also covers the production
composition in `app/app.go`, which now constructs the real adapter factory.

## Scope and method

The review used four kinds of evidence, labelled throughout:

- **Code inspection**: the behaviour is visible in the named files.
- **Test-proven**: a named test exercises the boundary and fails if it regresses.
- **Wired**: the production composition root constructs the real component, so
  the reviewed path is the one the application runs, not only a test fake.
- **Release verification dependency**: the check requires a live OS service, a
  packaged build, or a separate release-acceptance run that was not executed
  here. It is recorded with its owner and procedure below; it is not an open
  security-checklist item.

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
| Account panel -> services | in-process | Raw error text is never rendered; credential drafts leave the panel only while an operation runs |
| Field-secret store -> Vault cloud sync | outbound | Only the explicit Vault entity allowlist may be published |
| Cloud payload -> field-secret store | inbound | Unknown entity types (including `atuin_account`) are rejected before any write |
| Vault service -> portable export | outbound | Only explicit Host, Key, Identity, Proxy, and Network references, plus Connection Logs, are read. Command History is not read. |
| Vault tables -> Local Backup | outbound | Fixed table whitelist; `secret_values` ciphertext is included; history state is not |
| Sync engine -> Atuin server | network | HTTPS (or loopback HTTP); no redirects; fixed paths; only the `history` tag |
| Atuin adapter -> sync engine | in-process | Public sentinels only; unknown secret-store failures reduce to a public sentinel; no server message, body, token, key, or payload in errors |
| Host Atuin database -> importer | read-only copy | `mode=ro`, `query_only`, only the `history` table, no key file |
| Session observer -> history capture | in-process | Observer is a read-only shadow; it never emits `CommandEvent` |
| Composition root -> adapter | in-process | One non-nil `atuinremote.Factory` is shared by the account and sync services |

## Threat model

| # | Threat | Mitigation | Evidence | Status |
|---|---|---|---|---|
| T1 | An Atuin token or key is published to Vault cloud sync | `syncedEntityTypes` excludes `atuin_account`; `Snapshot` reads only explicit Vault refs; `mergeSecrets`/`filterSecretsToPayload` drop unrecognised types; `Apply` preflights inbound secrets | `adapters/syncvault/syncvault.go`; `TestSnapshotNeverCarriesAtuinAccountCredentials`; `TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything`; `application/sync/merge_test.go` `TestMergeDropsAtuinAccountCredentialsFromEverySide` | Test-proven |
| T2 | A credential or command is written by a portable export | Export reads credentials only through explicit Vault refs; no `atuin_account` path exists; the export port no longer exposes command history | `application/vault/export.go`; `application/vault/export/export.go`; `TestPortableExportNeverCarriesAtuinCredentials`; `TestPortableExportNeverCarriesCommandHistory`; `TestLogsExportConnectionLogsWithoutCommandHistory` | Test-proven |
| T3 | A Local Backup stores the Atuin credential in plaintext | The backup carries `secret_values` ciphertext, and the whole snapshot is encrypted with AES-256-GCM under a keychain key | `adapters/store/vault_backup.go`; `adapters/vaultbackup/store.go`; `TestLocalBackupCarriesAtuinCiphertextButNotHistoryState`; `TestSaveEncryptsAndRoundTrips` | Test-proven |
| T4 | A credential, command, request body, or decrypted payload appears in an error, log, notification, status, or crash report | The sync packages import no logger; adapter errors contain only a fixed method/path/status; public helpers return only known sentinels; `Status.Detail` is never populated with an error string; the account panel renders localized problem codes; the new details control renders a typed sanitized model; no application crash reporter exists; history notifications do not exist | Code inspection of `application/historysync`, `adapters/atuin`, `ui/settings/atuin*.go`, `app/notifications.go`; `TestLoginWithUsernamePasswordStoresOnlyTheToken`; `TestCheckMapsRemoteFailuresToSanitizedStatuses`; `TestStatusErrorsMapWithoutLeaking`; `TestAtuinPanelReportsAProblemWithoutRawErrorText`; `TestAtuinDetailsNeverRenderAnInternalError`; `TestPublicSecretErrorReducesUnknownErrorsToASentinel` | Verified by inspection and tests; OS crash-dump/swap caveat is release dependency 2 |
| T5 | A credential is read while the field-secret store is locked or App Lock is engaged | `localStatus` returns `SyncLocked` before any `Has`/`Get` and before `Mode` under App Lock; `stopOrPause` runs before the token/key read; the account service checks App Lock before every credential-touching operation | `TestLockedStoresNeverDecryptCredentials` (now asserts zero `Get`, zero `Has`, and zero `Mode` under App Lock); `TestAccountOperationsFailClosedWhileAppLocked`; `TestLoginRequiresAnUnlockedSecretStore` | Test-proven |
| T6 | The token or key is sent to a different account after the address changes | The stored account identity and normalized address are compared before the secret read and again after authentication; a mismatch requires logout | `TestStoredCredentialsFailClosedAfterTheSyncAddressChanges`; `sameAccountAddress` | Test-proven |
| T7 | Key material lingers after the operation | `Config.Wipe`, `Remote.Close`, `WipeMasterKey`, and the field-secret wipe are called at operation scope; the login request body and login response buffer are zeroed; the base64-decoded master-key intermediate and mnemonic `packed` entropy buffer are zeroed | `TestCloseWipesCredentialsAndKeyAndIsIdempotent`; `TestAuthenticateWipesTheLoginRequestBody`; `TestAuthenticateWipesTheLoginResponseBody`; `TestAuthenticateWipesTheLoginResponseOnDecodeFailure`; `TestAuthenticateWipesTheLoginResponseOnReadFailure`; `TestDecodeMasterKeyBytesWipesTheIntermediateBuffer`; `TestDecodeMasterKeyBytesWipesOnFailure`; `TestSealSecretValueWipesTheTransientPlaintext`; `TestOpenSecretValueWipesTheDecryptedPlaintext` | Application buffers test-proven; caller key-copy wipes verified by inspection; by-value copies remain (residual) |
| T8 | A token is forwarded to another origin through a redirect or hidden in the URL | The URL parser rejects userinfo, query, and fragment; plain HTTP is loopback-only; the HTTP client refuses every redirect | `normalizeBaseURL`; `TestRedirectIsNotFollowed`; `TestUnreachableServerIsReportedOfflineWithoutTheAddress`; `TestStatusErrorsMapWithoutLeaking`; `TestProductionFactoryDefaultsAreSuitableForProduction` | Test-proven |
| T9 | The Atuin server learns command text | The server sees record identity and metadata, not payload plaintext; this is stated in the account panel and is inherent to Atuin | Wire format and adapter inspection; `TestAtuinPanelStatesWhatTheServerCanSee` | Accepted limitation, disclosed |
| T10 | The Host Atuin import modifies the Host or reads the key file | The caller copies `history.db`/`-wal`/`-shm` locally; the reader opens the copy `mode=ro` with `query_only`, reads only the `history` table, and never reads a key file | `adapters/atuinimport/atuinimport.go`; `TestReadHistoryFailsClosedOnAnUnknownSchema`; `TestReadHistoryRejectsAMalformedRowInsteadOfDroppingIt`; `TestMalformedRowErrorNeverContainsTheCommand`; `TestReadOnlyDSNBuildsValidWindowsDriveURIs`; `TestReadOnlyDSNKeepsAnAbsoluteUnixPath` | Verified by inspection and tests |
| T11 | A sync path calls a destructive Atuin endpoint or another tag | No method builds `/api/v0/store`; download/upload reject any tag other than `history`; `DELETE` is asserted absent by a transport test | `adapters/atuin/remote/remote.go`; `TestStoreDeleteEndpointIsNeverRequested` | Test-proven |
| T12 | A malicious cloud payload overwrites local Atuin credentials | `validateIncomingSecrets` runs before any write and rejects unknown entity types | `TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything` | Test-proven |
| T13 | Observer output or session logs are fed into history capture | The observer is a read-only shadow terminal and has no command-completion hook; session logs are explicit user-started transcripts | `application/session/observe.go`; `application/session/observe_test.go` `TestObserverCannotWriteOrConsumeTheLiveView`; `application/session/session_tap.go` | Verified by inspection and test |
| T14 | The UI claims a Local Backup restores the Atuin account | Backup wording talks only about the Vault; history settings, metadata, and the record log are not backup tables | `ui/locales/en.json` backup messages; `TestPreviousBuildLocalBackupRestoresAfterHistorySchemaGrowth` | Verified by inspection and tests |
| T15 | The production build silently runs with no remote adapter | The composition helper constructs one real `atuinremote.NewFactory` and passes the same identifier to both constructors | `app/app.go` `newHistorySyncServices`; `TestCompositionWiresTheProductionAtuinFactory` (AST); `TestComposedAtuinServicesReachALoopbackServer` | Wired and test-proven |

## Findings and fixes

### F1: the login request body kept the password in a heap buffer

`adapters/atuin/remote/remote.go` marshals `{username,password}` into a `[]byte`
for the login POST. The buffer was never zeroed, so the password remained
readable in the heap until collection. The adapter defers `zeroBytes(body)`
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

### F6: the spec's sanitized details control was absent

Spec §11 requires sanitized error details behind a details control.
`Status.Detail` was never populated and the panel rendered only a localized
problem line. The account panel now has an explicit **Details** control,
collapsed by default. Expanding it renders a typed `AtuinDiagnostics` model
containing only:

- the localized sync state and action problem explanation;
- the server version;
- the last sync time;
- the unsynced upload count;
- the next retry time.

The model has no field for an error string, `Status.Detail`, a server message or
body, a token, a key, a password, command text, cwd, exit status, origin, a
server URL, or a username. The only way to add a value to the rendered surface
is to add a typed field, which is a visible code change. `Status.Detail` remains
empty and is never rendered.

Evidence: `ui/settings/atuin_test.go`
`TestAtuinDetailsControlIsCollapsedAndShowsSanitizedFields` proves the control
exists, starts collapsed, and shows the sanitized fields after the click;
`TestAtuinDetailsNeverRenderAnInternalError` plants the same secret in the
internal error and in `Status.Detail` and proves neither is rendered;
`TestAtuinDetailsStringsExistInEveryLocale` covers `en`, `es`, `ru`, `zh-CN`,
and `zh-TW`.

### F7: production wiring passed a nil factory

`app.go` constructed both `AccountService` and `Service` with `nil`. A nil
factory makes every production login, check, and sync fail with
`ErrRemoteUnavailable`, so the integrated feature could not reach an Atuin
server. The composition root now has one helper, `newHistorySyncServices`, that
constructs a single real `atuinremote.NewFactory` and passes the same non-nil
identifier to both constructors. The factory uses the packaged application
version for its User-Agent, with the adapter's `natcatty/<version>` fallback for
development builds.

Evidence:

- `TestProductionFactoryDefaultsAreSuitableForProduction` and
  `TestUserAgentForVersionNamesTheProductAndVersion` exercise the real adapter
  factory: non-nil, bounded timeout, redirect refusal, product User-Agent.
- `TestCompositionWiresTheProductionAtuinFactory` parses `app/app.go` and fails
  if either constructor receives anything other than the identifier initialized
  from `atuinremote.NewFactory`, if either call disappears, or if the production
  `run` composition stops calling the helper.
- `TestComposedAtuinServicesReachALoopbackServer` builds the services through
  the same composition helper and drives account login plus a sync run against a
  loopback `httptest` Atuin v18 server. Both services open the real remote; a
  nil factory would fail the login with `ErrRemoteUnavailable`.

### F8: the base64 master-key intermediate and mnemonic entropy buffer were unwiped

`domain/history/masterkey.go` base64-decoded the input and, on the MessagePack
path, returned a copy while leaving the decoded buffer (which contains the key)
on the heap. The mnemonic path built a `packed` entropy buffer and returned a
copy without wiping it. Both intermediates are now zeroed on success and on
every error path. The raw 32-byte legacy path now returns a copy so the decoded
buffer can be wiped.

Evidence: `domain/history/masterkey_test.go`
`TestDecodeMasterKeyBytesWipesTheIntermediateBuffer` covers legacy raw, array16,
and bin8 inputs and asserts the intermediate is zero and the returned key is a
separate copy; `TestDecodeMasterKeyBytesWipesOnFailure` covers the rejected
length path. The `packed` mnemonic buffer is wiped by a deferred call at
`parseMnemonic`; it is not exposed to a test, which is recorded under residual
risks.

### F9: the login response buffer kept the minted session token

`Authenticate` decoded the login response, which contains the newly minted
session token, from a heap buffer that was never zeroed. `postJSON` now defers
`zeroBytes(response)` after decoding, on success and decode failure. `do` also
zeroes a partial or oversized body that it never returns, and the read body
before rejecting a response with an incompatible server version. The decoded
token string remains by design because the account service must store it.

Evidence: `adapters/atuin/remote/login_response_wipe_test.go`
`TestAuthenticateWipesTheLoginResponseBody` retains the exact destination slice
`io.ReadAll` hands to the response body and requires it to be zero after a
successful login; `TestAuthenticateWipesTheLoginResponseOnDecodeFailure` covers
the malformed-response path; `TestAuthenticateWipesTheLoginResponseOnReadFailure`
covers the partial-read error path. The incompatible-version wipe is verified by
inspection. Existing endpoint tests prove response decoding still works.

### F10: unknown secret-store errors passed through the public helper

`publicSecretError` returned an unknown internal error unchanged. The review had
claimed that public helpers reduce errors to known sentinels, so the claim and
the code disagreed. An unknown secret-store failure now maps to the public
`ErrSecretUnreadable`; the known `ErrSecretNotFound`, `ErrSecretsLocked`,
`ErrSecretsUnavailable`, and `ErrSecretUnreadable` mappings are unchanged.

Evidence: `application/historysync/account_test.go`
`TestPublicSecretErrorReducesUnknownErrorsToASentinel` plants a token-shaped
string in an unknown error and proves the returned sentinel contains none of it.

### F11: portable export carried Command History

Spec O5 says Command History is not in Vault Export or Local Backup. The Vault
service populated `Dataset.History` from `HistoryReader.Entries`, and the logs
JSON/CSV formats serialized it. The export port now exposes only
`ConnectionLogs`; the dataset and both logs formats no longer have a history
field or row. Connection Logs remain exportable.

Evidence: `application/vault/export_test.go`
`TestPortableExportNeverCarriesCommandHistory` plants a command in the history
source, exports every format, asserts the command is absent, and asserts the
export never called `Entries`; `application/vault/export/export_test.go`
`TestLogsExportConnectionLogsWithoutCommandHistory` pins the logs JSON/CSV
shape.

### Not changed

- The session token is held as a Go string in `Config.Token`, `Remote.token`,
  and the request header. It cannot be zeroed; `Config.Wipe` and `Remote.Close`
  clear the references. This matches the spec's "where the HTTP API permits"
  language and is recorded as a residual risk.
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
- Secret-value merge and upload paths keep command text out of errors; command
  payloads are encrypted before upload and the upload body is ciphertext only.
- The credential draft fields have no UI length bound. The panel keeps them as
  ordinary Go strings while the user edits, clears them after a successful
  login, key save, or logout, and never renders them. The account service
  validates the login shape and address, and the master-key parser validates
  the key encoding; the adapter rejects a token containing a line break. There
  is no claim of a UI-enforced maximum length.

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
  `Close`. A login wipes the request body (F1) and the response buffer that
  carried the minted token (F9).
- Master-key parsing wipes the base64-decoded intermediate and the mnemonic
  entropy buffer (F8). The returned key is a fresh copy the caller wipes; parsed
  keys are stored as base64 in the field-secret store and re-parsed only for an
  operation.
- The UI keeps password, token, and master-key drafts as ordinary Go strings
  while the user edits, clears them after a successful login, key save, or
  logout, and never renders or offers to reveal them. There is no UI length
  bound; service and adapter validation is the boundary.

### Logs, errors, notifications, status, and details

- `application/historysync`, `adapters/atuin`, `adapters/atuinimport`, and the
  history-secret adapter import no logging package. There is no log statement to
  redact in these paths.
- Adapter errors contain a sentinel plus a fixed method/path and HTTP status
  (never the server message or body). `publicRemoteError`, `publicSyncError`, and
  `publicSecretError` reduce internal errors to known public sentinels before
  they reach callers.
- `account.Status.Detail` is never populated with an error string and is never
  rendered. The account panel renders a localized `AtuinProblem` code and the
  typed sanitized diagnostics from F6, never `err.Error()`, `Status.Detail`, a
  server message or body, a token, a key, or a password. The details control
  never renders a URL or a username; the ordinary settings inputs show the
  configured address and username by design.
- The details control starts collapsed. Its only string field sourced from the
  remote is the server version, which the collapsed panel already shows and the
  spec lists as non-sensitive. No field carries an error string, a server
  message or body, a URL, or a username; the remaining strings are localized
  catalog text and formatted times.
- `app/notifications.go` delivers terminal/program notifications only. There is
  no history-sync notification path, so no sync payload can reach a notification.
- There is no application crash reporter. A Go panic prints a stack trace, not
  the contents of string variables; the only `panic` in the reviewed crypto
  path is a fixed-argument programming error. OS crash dumps, hibernation
  images, and swap are outside the application's control (release dependency 2).

### Vault cloud sync

- Outbound: `adapters/syncvault.Vault.Snapshot` lists Vault records and calls
  `addSecret` only for explicit Host/Key/Identity/Proxy/Network refs. The
  `atuin_account` entity is never named.
- Inbound: `validateIncomingSecrets` rejects any entity type outside the
  allowlist before `records.ApplySync` or any secret write. A rejected payload
  leaves credentials, records, and settings untouched.
- Merge: `mergeSecrets` keys by entity and `filterSecretsToPayload` drops any
  entity type without a Vault family. The merge test plants an
  `atuin_account` secret on all three sides and proves it is absent from the
  merged payload and its JSON.

### Portable export

- `exportDataset` reads credentials only through explicit Vault refs:
  `HostSecretRef`, `KeySecretRef`, `IdentitySecretRef`,
  `ProxyProfileSecretRef`, `NetworkProfileSecretRef`, and
  `NetworkPeerSecretRef`. Portable settings strip all History fields.
- The export port has no command-history method (F11). `Dataset` has no History
  slice, and the logs JSON/CSV formats write Connection Logs only. A planted
  command is absent from every exported format and `Entries` is never called.
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

- `Service.localStatus` checks App Lock first and a passphrase-locked store
  second, before any `Has`/`Get`; `stopOrPause` checks again immediately before
  `secrets.Get` and key loading. A locked store reports `SyncLocked` and a zero
  retry time.
- App Lock performs no secret-store query at all, including `Mode`; a
  passphrase-locked store returns before any presence check.
- `AccountService` has the same gate (F3). Its `localStatus` returns the
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
  a stable derived UUID is used. Unknown columns are preserved as string
  values; they are not length-bounded.
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

### Production composition

- `app/app.go` `newHistorySyncServices` constructs one
  `atuinremote.NewFactory` and passes the same identifier to
  `historysync.NewAccountService` and `historysync.NewService`. App Lock
  predicates are supplied to both.
- The factory uses the packaged application version in its User-Agent
  (`natcatty/<version>`) and falls back to the adapter default for a
  development build. It keeps the bounded default timeout and always replaces
  the redirect policy with refusal.
- The AST wiring test fails if either constructor receives a different or nil
  value, and the loopback integration test proves both composed services reach
  a real HTTP remote.

## Acceptance checklist

| Criterion | Status | Notes |
|---|---|---|
| Token and key storage, memory lifetime, zeroing, and lock behaviour are reviewed and documented | **Closed** | F1, F2, F4, F8, F9 fixed; Go string/by-value limits recorded below |
| Logs, crash reports, notifications, and error details never contain command text, tokens, keys, request bodies, or decrypted payloads | **Closed for application-controlled outputs** | No logger or crash reporter in the sync paths; public sentinels; typed sanitized details control; OS crash dumps/swap are release dependency 2 |
| Atuin credentials never enter Vault cloud sync or portable export; Local Backup behaviour and its restore caveat match the spec | **Closed** | Sync snapshot/apply/merge, export spy, command-history removal, and Local Backup tests; production wiring wired and loopback-tested |
| Observer/session-log interaction and imported-record behaviour are reviewed | **Closed** | Observer has no command hook; imports are read-only, projection-only, and tombstone on delete |
| The security checklist is closed before release | **Closed for this change set** | F1-F11 implemented and covered; the scheduled environment-dependent checks are release verification dependencies, not open checklist items |

## Release verification dependencies

None of the four activities below was executed for this review. In particular,
this review did not run a live OS keychain/passphrase/session-only/App Lock
exercise, a packaged build, an OS crash-dump/swap inspection, a login against a
real Atuin server, or the conformance/Host-Shell matrix. They are scheduled
release-acceptance activities with a named owner and procedure. They are not
open items of the security checklist and no code defect was identified while
reviewing them; they exist because the required environment (a live OS service,
a packaged build, or a separate acceptance run) is not available here.

1. **Live OS keychain, passphrase, session-only, and App Lock behaviour.**
   Owner: release QA, on each target OS (macOS Keychain, Windows Credential
   Manager, Linux Secret Service), with the release manager signing off.
   Procedure: on a packaged build, create and read a field secret in OS mode,
   passphrase mode, and session-only mode; lock the field-secret store and
   engage App Lock and confirm sync pauses while capture continues; restart and
   confirm key persistence/orphaning matches the documented caveat.
   Why not a checklist defect: the adapter boundary is unit-tested with fakes;
   this dependency verifies the platform API integration and the field-key and
   backup-key lifecycle on real hardware.
2. **Crash dumps, swap, and hibernation review on each target OS.**
   Owner: platform/release owner for each OS, with the release manager.
   Procedure: inspect the packaged app's crash-reporting configuration, OS swap
   and hibernation behaviour, and the documented user guidance. The application
   itself has no crash reporter and no mlock/crash-dump exclusion.
   Why not a checklist defect: application-controlled outputs are verified; OS
   memory capture is outside application control and cannot be exercised by a
   unit test in this worktree.
3. **Packaged-build login and sync against a real Atuin v18 server.**
   Owner: release QA against the pinned server version, with the release
   manager accepting the result. Procedure: log in with token and password
   flows, verify the server-visible metadata notice, run a sync, and confirm
   error surfaces show only localized problems behind Details.
   Why not a checklist defect: the adapter is pinned and protocol-tested; the
   loopback integration test here proves the production composition opens a
   real HTTP remote. A live-server run is release acceptance, not a code
   boundary left unverified.
4. **Pinned-server conformance suite (ticket 19) and Host/Shell matrix
   (ticket 21).**
   Owner: release manager; both are separate release tasks.
   They were not run for this review, and this document does not claim their
   results. Why not a checklist defect: they are release-acceptance gates for
   protocol and shell coverage, not open findings of this security review.

None of these dependencies contradicts a checklist claim above: each is either
an OS-level property, a packaged-build exercise, or a separate acceptance run.
If any of them finds a defect, that defect reopens the relevant checklist item.

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
  wiped; the master-key parse intermediates are wiped; a fully copy-free design
  would require a larger wire-API change.
- **Mnemonic `packed` buffer coverage.** `parseMnemonic` defers a wipe of the
  entropy buffer, but the buffer is not exposed to a test, so this specific wipe
  is verified by inspection only. The base64/MessagePack intermediates are
  test-proven.
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
  behaviour, and the composition wiring test pins the shared factory, but a
  constructor audit remains a code-review responsibility. The predicate is
  sampled at operation entry, so a lock engaging mid-operation can let an
  already-started secret read finish; the window is small and the next operation
  fails closed.
- **Importer diagnostics.** SQLite driver errors wrapped with `%v` and history
  row ids may appear in import errors; these are not command text or
  credentials, but they are identifiers. No test proves every future SQLite
  diagnostic is value-free.
- **Server-visible metadata.** The Atuin server sees record id, host, tag,
  index, timestamp, and version. This is inherent to the protocol and is now
  stated in the account panel (F5); it is not a defect in this client.
- **The details control is only as safe as its typed model.** Adding an
  error-string, URL, username, or `Status.Detail` field to `AtuinDiagnostics`
  would create a leak path; the current renderer has no such field, and a test
  plants a secret in the discarded values.

## Test evidence

Hardening tests added or strengthened in this change:

- `app/atuin_wiring_test.go`:
  `TestCompositionWiresTheProductionAtuinFactory`,
  `TestComposedAtuinServicesReachALoopbackServer`
- `adapters/atuin/remote/factory_test.go`:
  `TestProductionFactoryDefaultsAreSuitableForProduction`,
  `TestUserAgentForVersionNamesTheProductAndVersion`
- `adapters/atuin/remote/login_wipe_test.go`:
  `TestAuthenticateWipesTheLoginRequestBody`
- `adapters/atuin/remote/login_response_wipe_test.go`:
  `TestAuthenticateWipesTheLoginResponseBody`,
  `TestAuthenticateWipesTheLoginResponseOnDecodeFailure`,
  `TestAuthenticateWipesTheLoginResponseOnReadFailure`
- `domain/history/masterkey_test.go`:
  `TestDecodeMasterKeyBytesWipesTheIntermediateBuffer`,
  `TestDecodeMasterKeyBytesWipesOnFailure`
- `application/historysync/account_test.go`:
  `TestPublicSecretErrorReducesUnknownErrorsToASentinel`,
  `TestAccountOperationsFailClosedWhileAppLocked`; no-credential-read assertions
  in `TestLoginRequiresAnUnlockedSecretStore` and
  `TestCheckDoesNothingWhileDisabledLockedOrUnconfigured`
- `application/historysync/service_test.go`:
  `TestLockedStoresNeverDecryptCredentials` now also asserts zero `Has` calls for
  a passphrase-locked store and zero `Has`/`Mode` calls under App Lock
- `application/vault/export_test.go`:
  `TestPortableExportNeverCarriesCommandHistory`
- `application/vault/export/export_test.go`:
  `TestLogsExportConnectionLogsWithoutCommandHistory`
- `adapters/store/secrets_zeroing_test.go`:
  `TestSealSecretValueWipesTheTransientPlaintext`,
  `TestOpenSecretValueWipesTheDecryptedPlaintext`,
  `TestOpenSecretValueReportsAnAuthFailure`
- `application/vault/export_atuin_boundary_test.go`:
  `TestPortableExportNeverCarriesAtuinCredentials`
- `application/sync/merge_test.go`:
  `TestMergeDropsAtuinAccountCredentialsFromEverySide`
- `adapters/store/vault_backup_atuin_test.go`:
  `TestLocalBackupCarriesAtuinCiphertextButNotHistoryState`
- `adapters/atuinimport/atuinimport_test.go`:
  `TestMalformedRowErrorNeverContainsTheCommand`
- `ui/settings/atuin_test.go`:
  `TestAppLockMapsToTheLockedProblem`,
  `TestAtuinPanelStatesWhatTheServerCanSee`,
  `TestAtuinDetailsControlIsCollapsedAndShowsSanitizedFields`,
  `TestAtuinDetailsNeverRenderAnInternalError`,
  `TestAtuinDetailsStringsExistInEveryLocale`
- `ui/locales/*.json`: the account description in all five catalogs states the
  server-visible metadata limitation, and all five carry the details-control
  strings
- `adapters/syncvault/syncvault_test.go`: failure messages no longer print
  planted credential payloads; they report counts or omit the value

Existing tests relied on for boundaries:

- `adapters/syncvault/syncvault_test.go`:
  `TestSnapshotNeverCarriesAtuinAccountCredentials`,
  `TestApplyRejectsInboundAtuinAccountCredentialsBeforeWritingAnything`
- `adapters/atuin/remote/transport_test.go`:
  `TestCloseWipesCredentialsAndKeyAndIsIdempotent`,
  `TestStoreDeleteEndpointIsNeverRequested`, `TestRedirectIsNotFollowed`,
  `TestUnreachableServerIsReportedOfflineWithoutTheAddress`,
  `TestStatusErrorsMapWithoutLeaking`, `TestOpenedRemoteCopiesTheMasterKey`,
  `TestOpenRejectsIncompleteOrConflictingCredentials`
- `adapters/vaultbackup/store_test.go`: `TestSaveEncryptsAndRoundTrips`,
  `TestUnavailableKeychainRefusesToWrite`, `TestReadDetectsTamperedCiphertext`
- `application/history/service_test.go`:
  `TestRecordDoesNotCreateWireRecordsForRemoteOrLegacyRows`,
  `TestDeleteEntryOfAnImportedRowAppendsATombstone`,
  `TestImportHostAtuinFailsClosedOnAMalformedRowBeforeWriting`,
  `TestImportHostAtuinKeepsTheCheckpointWhenTheStoreFails`,
  `TestImportHostAtuinSkipsCheckpointedRowsAndNeverDuplicates`
- `domain/preferences/history_test.go`:
  `TestPortableStripsHistoryAccountSettings`
- `application/session/observe_test.go`:
  `TestObserverCannotWriteOrConsumeTheLiveView`,
  `TestObservationEndsWhenTheSessionEnds`,
  `TestObserverSnapshotMatchesTheOwnerAndReceivesFeed`

The quality gate for this change is the full Go suite
(`nix develop --command env GOFLAGS=-buildvcs=false go test ./... -p 1 -count=1`),
`go vet ./...`, `gofmt -l` on changed files, and `go tool mygo i18n check`. The
targeted and full-suite results are reported with this change set. They do not
claim the release verification dependencies above.
