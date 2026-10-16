// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// taskHistorySQL creates the per-task change history (MTIX-95.31.19.2,
// ADR-006 A3). It is additive: CREATE ... IF NOT EXISTS on an existing store,
// no meta.schema_version bump, no foreign key to nodes (history outlives a
// removed task and a redaction can arrive before its entry).
//
// Entry shape (model.HistoryEntry): entry_id is an immutable ULID; task_uid is
// the task's durable uid (its id for a legacy task with none; the history is rekeyed
// when the task gets a uid or a new id); ts is
// the injected clock in fixed-width UTC with nanoseconds, so it sorts as text; actor and origin (local|import|hub|system)
// say who and where; kind is change|redaction|retention_drop; field names the
// changed field; old_value and new_value are JSON text for a structured field
// (SQL NULL = absent, the text 'null' = explicit null) and always SQL NULL for
// free text, which stores neither text nor hash; event_id optionally names the
// causal sync event. A unique index over (task_uid, event_id, field) where
// event_id is set makes the replay of one event a no-op.
const taskHistorySQL = `
CREATE TABLE IF NOT EXISTS task_history (
    entry_id   TEXT PRIMARY KEY,
    task_uid   TEXT NOT NULL,
    ts         TEXT NOT NULL,
    actor      TEXT NOT NULL,
    origin     TEXT NOT NULL,
    kind       TEXT NOT NULL,
    field      TEXT NOT NULL,
    old_value  TEXT,
    new_value  TEXT,
    event_id   TEXT
);
CREATE INDEX IF NOT EXISTS idx_task_history_uid_ts ON task_history(task_uid, ts);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_history_event ON task_history(task_uid, event_id, field) WHERE event_id IS NOT NULL;
-- history_retention_cutoff is local to this copy of the board (MTIX-95.31.19.4):
-- the newest ts this store trimmed each task's change entries up to. Written only
-- by this store's own trim, in the trim's transaction; never exported, imported
-- or synced. The import union skips an incoming change entry at or before it.
CREATE TABLE IF NOT EXISTS history_retention_cutoff (
    task_uid   TEXT PRIMARY KEY,
    cutoff_ts  TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`
