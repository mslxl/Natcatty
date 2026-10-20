-- Host Groups and Proxy Profiles, version 7.
--
-- Host Groups are identified by their slash-separated path, so the path is
-- the primary key; renaming writes a new path and removes the old one. The
-- grouped defaults live in indexed columns and the complete aggregate is kept
-- as JSON, matching every other Vault record. Proxy Profiles are referenced by
-- Hosts and Host Groups with soft ids: there are no foreign keys, and deleting
-- a profile clears its references without deleting the referrer.

CREATE TABLE host_groups (
  path              TEXT PRIMARY KEY,
  notes             TEXT NOT NULL DEFAULT '',
  username          TEXT NOT NULL DEFAULT '',
  port              INTEGER NOT NULL DEFAULT 0,
  protocol          TEXT NOT NULL DEFAULT '',
  auth_method       TEXT NOT NULL DEFAULT '',
  identity_id       TEXT NOT NULL DEFAULT '',
  key_id            TEXT NOT NULL DEFAULT '',
  use_ssh_agent     INTEGER NOT NULL DEFAULT 0,
  requires_mfa      INTEGER NOT NULL DEFAULT 0,
  legacy_algorithms INTEGER NOT NULL DEFAULT 0,
  skip_ecdsa_host_key INTEGER NOT NULL DEFAULT 0,
  proxy_profile_id  TEXT NOT NULL DEFAULT '',
  order_key         INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  data              TEXT NOT NULL
);

CREATE INDEX host_groups_order ON host_groups(order_key, path COLLATE NOCASE);

CREATE TABLE proxy_profiles (
  id         TEXT PRIMARY KEY,
  label      TEXT NOT NULL,
  type       TEXT NOT NULL DEFAULT '',
  host       TEXT NOT NULL DEFAULT '',
  port       INTEGER NOT NULL DEFAULT 0,
  order_key  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  data       TEXT NOT NULL
);

CREATE INDEX proxy_profiles_order ON proxy_profiles(order_key, label COLLATE NOCASE);
