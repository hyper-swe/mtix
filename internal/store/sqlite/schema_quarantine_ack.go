// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// quarantineAcknowledgementDDL is additive local metadata, never exported or synced.
// Receipts outlive the retained quarantine row and cannot be rewritten or removed.
const quarantineAcknowledgementDDL = `
CREATE TABLE IF NOT EXISTS sync_quarantine_acknowledgements (
    event_id TEXT NOT NULL,
    raw_event TEXT NOT NULL,
    raw_event_sha256 TEXT NOT NULL CHECK(length(raw_event_sha256) = 64),
    source TEXT NOT NULL CHECK(source IN ('pull', 'sweep')),
    actor TEXT NOT NULL CHECK(length(actor) > 0),
    reason TEXT NOT NULL CHECK(length(trim(reason)) > 0),
    acknowledged_at TEXT NOT NULL CHECK(length(acknowledged_at) > 0),
    PRIMARY KEY(event_id, raw_event)
);
CREATE TRIGGER IF NOT EXISTS sync_quarantine_acknowledgements_no_update
BEFORE UPDATE ON sync_quarantine_acknowledgements
BEGIN SELECT RAISE(ABORT, 'quarantine acknowledgement is immutable'); END;
CREATE TRIGGER IF NOT EXISTS sync_quarantine_acknowledgements_no_delete
BEFORE DELETE ON sync_quarantine_acknowledgements
BEGIN SELECT RAISE(ABORT, 'quarantine acknowledgement is immutable'); END;
`
