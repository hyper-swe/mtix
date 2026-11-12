// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// deliverySchemaSQL defines the board-local delivery-status tables (MTIX-112.3).
// All three are additive (IF NOT EXISTS, no schema version change, so a 0.5.4
// binary still opens the store) and LOCAL ONLY: they are never exported, never
// journaled in sync_events and never replicated.
const deliverySchemaSQL = `
-- Last time each agent read its inbox (list, wait or ack). Written at most once
-- per inboxReadThrottle per agent, except that an ack always writes.
CREATE TABLE IF NOT EXISTS agent_inbox_reads (
    agent_id     TEXT PRIMARY KEY,
    last_read_at TEXT NOT NULL
);

-- One row per cumulative ack that advanced the cursor (kind watermark, covering
-- from_cursor < seq <= cursor) and one per ack folded into a watermark, covering
-- exactly seq = cursor: kind selective for an ack that carried a time, kind
-- watermark for an ack with no recorded time (carried or restored). An event reports its ack time
-- only from a mark that covers it; otherwise the time is unknown. Bounded per agent.
CREATE TABLE IF NOT EXISTS agent_inbox_cursor_marks (
    agent_id    TEXT    NOT NULL,
    cursor      INTEGER NOT NULL,
    from_cursor INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    advanced_at TEXT    NOT NULL,
    PRIMARY KEY (agent_id, cursor, from_cursor, kind)
);

-- One row per live inbox wait (token is unique per wait within a process, so
-- overlapping waits of one agent never share a row). Inserted when a wait starts, deleted when it
-- ends; a row left by a crashed process is ignored at read time because its
-- pid and start time no longer match a running process.
CREATE TABLE IF NOT EXISTS agent_inbox_waiters (
    agent_id       TEXT    NOT NULL,
    pid            INTEGER NOT NULL,
    pid_start_time TEXT    NOT NULL,
    token          INTEGER NOT NULL,
    since          TEXT    NOT NULL,
    PRIMARY KEY (agent_id, pid, pid_start_time, token)
);
`
