-- SSH Keys and Identities, version 4.
--
-- Hybrid storage matches the hosts table: indexed identity and ordering
-- columns plus the complete aggregate as JSON. Private key material,
-- passphrases, and Identity passwords are encrypted in secret_values and are
-- never columns here. There are no foreign keys: deleting a Key leaves Host
-- and Identity references dangling, as in the original Vault.

CREATE TABLE ssh_keys (
  id          TEXT PRIMARY KEY,
  label       TEXT NOT NULL,
  type        TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL DEFAULT 'imported',
  order_key   INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  data        TEXT NOT NULL
);

CREATE INDEX ssh_keys_order ON ssh_keys(order_key, label COLLATE NOCASE);

CREATE TABLE identities (
  id          TEXT PRIMARY KEY,
  label       TEXT NOT NULL,
  username    TEXT NOT NULL DEFAULT '',
  auth_method TEXT NOT NULL DEFAULT 'password',
  key_id      TEXT NOT NULL DEFAULT '',
  order_key   INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  data        TEXT NOT NULL
);

CREATE INDEX identities_order ON identities(order_key, label COLLATE NOCASE);
