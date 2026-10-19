-- Field-secret storage, version 2.
--
-- Ciphertext never appears in an entity's JSON tail. secret_values keeps one
-- AES-256-GCM row per encrypted field. crypto_keys keeps the versioned key
-- metadata; in passphrase mode it also keeps the Argon2id-wrapped field key.
-- The field key itself never appears in either table.

CREATE TABLE crypto_keys (
  version          INTEGER PRIMARY KEY,
  purpose          TEXT NOT NULL,
  created_at       INTEGER NOT NULL,
  retired_at       INTEGER,
  wrapped_key      BLOB,
  wrap_nonce       BLOB,
  wrap_kdf         TEXT,
  wrap_salt        BLOB,
  wrap_time        INTEGER,
  wrap_memory      INTEGER,
  wrap_parallelism INTEGER
);

CREATE TABLE secret_values (
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  field       TEXT NOT NULL,
  alg         TEXT NOT NULL,
  key_version INTEGER NOT NULL,
  nonce       BLOB NOT NULL,
  ciphertext  BLOB NOT NULL,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (entity_type, entity_id, field)
);

CREATE INDEX secret_values_key_version ON secret_values(key_version);
