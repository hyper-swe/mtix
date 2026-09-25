// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// syncQuarantineSQL creates the local quarantine of sync events: pulled
// events `mtix sync pull` could not take (MTIX-95.11) and own events
// `mtix sync push` holds back (MTIX-95.12); schemaSQL runs it on every start.
//
// Pull checks every pulled event (the FR-18.7 envelope caps, the
// sync.max_lamport_jump bound) and applies it in its own savepoint; an event
// that fails either is rolled back and stored here instead, as the raw event
// JSON it was pulled as, and the pull goes on. Pull retries those rows
// (source pull or sweep) at its start (and at its end when it applied
// events), in Lamport order, and deletes a row once its event applies or the
// store already holds it. Pulled events in the quarantine are never written
// to sync_events. source says which pass first quarantined the event: the
// cursor pass (pull) or the late-event sweep (sweep).
//
// Push validates each pending event before it sends a batch; an event the
// hub would refuse (a payload over the wire cap, say), or one in the
// subtree of a held task creation, is held here with source push, as the
// event JSON, and the rest of the batch is pushed. A held push event stays
// in sync_events as pending and is left out of the pending reads. Pull
// never retries or deletes a push row. Push settles its rows at the start of
// every push (sync_push_hold.go): it deletes the row of a hold that ended (a
// clock hold that passes, and the subtree of a creation released that way)
// and rewrites the reason of a kept dependent whose nearest held creation
// changed.
//
// A repeat failure of a pulled event updates only attempts and
// last_attempt; its reason and cli_version stay as first recorded.
// `mtix sync reconcile --discard-local` empties the table; `mtix sync
// clone` removes the pulled rows. Local only,
// never synced. Created with IF NOT EXISTS; no schema version change. A
// table created before push holds existed is widened to accept source push
// on start (widenQuarantineSource).
const syncQuarantineSQL = `
CREATE TABLE IF NOT EXISTS sync_quarantine (
    event_id     TEXT PRIMARY KEY,
    source       TEXT NOT NULL CHECK (source IN ('pull', 'sweep', 'push')),
    raw_event    TEXT NOT NULL,
    reason       TEXT NOT NULL,
    first_seen   TEXT NOT NULL,
    last_attempt TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 1,
    cli_version  TEXT NOT NULL DEFAULT ''
);
`
