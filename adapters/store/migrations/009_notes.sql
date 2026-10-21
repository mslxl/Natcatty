-- Vault Notes and Note Groups, version 9.
--
-- A Note Group is identified by its slash-separated path, so the path is the
-- primary key; renaming writes the new path and removes the old one. A Note
-- references a group by path with no foreign key, matching the Vault's
-- non-cascading references: deleting a group never deletes its Notes; the
-- application moves them to the Vault root. The Markdown source and timestamps
-- beyond the indexed columns live in the aggregate's JSON tail, like every
-- other Vault record.

CREATE TABLE vault_notes (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL,
  group_path TEXT NOT NULL DEFAULT '',
  order_key  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  data       TEXT NOT NULL
);

CREATE INDEX vault_notes_order ON vault_notes(order_key, title COLLATE NOCASE);
CREATE INDEX vault_notes_group ON vault_notes(group_path, order_key);

CREATE TABLE note_groups (
  path       TEXT PRIMARY KEY,
  order_key  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  data       TEXT NOT NULL
);

CREATE INDEX note_groups_order ON note_groups(order_key, path COLLATE NOCASE);
