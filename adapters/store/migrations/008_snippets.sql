-- Snippets and Snippet packages, version 8.
--
-- A Snippet package is identified by its slash-separated path, so the path is
-- the primary key; renaming writes the new paths and removes the old ones.
-- Snippets reference a package by path with no foreign key, matching the
-- Vault's non-cascading references: deleting a package clears its references
-- and never deletes a Snippet. Targets and remembered variable values live in
-- the aggregate's JSON tail, like every other Vault record.

CREATE TABLE snippets (
  id           TEXT PRIMARY KEY,
  label        TEXT NOT NULL,
  package_path TEXT NOT NULL DEFAULT '',
  order_key    INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  data         TEXT NOT NULL
);

CREATE INDEX snippets_order ON snippets(order_key, label COLLATE NOCASE);
CREATE INDEX snippets_package ON snippets(package_path, order_key);

CREATE TABLE snippet_packages (
  path       TEXT PRIMARY KEY,
  order_key  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  data       TEXT NOT NULL
);

CREATE INDEX snippet_packages_order ON snippet_packages(order_key, path COLLATE NOCASE);
