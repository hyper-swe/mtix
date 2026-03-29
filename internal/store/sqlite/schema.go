// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// schemaVersion is the current database schema version.
// Increment when making backwards-incompatible schema changes per NFR-2.6.
const schemaVersion = 1

// schemaSQL defines the complete database schema per NFR-2.2.
// All tables, indexes, triggers, and initial metadata.
const schemaSQL = `
-- Core node storage (one row per task/micro task) per NFR-2.2
CREATE TABLE IF NOT EXISTS nodes (
    id              TEXT PRIMARY KEY,
    parent_id       TEXT,
    depth           INTEGER NOT NULL,
    seq             INTEGER NOT NULL,
    project         TEXT NOT NULL,

    -- Content
    title           TEXT NOT NULL,
    description     TEXT,
    prompt          TEXT,
    acceptance      TEXT,

    -- Classification
    node_type       TEXT DEFAULT 'auto',
    issue_type      TEXT,
    priority        INTEGER DEFAULT 3,
    labels          TEXT,

    -- State
    status          TEXT DEFAULT 'open',
    previous_status TEXT,
    progress        REAL DEFAULT 0.0,
    assignee        TEXT,
    creator         TEXT,
    agent_state     TEXT,

    -- Timestamps (ISO-8601 UTC)
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    closed_at       TEXT,
    defer_until     TEXT,

    -- Tracking
    estimate_min    INTEGER,
    actual_min      INTEGER,
    weight          REAL DEFAULT 1.0,
    content_hash    TEXT,

    -- Code references
    code_refs       TEXT,
    commit_refs     TEXT,

    -- Prompt steering
    annotations         TEXT DEFAULT '[]',
    invalidated_at      TEXT,
    invalidated_by      TEXT,
    invalidation_reason TEXT,

    -- Activity stream (JSON array per FR-3.6)
    activity        TEXT DEFAULT '[]',

    -- Soft delete
    deleted_at      TEXT,
    deleted_by      TEXT,

    -- Metadata
    metadata        TEXT,
    session_id      TEXT,

    FOREIGN KEY (parent_id) REFERENCES nodes(id) ON DELETE SET NULL
);

-- Indexes for query performance per NFR-2.2
CREATE INDEX IF NOT EXISTS idx_nodes_parent   ON nodes(parent_id);
CREATE INDEX IF NOT EXISTS idx_nodes_status   ON nodes(project, status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_nodes_priority ON nodes(project, priority) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_nodes_assignee ON nodes(project, assignee) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_nodes_deleted  ON nodes(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_nodes_deferred ON nodes(defer_until)
    WHERE status = 'deferred' AND defer_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_nodes_updated  ON nodes(updated_at);

-- Dependencies per FR-4.4 / NFR-2.2
CREATE TABLE IF NOT EXISTS dependencies (
    from_id     TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    to_id       TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    dep_type    TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    created_by  TEXT,
    metadata    TEXT,
    PRIMARY KEY (from_id, to_id, dep_type)
);

CREATE INDEX IF NOT EXISTS idx_deps_to ON dependencies(to_id, dep_type);

-- Sync event log per NFR-3.2
-- Events are append-only; pushed marks successful cloud sync.
CREATE TABLE IF NOT EXISTS sync_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id      TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    operation    TEXT NOT NULL,
    field        TEXT,
    old_value    TEXT,
    new_value    TEXT,
    timestamp    TEXT NOT NULL,
    author       TEXT,
    vector_clock TEXT,
    pushed       INTEGER DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_sync_events_unpushed
    ON sync_events(pushed) WHERE pushed = 0;
CREATE INDEX IF NOT EXISTS idx_sync_events_node
    ON sync_events(node_id);

-- Agent state per FR-10
CREATE TABLE IF NOT EXISTS agents (
    agent_id         TEXT PRIMARY KEY,
    project          TEXT NOT NULL,
    state            TEXT DEFAULT 'idle',
    state_changed_at TEXT,
    current_node_id  TEXT,
    last_heartbeat   TEXT
);

-- Session tracking per FR-10
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL REFERENCES agents(agent_id),
    project     TEXT NOT NULL,
    started_at  TEXT NOT NULL,
    ended_at    TEXT,
    status      TEXT DEFAULT 'active',
    summary     TEXT
);

-- Project metadata per NFR-2.2
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT
);

-- Sequence counters for dot-notation ID generation per FR-2.7
CREATE TABLE IF NOT EXISTS sequences (
    key   TEXT PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0
);

-- Full-text search per NFR-2.7
CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
    title, description, prompt,
    content='nodes', content_rowid='rowid'
);

-- FTS triggers to keep search index in sync per NFR-2.7.
-- Uses IS NOT for NULL-safe comparisons.
CREATE TRIGGER IF NOT EXISTS nodes_ai AFTER INSERT ON nodes BEGIN
    INSERT INTO nodes_fts(rowid, title, description, prompt)
    VALUES (new.rowid, new.title, new.description, new.prompt);
END;

CREATE TRIGGER IF NOT EXISTS nodes_ad AFTER DELETE ON nodes BEGIN
    INSERT INTO nodes_fts(nodes_fts, rowid, title, description, prompt)
    VALUES ('delete', old.rowid, old.title, old.description, old.prompt);
END;

CREATE TRIGGER IF NOT EXISTS nodes_au AFTER UPDATE ON nodes
WHEN new.title IS NOT old.title
  OR new.description IS NOT old.description
  OR new.prompt IS NOT old.prompt
BEGIN
    INSERT INTO nodes_fts(nodes_fts, rowid, title, description, prompt)
    VALUES ('delete', old.rowid, old.title, old.description, old.prompt);
    INSERT INTO nodes_fts(rowid, title, description, prompt)
    VALUES (new.rowid, new.title, new.description, new.prompt);
END;

-- Initial metadata
INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', '1');
`
