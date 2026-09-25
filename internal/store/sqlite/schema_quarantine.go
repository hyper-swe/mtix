// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// syncQuarantineSQL creates the pulled-event quarantine of `mtix sync pull`
// (MTIX-95.11); schemaSQL runs it on every start. sync pull checks every
// pulled event (the FR-18.7 envelope caps, the sync.max_lamport_jump bound)
// and applies it in its own savepoint; an event that fails either is rolled
// back and stored here instead, as the raw event JSON it was pulled as, and
// the pull goes on. Pull retries every row at its start (and at its end when
// it applied events), in Lamport order, and deletes a row once its event
// applies or the store already holds it. Quarantined events are never
// written to sync_events. source says which pass first quarantined the
// event: the cursor pass (pull) or the late-event sweep (sweep). A repeat
// failure updates only attempts and last_attempt; reason and cli_version
// stay as first recorded. `mtix sync reconcile --discard-local` and
// `mtix sync clone` empty the table. Local only, never synced. Created with
// IF NOT EXISTS; no schema version change.
const syncQuarantineSQL = `
CREATE TABLE IF NOT EXISTS sync_quarantine (
    event_id     TEXT PRIMARY KEY,
    source       TEXT NOT NULL CHECK (source IN ('pull', 'sweep')),
    raw_event    TEXT NOT NULL,
    reason       TEXT NOT NULL,
    first_seen   TEXT NOT NULL,
    last_attempt TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 1,
    cli_version  TEXT NOT NULL DEFAULT ''
);
`
