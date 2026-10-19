-- Known Hosts, version 3.
--
-- One row per trusted endpoint and key type. The indexed columns are copies
-- of the JSON tail. The unique selector mirrors the original's upsert rule:
-- the same case-insensitive hostname, port, and key type is one trust record,
-- updated in place so its identity and discovery time survive a rotation.

CREATE TABLE known_hosts (
  id                  TEXT PRIMARY KEY,
  hostname            TEXT NOT NULL COLLATE NOCASE,
  port                INTEGER NOT NULL DEFAULT 22,
  key_type            TEXT NOT NULL,
  fingerprint         TEXT NOT NULL DEFAULT '',
  public_key          TEXT NOT NULL DEFAULT '',
  discovered_at       INTEGER NOT NULL,
  last_seen           INTEGER NOT NULL,
  converted_to_host_id TEXT NOT NULL DEFAULT '',
  order_key           INTEGER NOT NULL DEFAULT 0,
  data                TEXT NOT NULL
);

CREATE UNIQUE INDEX known_hosts_selector
  ON known_hosts(hostname COLLATE NOCASE, port, key_type);

CREATE INDEX known_hosts_endpoint
  ON known_hosts(hostname COLLATE NOCASE, port);
