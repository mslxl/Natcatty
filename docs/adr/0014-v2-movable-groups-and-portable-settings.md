# v2 Host Groups are a movable tree; portable settings ride the flat settings object

The v2 CRDT payload keeps the v1 family names and array shapes, so an older
reader can still materialize a complete snapshot. Two families needed a
semantic change rather than a new wire field:

- **Host Groups** are keyed by a stable `id` instead of `path`. A record carries
  `parentId` and `name`; a rename or move is a register write on those fields,
  not a delete plus an insert. `path` is derived from the tree when a v1-shaped
  payload is materialized and is never written to the CRDT. Hosts carry
  `groupId` and Snippets carry `targetGroupIds`; the path forms (`hosts.group`,
  `snippets.targetGroups`) are likewise derived at materialization. A state
  written before stable IDs is migrated deterministically: the migration node is
  derived from the old state hash, every old record gets the deterministic
  path-derived ID, and the old path-keyed entities remain in the state as
  tombstones so their dots continue to witness the global version vector.
  (The records are re-emitted under the new IDs; the old entities are not
  rewritten in place.) A legacy Host or Snippet reference is migrated in the
  same pass: the stable `groupId` or `targetGroupIds` is written from the path
  and the derived register is tombstoned, keeping the membership without
  leaving a field that a later group move would contradict.
- **Settings** remain the original's flat `settings` object, modeled as an
  object-shaped family keyed by document name. This build publishes only
  portable documents under namespaced keys (`natcatty.preferences`,
  `natcatty.terminal`, `natcatty.keybindings`, `natcatty.themes`,
  `natcatty.sftpBookmarks`, `natcatty.browserBookmarks`). Device-local fields
  (app icon, local opener paths and file associations, log directory,
  last-used encodings, the sync provider profile) are stripped on snapshot and
  preserved from the local document on apply. A non-object document such as the
  theme or bookmark array is wrapped under a private marker field because a
  CRDT entity must be an object.

**Status**: accepted

**Consequences**:
- The envelope stays at `syncSchemaVersion: 2`; no new top-level payload field
  and no KDF/envelope change. Older readers see the derived `path`,
  `group`, and `targetGroups` values they already understand.
- A legacy path-keyed group family becomes a tombstoned old generation plus one
  stable-ID generation. The tombstones are required for causal validity and are
  invisible to materialization.
- Derived fields must never be added to a CRDT register. Verification compares
  the materialized payload with the inline envelope after normalization, so a
  decoration hook may only fill fields the normalized family drops.
- Preferences that name a local path or a platform-installed application are
  never uploaded. Only the portable subset follows the user between devices.
- Bootstrap decisions compare Vault families only, because every device has
  its own current settings documents. The chosen portable settings are then
  applied to the initial state explicitly, so a v1 provider with no settings
  does not make the first v2 envelope drop the local documents.
- The v1 sync path carries the same portable settings object and applies it
  after the record families; an empty Vault still refuses to publish through
  the v1 empty-vault guard.
