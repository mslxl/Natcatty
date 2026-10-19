-- Port Forwarding Rules. Runtime status is intentionally absent: it is a
-- projection rebuilt at launch and never survives a restart.
CREATE TABLE IF NOT EXISTS forwarding_rules (
    id           TEXT PRIMARY KEY,
    label        TEXT NOT NULL,
    type         TEXT NOT NULL,
    host_id      TEXT NOT NULL DEFAULT '',
    bind_address TEXT NOT NULL DEFAULT '',
    local_port   INTEGER NOT NULL DEFAULT 0,
    order_key    INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL DEFAULT 0,
    updated_at   INTEGER NOT NULL DEFAULT 0,
    data         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS forwarding_rules_order
    ON forwarding_rules (order_key ASC, label COLLATE NOCASE ASC, id ASC);
