// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// Wake accounting is local-only and additive for released 0.5.4 readers.
const wakeBudgetSQL = `
CREATE TABLE IF NOT EXISTS wake_budget_counters (
    agent_id TEXT NOT NULL,
    host_id TEXT NOT NULL,
    uid TEXT NOT NULL,
    total INTEGER NOT NULL CHECK(total >= 0),
    PRIMARY KEY(agent_id,host_id,uid)
);
`
