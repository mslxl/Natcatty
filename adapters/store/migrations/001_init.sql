-- Vault schema, version 1.
--
-- Hybrid storage: hosts keeps the identity and sort columns the Vault queries
-- plus the complete Host aggregate as JSON. Later tickets add their own tables
-- in new migrations; this base intentionally contains only what ticket 03
-- needs. There are no foreign keys: Vault references may dangle, matching the
-- original's non-cascading behavior.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE hosts (
  id          TEXT PRIMARY KEY,
  label       TEXT NOT NULL,
  hostname    TEXT NOT NULL,
  port        INTEGER NOT NULL DEFAULT 0,
  username    TEXT NOT NULL DEFAULT '',
  protocol    TEXT NOT NULL DEFAULT 'ssh',
  group_path  TEXT NOT NULL DEFAULT '',
  pinned      INTEGER NOT NULL DEFAULT 0,
  order_key   INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  data        TEXT NOT NULL
);

CREATE INDEX hosts_group_order ON hosts(group_path, order_key);

CREATE INDEX hosts_recent ON hosts(pinned DESC, updated_at DESC);
