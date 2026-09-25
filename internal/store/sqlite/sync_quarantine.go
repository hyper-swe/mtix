// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// Data access for the pulled-event quarantine of `mtix sync pull`
// (MTIX-95.11): the local sync_quarantine table (schema.go), the savepoint
// that isolates each pulled event's apply inside a batch transaction, and
// the local Lamport read the jump bound compares with. The rules that decide
// what is quarantined live with the pull (cmd/mtix/sync_pull_quarantine.go
// and internal/sync/validator); this file only stores and reads.

// QuarantinedEvent is one row of sync_quarantine: a pulled event that failed
// its ingest checks or its apply, kept as the raw event JSON it was pulled as
// so a later pull can retry it.
type QuarantinedEvent struct {
	// EventID is the event's id, the table's key.
	EventID string
	// Source is the pull pass that first quarantined the event: "pull" (the
	// cursor pass) or "sweep" (the late-event sweep).
	Source string
	// RawEvent is the event as JSON (a model.SyncEvent).
	RawEvent string
	// Reason says why the event was first quarantined.
	Reason string
	// FirstSeen and LastAttempt are RFC 3339 UTC times: the first
	// quarantine and the latest failed attempt.
	FirstSeen   string
	LastAttempt string
	// Attempts counts every failed attempt, the first one included.
	Attempts int
	// CLIVersion is the version of the mtix that first quarantined it.
	CLIVersion string
	// Lamport, NodeID and OpType are read from the raw event: Lamport is
	// the retry order key (0 when the raw event has none), NodeID and
	// OpType are shown by `mtix sync quarantine list` (empty when absent).
	// QuarantinePage fills them; QuarantineEvent ignores them.
	Lamport        int64
	NodeID, OpType string
}

// QuarantineKey is a position in the retry order of sync_quarantine: the
// Lamport clock and event id of the last row a page returned.
type QuarantineKey struct {
	Lamport int64
	EventID string
}

// QuarantineEvent records a failed pulled event in the caller's transaction.
// The first time an event id is recorded it is inserted with one attempt;
// after that each call only counts one more attempt and moves last_attempt:
// the row is not rewritten, so source, the raw event, the reason, first_seen
// and cli_version stay as first recorded.
func QuarantineEvent(ctx context.Context, tx *sql.Tx, q QuarantinedEvent) error {
	// Insert the event, or count one more failed attempt of it (event_id is
	// the primary key).
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sync_quarantine
		  (event_id, source, raw_event, reason, first_seen, last_attempt, attempts, cli_version)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT(event_id) DO UPDATE SET
		  last_attempt = excluded.last_attempt,
		  attempts     = sync_quarantine.attempts + 1`,
		q.EventID, q.Source, q.RawEvent, q.Reason, q.FirstSeen, q.LastAttempt, q.CLIVersion,
	)
	if err != nil {
		return fmt.Errorf("quarantine %s: %w", q.EventID, err)
	}
	return nil
}

// RemoveQuarantined deletes eventID's row, in the caller's transaction; a
// pull calls it when the event has applied. An id that is not quarantined
// is a no-op.
func RemoveQuarantined(ctx context.Context, tx *sql.Tx, eventID string) error {
	// Drop the row of an event that has now applied.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_quarantine WHERE event_id = ?`, eventID); err != nil {
		return fmt.Errorf("unquarantine %s: %w", eventID, err)
	}
	return nil
}

// QuarantinePage returns up to limit quarantined events in retry order, the
// raw event's Lamport clock and then the event id, starting after the given
// key (from the first row when after is nil). Lamport order is causal, so a
// node's create is retried before its edits. A raw event that is not JSON
// or has no integer lamport_clock sorts as 0; its node and op are empty.
func (s *Store) QuarantinePage(ctx context.Context, after *QuarantineKey, limit int) ([]QuarantinedEvent, error) {
	started, afterLamport, afterID := 0, int64(0), ""
	if after != nil {
		started, afterLamport, afterID = 1, after.Lamport, after.EventID
	}
	// The next page of the quarantine in (Lamport clock, event id) order,
	// keyset-paged after the last row returned. The table is small (it holds
	// only events that failed), so the order key is computed per row.
	rows, err := s.Query(ctx, `
		SELECT event_id, source, raw_event, reason, first_seen, last_attempt,
		       attempts, cli_version, lamport, node_id, op_type
		FROM (
			SELECT q.*,
			       CASE WHEN json_valid(q.raw_event)
			            THEN COALESCE(CAST(json_extract(q.raw_event, '$.lamport_clock') AS INTEGER), 0)
			            ELSE 0 END AS lamport,
			       CASE WHEN json_valid(q.raw_event)
			            THEN COALESCE(CAST(json_extract(q.raw_event, '$.node_id') AS TEXT), '')
			            ELSE '' END AS node_id,
			       CASE WHEN json_valid(q.raw_event)
			            THEN COALESCE(CAST(json_extract(q.raw_event, '$.op_type') AS TEXT), '')
			            ELSE '' END AS op_type
			FROM sync_quarantine q
		)
		WHERE ? = 0 OR lamport > ? OR (lamport = ? AND event_id > ?)
		ORDER BY lamport, event_id
		LIMIT ?`, started, afterLamport, afterLamport, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("read quarantined events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var page []QuarantinedEvent
	for rows.Next() {
		var q QuarantinedEvent
		if err := rows.Scan(&q.EventID, &q.Source, &q.RawEvent, &q.Reason, &q.FirstSeen,
			&q.LastAttempt, &q.Attempts, &q.CLIVersion, &q.Lamport, &q.NodeID, &q.OpType); err != nil {
			return nil, fmt.Errorf("read quarantined events: %w", err)
		}
		page = append(page, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read quarantined events: %w", err)
	}
	return page, nil
}

// CountQuarantined returns how many pulled events are quarantined, for
// `mtix sync status` and `mtix sync doctor`.
func (s *Store) CountQuarantined(ctx context.Context) (int, error) {
	var n int
	// Every quarantined event, whatever its source.
	if err := s.QueryRow(ctx, `SELECT COUNT(*) FROM sync_quarantine`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quarantined events: %w", err)
	}
	return n, nil
}

// LocalLamport returns the local Lamport clock (meta.sync.lamport), read in
// the caller's transaction so it reflects the events that transaction has
// applied.
func LocalLamport(ctx context.Context, tx *sql.Tx) (int64, error) {
	var raw string
	// The replica's Lamport clock, advanced by every applied event.
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`).Scan(&raw); err != nil {
		return 0, fmt.Errorf("read meta.sync.lamport: %w", err)
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse meta.sync.lamport %q: %w", raw, err)
	}
	return v, nil
}

// WithSavepoint runs fn inside a savepoint of the caller's transaction, so a
// failing fn undoes only its own writes (MTIX-95.11: one pulled event's
// apply inside a batch). When fn fails, the savepoint is rolled back and
// fn's error is returned as rolledBack; the transaction stays usable. err
// reports a failure of the savepoint statements themselves, after which the
// caller must abandon the transaction. The savepoint name is a constant
// (SQL rule 1a: no identifier is built from input).
func WithSavepoint(ctx context.Context, tx *sql.Tx, fn func() error) (rolledBack, err error) {
	// Open the savepoint.
	if _, spErr := tx.ExecContext(ctx, `SAVEPOINT mtix_event`); spErr != nil {
		return nil, fmt.Errorf("savepoint: %w", spErr)
	}
	fnErr := fn()
	if fnErr != nil {
		// Undo fn's writes; the savepoint stays on the stack until released.
		if _, spErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT mtix_event`); spErr != nil {
			return nil, fmt.Errorf("rollback to savepoint: %w (after %w)", spErr, fnErr)
		}
	}
	// Close the savepoint, keeping whatever it still holds.
	if _, spErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT mtix_event`); spErr != nil {
		return nil, fmt.Errorf("release savepoint: %w", spErr)
	}
	return fnErr, nil
}

// IsQuarantined reports, in the caller's transaction, whether eventID has a
// quarantine row. The cursor pass leaves such an event to the quarantine
// retry instead of rewriting its row on every pull.
func IsQuarantined(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	var n int
	// One primary-key probe of the quarantine.
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_quarantine WHERE event_id = ?`, eventID).Scan(&n); err != nil {
		return false, fmt.Errorf("check quarantine for %s: %w", eventID, err)
	}
	return n > 0, nil
}

// EventApplied reports, in the caller's transaction, whether eventID is
// already in applied_events: this store applied or acknowledged it, and
// IdempotentApply dedupes it with no clock change. Pull skips the ingest
// checks for such an event, and the quarantine retry drops its row before
// any check. An own event that is only in sync_events does not count: the
// hub copy of an event this replica pushed can be changed on the hub, so it
// is checked like any other until a pull acknowledges it (MTIX-95.11).
func EventApplied(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	var n int
	// One primary-key probe of applied_events.
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, eventID).Scan(&n); err != nil {
		return false, fmt.Errorf("check applied_events for %s: %w", eventID, err)
	}
	return n > 0, nil
}

// ClearQuarantine empties sync_quarantine. `mtix sync clone` calls it with
// the rest of its reset: a clone rebuilds the store from the hub, so the
// quarantined events are applied again or quarantined again by later pulls.
func (s *Store) ClearQuarantine(ctx context.Context) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		// Drop every quarantined event.
		if _, err := tx.ExecContext(ctx, `DELETE FROM sync_quarantine`); err != nil {
			return fmt.Errorf("clear quarantine: %w", err)
		}
		return nil
	})
}

// LocalLamportClock returns the local Lamport clock (meta.sync.lamport)
// outside a transaction: the pull loop reads it after each batch to decide
// how far the saved cursor may move.
func (s *Store) LocalLamportClock(ctx context.Context) (int64, error) {
	var raw string
	// The replica's Lamport clock after the last committed apply.
	if err := s.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`).Scan(&raw); err != nil {
		return 0, fmt.Errorf("read meta.sync.lamport: %w", err)
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse meta.sync.lamport %q: %w", raw, err)
	}
	return v, nil
}
