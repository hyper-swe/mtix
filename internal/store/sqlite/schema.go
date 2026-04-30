// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// schemaVersion is the current database schema version.
// Increment when making backwards-incompatible schema changes per NFR-2.6.
//
// Version history:
//   - v1 (v0.1.x): original schema; sync_events was a placeholder per NFR-3.2.
//   - v2 (v0.2.x): sync_events rewritten to FR-18.6 / SYNC-DESIGN section 3.1
//     shape; applied_events added; meta.sync.* sentinels populated.
const schemaVersion = 2

// schemaSQL defines the complete v2 database schema per NFR-2.2 and FR-18.
// All tables, indexes, triggers, and initial metadata.
//
// Every CREATE is IF NOT EXISTS so this SQL is safe to re-run on every
// startup. Migration from v1 (which has the OLD sync_events shape) drops
// the legacy table BEFORE running this SQL — see migrateV1ToV2SQL and
// the dispatch in store.init.
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

-- Sync event log per FR-18.6 / SYNC-DESIGN section 3.1.
-- Append-only mirror of events emitted by every node mutation.
-- The hub stores the canonical replica; this is the local outbox/cache.
-- retained_until is reserved for v2 compaction (FR-18.26 / MTIX v3 ticket).
CREATE TABLE IF NOT EXISTS sync_events (
    event_id            TEXT PRIMARY KEY,
    project_prefix      TEXT NOT NULL,
    node_id             TEXT NOT NULL,
    op_type             TEXT NOT NULL CHECK (op_type IN (
        'create_node','update_field','transition_status',
        'claim','unclaim','defer',
        'comment','link_dep','unlink_dep',
        'delete','set_acceptance','set_prompt'
    )),
    payload             TEXT NOT NULL,
    wall_clock_ts       INTEGER NOT NULL,
    lamport_clock       INTEGER NOT NULL,
    vector_clock        TEXT NOT NULL,
    author_id           TEXT NOT NULL,
    author_machine_hash TEXT NOT NULL,
    sync_status         TEXT NOT NULL DEFAULT 'pending' CHECK (sync_status IN (
        'pending','pushed','conflicted','applied'
    )),
    created_at          TEXT NOT NULL,
    retained_until      TEXT
);

CREATE INDEX IF NOT EXISTS idx_sync_events_status_lamport
    ON sync_events(sync_status, lamport_clock) WHERE sync_status = 'pending';
CREATE INDEX IF NOT EXISTS idx_sync_events_node    ON sync_events(node_id);
CREATE INDEX IF NOT EXISTS idx_sync_events_lamport ON sync_events(lamport_clock);

-- Idempotent dedupe per FR-18.9.
-- IdempotentApply (MTIX-15.4) checks this table before applying any pulled event.
CREATE TABLE IF NOT EXISTS applied_events (
    event_id           TEXT PRIMARY KEY,
    applied_at         TEXT NOT NULL,
    applied_by_lamport INTEGER NOT NULL
);

-- Local sync_projects mirror per FR-18.13 / MTIX-15.6.
-- Mirrors the hub-side sync_projects table from internal/store/postgres/migrations/003_sync_projects.sql.
-- One row per project the local CLI has cloned-from or pushed-to.
CREATE TABLE IF NOT EXISTS sync_projects (
    project_prefix         TEXT PRIMARY KEY,
    first_event_hash       TEXT NOT NULL,
    created_at             TEXT NOT NULL,
    schema_version         INTEGER NOT NULL DEFAULT 1,
    last_seen_cli_version  TEXT
);

-- Local conflict log per FR-18.12 / MTIX-15.5.
-- Mirrors the hub-side sync_conflicts table (see internal/store/postgres/migrations/002_sync_conflicts.sql).
-- Local rows are written by the apply engine when LWW resolution drops a value;
-- they let mtix sync conflicts list (MTIX-15.7) surface conflicts even when the hub is unreachable.
CREATE TABLE IF NOT EXISTS sync_conflicts (
    conflict_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id_winner TEXT NOT NULL,
    event_id_loser  TEXT NOT NULL,
    node_id         TEXT NOT NULL,
    field_name      TEXT,
    resolution      TEXT NOT NULL CHECK (resolution IN ('lww','tombstone','manual')),
    resolved_at     TEXT NOT NULL,
    resolved_by     TEXT
);
CREATE INDEX IF NOT EXISTS idx_sync_conflicts_node ON sync_conflicts(node_id);
CREATE INDEX IF NOT EXISTS idx_sync_conflicts_resolved_at ON sync_conflicts(resolved_at);

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

-- Initial metadata. INSERT OR IGNORE so existing values survive re-init.
-- For v1 -> v2 migration, schema_version is updated explicitly by the
-- migration step in store.init.
INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', '2');

-- Sync sentinels per FR-18 / SYNC-DESIGN sections 3.1 and 8.1.
-- Default values:
--   meta.sync.lamport         — local Lamport counter; bumped on every emit.
--   meta.sync.last_pulled_clock — high-water Lamport pulled from the hub.
--   meta.sync.machine_hash    — populated on first emit by sync/clock.MachineHash.
--   sync.max_queue_size       — 0 means unlimited (FR-18; enforced in MTIX-15.2.4).
--   hub.events_retention_days — 0 means forever; reserved for v2 compaction.
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.lamport', '0');
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.last_pulled_clock', '0');
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.machine_hash', '');
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.vector_clock', '{}');
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.first_event_hash', '');
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.project_prefix', '');
INSERT OR IGNORE INTO meta (key, value) VALUES ('meta.sync.clone.checkpoint', '0');
INSERT OR IGNORE INTO meta (key, value) VALUES ('sync.max_queue_size', '0');
INSERT OR IGNORE INTO meta (key, value) VALUES ('hub.events_retention_days', '0');
`

// migrateV1ToV2SQL drops the v0.1.x sync_events placeholder so the v2
// CREATE TABLE in schemaSQL can run without colliding on the table name.
//
// The legacy placeholder had no production callers (confirmed by grep
// across internal/* and cmd/* before MTIX-15.2.1 landed) so dropping it
// loses no real data. Pre-existing meta keys, nodes, dependencies, and
// every other v1 table survive untouched.
const migrateV1ToV2SQL = `
DROP INDEX IF EXISTS idx_sync_events_unpushed;
DROP INDEX IF EXISTS idx_sync_events_node;
DROP TABLE IF EXISTS sync_events;
`
