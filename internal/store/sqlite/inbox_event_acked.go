// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxEventAcked reports whether the agent acknowledged the inbox event with this journal
// sequence, selectively or through the cumulative cursor. It never mutates, advances a
// cursor or touches a session. A wake claim that was reclaimed after a crash uses it as
// delivery evidence: an acknowledged event was read, so waking again would only duplicate
// it (MTIX-112.40.13, FR-20).
func (s *Store) InboxEventAcked(ctx context.Context, agent string, seq int64) (bool, error) {
	if agent == "" || seq <= 0 {
		return false, fmt.Errorf("inbox event acknowledgement needs an agent and a positive sequence: %w", model.ErrInvalidInput)
	}
	var acked bool
	// Covered by the agent's cumulative cursor, or recorded as a selective acknowledgement.
	err := s.readDB.QueryRowContext(ctx, `SELECT
 ? <= COALESCE((SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?), 0)
 OR EXISTS (SELECT 1 FROM agent_inbox_ack WHERE agent_id = ? AND event_seq = ?)`,
		seq, agent, agent, seq).Scan(&acked)
	if err != nil {
		return false, fmt.Errorf("read inbox event acknowledgement for %q: %w", agent, err)
	}
	return acked, nil
}
