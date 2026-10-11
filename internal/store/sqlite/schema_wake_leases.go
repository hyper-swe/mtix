// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Wake ownership is additive local state without a schema version change.
package sqlite

// wakeLeasesSQL is additive, local-only wake ownership (FR-20). Older readers
// ignore this table; schemaVersion stays unchanged and leases are never synced.
const wakeLeasesSQL = `
CREATE TABLE IF NOT EXISTS wake_leases (
    agent_id TEXT NOT NULL,
    host_id TEXT NOT NULL,
    uid TEXT NOT NULL,
    holder TEXT NOT NULL,
    high_water INTEGER NOT NULL,
    acquired_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    dirty INTEGER NOT NULL DEFAULT 0 CHECK (dirty IN (0, 1)),
    PRIMARY KEY (agent_id, host_id, uid)
);
`
