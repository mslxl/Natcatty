# Vault Host Groups are keyed by ID; a path is derived

A Host Group is a tree node with a stable `id` and a `(parentId, name)`
position. Its display path is derived from the tree on every read: no column,
aggregate, or Host record stores one. A Host's only stored membership is
`groupId`.

The previous model kept two identities for one relationship. Storage keyed
`host_groups` by path and each Host carried the group's path, while convergent
sync keyed groups by ID and derived paths from the tree. A rename had to
rewrite every member Host and every descendant group, and any operation that
touched only one of the two forms left a record that disagreed with itself:
`Create` never wrote `groupId`, an editor move deliberately cleared it, and a
path that became an explicit group could end up with two identities.

**Status**: accepted

## What changed

- `host_groups` is keyed by `id`; `path` is gone from the physical schema.
  `(parent_id, name)` is the stored structure, and `ListGroups` materializes
  paths with the same `ResolveGroupTree` the sync payload uses.
- `hosts.group_id` replaces `hosts.group_path`. The aggregate JSON omits the
  derived `group` field, so storage cannot disagree with the tree.
- The repository and service address groups by ID: `GetGroup(id)`,
  `UpdateGroup(id, draft)`, `DeleteGroup(id)`, `AssignHostGroup(hostID, groupID)`.
  A rename or a move is one row update and never touches members.
- Paths remain at the edges: a Host draft, an import record, the group editor's
  text field, and the v1 payload still name a path. The service resolves it to
  a stable ID, creating the group and any missing ancestor the first time the
  path is used, so every membership has a node.
- Group defaults resolve along the `ParentID` chain, root first. A path with no
  node has no identity, so a Host cannot belong to a level that does not exist.
- Snippets still target group paths. They are a separate reference and a rename
  remaps them; nothing about them stores group identity.
- Database schema version 2. `Open` upgrades a path-keyed database before
  AutoMigrate (the primary key changes, which GORM cannot do in place) and
  turns hosts' `group_path` into `group_id`, synthesizing a record for a path
  no group row named. Local backups written under version 1 are upgraded on
  restore.
- The sync payload still carries a derived `path`/`group` for older readers;
  convergent sync was already ID-keyed (ADR 0014-v2).

## Consequences

- Renaming or moving a group writes one row. Member Hosts keep their ID and
  follow the move; descendant groups derive their new paths.
- Two groups cannot share an identity for one path: creating a group at a path
  that already has a node is a conflict, because the node exists.
- A Host's path survives only as a legacy shape: a record written by an older
  build keeps its stored path until the migration rewrites it, and reads fall
  back to that path when resolving defaults.
- Deleting a group is explicit work: the service removes the subtree by ID and
  clears members' `groupId`; storage never cascades.
- A remote device that has not upgraded still understands the payload, because
  the wire format keeps paths.
