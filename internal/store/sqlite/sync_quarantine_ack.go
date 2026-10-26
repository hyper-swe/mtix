// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// QuarantineAcknowledgement is the first receipt for one exact retained event.
type QuarantineAcknowledgement struct {
	EventID        string `json:"event_id"`
	RawEventSHA256 string `json:"raw_event_sha256"`
	Source         string `json:"source"`
	Actor          string `json:"actor"`
	Reason         string `json:"reason"`
	AcknowledgedAt string `json:"acknowledged_at"`
}

// QuarantineAcknowledgementResult reports whether this call reused the immutable
// first receipt. AlreadyAcknowledged is response metadata, not stored audit data
// (MTIX-187.2).
type QuarantineAcknowledgementResult struct {
	QuarantineAcknowledgement
	AlreadyAcknowledged bool `json:"already_acknowledged"`
}

// QuarantineForAcknowledgement reads the snapshot the service must validate.
func (s *Store) QuarantineForAcknowledgement(ctx context.Context, eventID string) (QuarantinedEvent, error) {
	q := QuarantinedEvent{EventID: eventID}
	err := s.QueryRow(ctx, `SELECT source, raw_event FROM sync_quarantine WHERE event_id = ?`, eventID).Scan(&q.Source, &q.RawEvent)
	if errors.Is(err, sql.ErrNoRows) {
		return q, fmt.Errorf("quarantined event: %w", model.ErrNotFound)
	}
	if err != nil {
		return q, fmt.Errorf("read quarantined event: %w", err)
	}
	return q, nil
}

// RecordQuarantineAcknowledgement atomically checks the validated snapshot and
// returns its first receipt with an atomic per-call repeat flag (MTIX-187.2).
// Local bookkeeping never schedules a task export.
func (s *Store) RecordQuarantineAcknowledgement(ctx context.Context, expected QuarantinedEvent, receipt QuarantineAcknowledgement) (QuarantineAcknowledgementResult, error) {
	var result QuarantineAcknowledgementResult
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var source, raw string
		err := tx.QueryRowContext(ctx, `SELECT source, raw_event FROM sync_quarantine WHERE event_id = ?`, expected.EventID).Scan(&source, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("recheck quarantined event: %w", model.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("recheck quarantined event: %w", err)
		}
		if source != expected.Source || raw != expected.RawEvent {
			return fmt.Errorf("quarantined event changed; inspect it again: %w", model.ErrInvalidInput)
		}
		inserted, err := tx.ExecContext(ctx, `INSERT INTO sync_quarantine_acknowledgements
   (event_id, raw_event, raw_event_sha256, source, actor, reason, acknowledged_at)
   VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(event_id, raw_event) DO NOTHING`,
			expected.EventID, raw, receipt.RawEventSHA256, source, receipt.Actor, receipt.Reason, receipt.AcknowledgedAt)
		if err != nil {
			return fmt.Errorf("record quarantine acknowledgement: %w", err)
		}
		rows, err := inserted.RowsAffected()
		if err != nil {
			return fmt.Errorf("count recorded quarantine acknowledgements: %w", err)
		}
		result.QuarantineAcknowledgement, err = scanQuarantineAcknowledgement(tx.QueryRowContext(ctx, quarantineReceiptQuery, expected.EventID, raw))
		if err != nil {
			return fmt.Errorf("load first quarantine acknowledgement: %w", err)
		}
		result.AlreadyAcknowledged = rows == 0
		return nil
	}, false)
	if err != nil {
		return QuarantineAcknowledgementResult{}, fmt.Errorf("acknowledge quarantined event: %w", err)
	}
	return result, nil
}

const quarantineReceiptQuery = `SELECT event_id, raw_event_sha256, source, actor, reason, acknowledged_at
	FROM sync_quarantine_acknowledgements WHERE event_id = ? AND raw_event = ?`

func scanQuarantineAcknowledgement(row *sql.Row) (QuarantineAcknowledgement, error) {
	var a QuarantineAcknowledgement
	err := row.Scan(&a.EventID, &a.RawEventSHA256, &a.Source, &a.Actor, &a.Reason, &a.AcknowledgedAt)
	if err != nil {
		return a, fmt.Errorf("read quarantine acknowledgement: %w", err)
	}
	return a, nil
}

// QuarantineAcknowledgementFor matches exact bytes of the row being displayed.
func (s *Store) QuarantineAcknowledgementFor(ctx context.Context, q QuarantinedEvent) (*QuarantineAcknowledgement, error) {
	available, err := s.quarantineAcknowledgementsAvailable(ctx)
	if err != nil || !available {
		return nil, err
	}
	if q.Source != "pull" && q.Source != "sweep" {
		return nil, nil
	}
	a, err := scanQuarantineAcknowledgement(s.QueryRow(ctx, quarantineReceiptQuery, q.EventID, q.RawEvent))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Legacy stores opened read-only cannot install additive metadata. Only that
// confirmed case may omit receipts; writable-store audit failures are errors.
func (s *Store) quarantineAcknowledgementsAvailable(ctx context.Context) (bool, error) {
	if !s.writeRefused {
		return true, nil
	}
	var n int
	err := s.QueryRow(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'sync_quarantine_acknowledgements'`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inspect quarantine audit schema: %w", err)
	}
	return n == 1, nil
}
