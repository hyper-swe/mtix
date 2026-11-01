// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxAckCoverage returns the acknowledgement's own coverage change atomically.
// An overlapping idempotent writer cannot borrow another writer's new effect.
func (s *Store) InboxAckCoverage(ctx context.Context, agent string, seq int64, through bool) (before, after int64, err error) {
	if agent == "" {
		return 0, 0, fmt.Errorf("inbox ack: agent id required: %w", model.ErrInvalidInput)
	}
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		if targetErr := validateInboxAckTarget(ctx, tx, seq, through); targetErr != nil {
			return fmt.Errorf("validate inbox acknowledgement target: %w", targetErr)
		}
		var readErr error
		before, readErr = readWakeAckMark(ctx, tx, agent)
		if readErr != nil {
			return fmt.Errorf("read inbox acknowledgement coverage: %w", readErr)
		}
		if writeErr := s.writeInboxAcknowledgement(ctx, tx, agent, seq, through); writeErr != nil {
			return fmt.Errorf("write inbox acknowledgement coverage: %w", writeErr)
		}
		after, readErr = readWakeAckMark(ctx, tx, agent)
		if readErr != nil {
			return fmt.Errorf("read resulting inbox acknowledgement coverage: %w", readErr)
		}
		return nil
	})
	if err == nil {
		s.touchFromContext(ctx, agent)
	}
	if err != nil {
		return before, after, fmt.Errorf("commit inbox acknowledgement coverage: %w", err)
	}
	return before, after, nil
}

func validateInboxAckTarget(ctx context.Context, tx *sql.Tx, seq int64, through bool) error {
	tail, err := journalTail(ctx, tx)
	if err != nil {
		return fmt.Errorf("read journal bounds for inbox acknowledgement: %w", err)
	}
	if seq >= 1 && seq <= tail {
		return nil
	}
	selector := "seq"
	if through {
		selector = "--through"
	}
	return fmt.Errorf("inbox ack: %s %d is outside the journal (tail is %d); acking beyond the tail would silently pre-acknowledge future events: %w",
		selector, seq, tail, ErrSeqOutsideJournal)
}

func (s *Store) writeInboxAcknowledgement(ctx context.Context, tx *sql.Tx, agent string, seq int64, through bool) error {
	if !through {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES(?,?,?)`,
			agent, seq, s.clock().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("record selective inbox acknowledgement: %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES(?,?)
		ON CONFLICT(agent_id) DO UPDATE SET cursor=MAX(cursor,excluded.cursor)`, agent, seq); err != nil {
		return fmt.Errorf("advance cumulative inbox acknowledgement: %w", err)
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM agent_inbox_ack WHERE agent_id=?
		AND event_seq<=(SELECT cursor FROM agent_inbox_cursor WHERE agent_id=?)`, agent, agent)
	if err != nil {
		return fmt.Errorf("prune selectively acknowledged prefix: %w", err)
	}
	return nil
}
