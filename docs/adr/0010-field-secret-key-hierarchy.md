# Field-level secret key hierarchy and ciphertext format

Credentials are encrypted per field with AES-256-GCM using a random 32-byte `K_field`
stored in `mygo.Secrets`; the AAD binds entity, field, and key version, and ciphertext is
kept in a structured `secret_values` table with `enc:v1:` portability semantics. We never
persist plaintext: when the OS secret store is unavailable the app offers passphrase
(Argon2id-wrapped `K_field`) or session-only mode. This is hard to change because every
credential, export, and backup would need re-encryption.

**Status**: accepted

**Considered Options**:
- Keep the original Electron `safeStorage`-style opaque blob per field: simpler, but no
  key versioning or structured re-encryption.
- Encrypt the whole vault: simpler key handling, but breaks per-field sync/merge and
  makes partial imports impossible.

**Consequences**:
- Unreadable ciphertext is preserved and blocks cloud upload rather than being dropped.
- Cloud sync keeps its own PBKDF2-SHA-256 600k scheme for the payload envelope.
- Notes, snippet variable values, shell history, and logs remain plaintext at rest,
  matching the original credential-only boundary.
