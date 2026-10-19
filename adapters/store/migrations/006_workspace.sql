-- Workspace restore layout. One row holds the main window's Tabs, split
-- trees, Pane descriptors, and last working directories. Scrollback output is
-- deliberately not stored.
CREATE TABLE IF NOT EXISTS workspace_layout (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    version    INTEGER NOT NULL,
    data       TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
