// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// A guarded expression keeps legacy malformed JSON insertable. The partial
// addressee index bounds comment reads without a schema-version change.
const inboxCountIndexSQL = `CREATE INDEX IF NOT EXISTS idx_sync_events_comment_addressee
 ON sync_events(CASE WHEN json_valid(payload) THEN json_extract(payload, '$.to') END)
 WHERE op_type = 'comment'`

// UNION counts each journal sequence once, including hook-only deliveries.
// The join excludes orphan deliveries. Both aggregates share one snapshot;
// selective acknowledgements and the cumulative cursor affect pending only.
const inboxCountSQL = `WITH addressed(seq) AS (
 SELECT e.rowid FROM sync_events e
 WHERE e.op_type = 'comment'
 AND (CASE WHEN json_valid(e.payload) THEN json_extract(e.payload, '$.to') END) = ?
 UNION
 SELECT e.rowid FROM inbox_deliveries d
 JOIN sync_events e ON e.rowid = d.event_seq
 WHERE d.agent_id = ?
)
SELECT COUNT(CASE WHEN seq > COALESCE(
 (SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?), 0)
 AND NOT EXISTS (SELECT 1 FROM agent_inbox_ack a
 WHERE a.agent_id = ? AND a.event_seq = addressed.seq)
 THEN 1 END), COUNT(*) FROM addressed`

// InboxCount returns pending and all retained addressed events per FR-19.1.
// It never acknowledges, advances cursors or refreshes session last_seen,
// including when the context carries a session touch identity.
func (s *Store) InboxCount(ctx context.Context, agent string) (int64, int64, error) {
	if agent == "" {
		return 0, 0, fmt.Errorf("inbox count: agent ID is required: %w", model.ErrInvalidInput)
	}
	var pending, total int64
	if err := s.readDB.QueryRowContext(ctx, inboxCountSQL, agent, agent, agent, agent).Scan(&pending, &total); err != nil {
		return 0, 0, fmt.Errorf("count inbox for %q: %w", agent, err)
	}
	return pending, total, nil
}

func (s *Store) ensureInboxCountIndex(ctx context.Context) error {
	return s.withSchemaTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, inboxCountIndexSQL); err != nil {
			return fmt.Errorf("create inbox addressee index: %w", err)
		}
		return nil
	})
}
