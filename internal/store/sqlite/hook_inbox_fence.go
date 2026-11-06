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

// RecordInboxDeliveryEvent records a hook delivery only while the observed
// event's journal identity still matches in the writer transaction. A replaced
// event returns matched=false without attaching its delivery to reused mail
// (FR-19.4, MTIX-112.2.2).
func (s *Store) RecordInboxDeliveryEvent(ctx context.Context, agentID string, event JournalEvent, hookName string) (bool, error) {
	if agentID == "" {
		return false, fmt.Errorf("inbox delivery: agent id required: %w", model.ErrInvalidInput)
	}
	var matched bool
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		matched, err = hookJournalIdentityMatches(ctx, tx, event)
		if err != nil || !matched {
			return err
		}
		// Guarded insertion preserves per-agent/event idempotency for surviving mail.
		_, err = tx.ExecContext(ctx, `INSERT INTO inbox_deliveries (agent_id,event_seq,hook_name,delivered_at) VALUES (?,?,?,?) ON CONFLICT(agent_id,event_seq) DO NOTHING`, agentID, event.Seq, hookName, s.clock().UTC().Format(time.RFC3339))
		return err
	})
	if err != nil {
		return false, fmt.Errorf("record inbox delivery at %d: %w", event.Seq, err)
	}
	return matched, nil
}
