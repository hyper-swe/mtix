// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// relayQuarantineDDL is additive local metadata, never exported or synced
// (MTIX-268). Relay ingest skips a record from an authenticated peer that the
// validator or the apply refused (two peers took the same next task number, for
// example); the ingest cursor then moves past it, so this table is the only
// place the skipped record survives. It is not sync_quarantine: that table is
// keyed by event id, its pull path retries every row that is not a push hold,
// and a relay record may carry no decodable event at all.
//
// A row is keyed by where the record sat in the peer's stream (peer id,
// publisher epoch, relay sequence), so reading the same record again is one
// row with a higher attempts count. raw_payload is the exact record bytes.
// state is open until `mtix sync quarantine resolve` settles the row; a
// resolved row stays as the audit trail. Created with IF NOT EXISTS; no schema
// version change, like the other local-only tables.
const relayQuarantineDDL = `
CREATE TABLE IF NOT EXISTS relay_quarantine (
    peer_id        TEXT    NOT NULL,
    pub_epoch      INTEGER NOT NULL,
    rs             INTEGER NOT NULL,
    segment_no     INTEGER NOT NULL DEFAULT 0,
    event_id       TEXT    NOT NULL DEFAULT '',
    node_id        TEXT    NOT NULL DEFAULT '',
    op_type        TEXT    NOT NULL DEFAULT '',
    lamport        INTEGER NOT NULL DEFAULT 0,
    reason         TEXT    NOT NULL,
    raw_payload    BLOB    NOT NULL,
    first_seen     TEXT    NOT NULL,
    last_seen      TEXT    NOT NULL,
    attempts       INTEGER NOT NULL DEFAULT 1,
    state          TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved')),
    resolution     TEXT    NOT NULL DEFAULT '' CHECK (resolution IN ('', 'accepted', 'recreated')),
    resolved_by    TEXT    NOT NULL DEFAULT '',
    resolved_at    TEXT    NOT NULL DEFAULT '',
    resolve_reason TEXT    NOT NULL DEFAULT '',
    recreated_as   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (peer_id, pub_epoch, rs)
);
`
