// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// Local provenance, never transported. A restored record is readable mail,
// not proof of hub contact or a complete replication envelope. Its marker
// follows journal deletion, so an identity cannot leave stale bookkeeping.
const restoredInboxSQL = `
CREATE TABLE IF NOT EXISTS inbox_restored_events (
 event_id TEXT PRIMARY KEY REFERENCES sync_events(event_id) ON DELETE CASCADE
);
`
