-- Connection logs and shell/command history, version 10.
--
-- Both tables keep the indexed fields the lists sort and filter by plus the
-- aggregate's JSON tail, like every other Vault record. References to a Host
-- or Session may dangle: the Vault has no foreign keys, and a deleted Host
-- must not delete the audit trail. History is per execution, so autocomplete
-- aggregates rows instead of the history surface forking a second store.

CREATE TABLE connection_logs (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL DEFAULT '',
  host_id     TEXT NOT NULL DEFAULT '',
  host_label  TEXT NOT NULL DEFAULT '',
  hostname    TEXT NOT NULL DEFAULT '',
  username    TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL,
  error       TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER NOT NULL DEFAULT 0,
  data        TEXT NOT NULL
);

CREATE INDEX connection_logs_started ON connection_logs(started_at DESC, id DESC);
CREATE INDEX connection_logs_host ON connection_logs(host_id, started_at DESC);

CREATE TABLE history_entries (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL DEFAULT '',
  host_id     TEXT NOT NULL DEFAULT '',
  host_label  TEXT NOT NULL DEFAULT '',
  scope       TEXT NOT NULL DEFAULT '',
  command     TEXT NOT NULL,
  source      TEXT NOT NULL DEFAULT '',
  recorded_at INTEGER NOT NULL,
  data        TEXT NOT NULL
);

CREATE INDEX history_entries_recorded ON history_entries(recorded_at DESC, id DESC);
CREATE INDEX history_entries_host ON history_entries(host_id, recorded_at DESC);
CREATE INDEX history_entries_scope ON history_entries(scope, recorded_at DESC);
