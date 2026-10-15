// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// attachSchemaSQL defines local coordination tables without changing the schema version.
const attachSchemaSQL = `
CREATE TABLE IF NOT EXISTS agent_registrations (
    agent_id TEXT NOT NULL,
    harness TEXT NOT NULL,
    session_handle TEXT,
    uid TEXT NOT NULL,
    host_id TEXT NOT NULL,
    pid INTEGER,
    pid_start_time TEXT,
    permission_mode TEXT,
    mode_seen_at TEXT,
    origin TEXT NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0,
    high_water INTEGER NOT NULL DEFAULT 0,
    pending_hold INTEGER NOT NULL DEFAULT 0 CHECK(pending_hold IN (0,1)),
    registered_at TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    registrar_version TEXT NOT NULL,
    attach_protocol INTEGER NOT NULL,
    principal_id TEXT,
    returned_seq_mark INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,host_id,uid)
);
CREATE INDEX IF NOT EXISTS idx_agent_registrations_seen ON agent_registrations(host_id,uid,last_seen);
CREATE TABLE IF NOT EXISTS wake_ledger (
    agent_id TEXT NOT NULL,
    host_id TEXT NOT NULL,
    uid TEXT NOT NULL,
    event_seq INTEGER NOT NULL,
    epoch INTEGER NOT NULL,
    claimed_at TEXT NOT NULL,
    outcome TEXT NOT NULL,
    attempt_token TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(agent_id,host_id,uid,event_seq,epoch)
);
CREATE INDEX IF NOT EXISTS idx_wake_ledger_claims ON wake_ledger(outcome,claimed_at);
CREATE TABLE IF NOT EXISTS wake_outcome_rejections (
    agent_id TEXT NOT NULL,
    host_id TEXT NOT NULL,
    uid TEXT NOT NULL,
    event_seq INTEGER NOT NULL,
    epoch INTEGER NOT NULL,
    attempt_token TEXT NOT NULL,
    outcome TEXT NOT NULL,
    rejected_at TEXT NOT NULL,
    PRIMARY KEY(agent_id,host_id,uid,event_seq,epoch,attempt_token,outcome)
);
CREATE TABLE IF NOT EXISTS wake_scan_cursor (
    host_id TEXT NOT NULL,
    uid TEXT NOT NULL,
    cursor INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(host_id,uid)
);
CREATE TABLE IF NOT EXISTS dispatcher_heartbeat (
    host_id TEXT NOT NULL,
    uid TEXT NOT NULL,
    pid INTEGER NOT NULL,
    protocol INTEGER NOT NULL,
    version TEXT NOT NULL,
    seen_at TEXT NOT NULL,
    PRIMARY KEY(host_id,uid)
);
CREATE TABLE IF NOT EXISTS board_flags (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    set_at TEXT NOT NULL,
    source TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attach_pending (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    board_key TEXT NOT NULL,
    path TEXT NOT NULL,
    payload TEXT NOT NULL,
    queued_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_attach_pending_queue ON attach_pending(queued_at,id);
INSERT OR IGNORE INTO meta(key,value) VALUES('min_dispatch_protocol','0');
`
