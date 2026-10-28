# Cloud-sync wire format is a compatibility contract

Cloud sync preserves the original `netcatty-vault.json` envelope exactly: `{meta,
payload}`, PBKDF2-SHA-256 with 600k iterations and a per-file 32-byte salt, AES-256-GCM
with a 12-byte IV and 128-bit tag, plaintext metadata, and an optional
`syncSchemaVersion: 2` CRDT payload. This is an interoperability contract with existing
installs and providers; changing the KDF, metadata authentication, or envelope shape
would break sync between app versions.

**Status**: accepted

**Consequences**:
- The original weaknesses are retained: metadata is outside GCM authentication and the
  anchor is not a MAC. This should be documented to users.
- Readers already honor the envelope's recorded `kdfIterations`. Production sealing
  writes the 600k work factor; the injectable lower value exists only for tests that
  exercise the real seal/open path repeatedly under the race detector and must never be
  set by a user-facing sync path.
- The v1 complete-snapshot payload and v2 CRDT envelope must coexist; migration is
  fail-closed.
- Any future authenticated-metadata design requires a new schema version and an explicit
  migration, not an in-place change.
