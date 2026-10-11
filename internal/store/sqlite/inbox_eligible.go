// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Inbox reads and wake release share one eligibility predicate.
package sqlite

import (
	"context"
	"database/sql"
)

// inboxEligibleSQL is the single eligibility predicate for reads and atomic
// wake release (FR-19.4/FR-20): UNION dedupes deliveries and addressed comments;
// the cursor and selective acknowledgement ledger exclude handled events.
const inboxEligibleSQL = `
		SELECT seq, event_id, node_id, author_id, payload, created_at FROM (
			SELECT e.rowid AS seq, e.event_id, e.node_id, e.author_id, e.payload, e.created_at
			  FROM sync_events e
			 WHERE e.op_type = 'comment'
			   AND json_valid(e.payload)
			   AND json_extract(e.payload, '$.to') = ?
			   AND e.rowid > COALESCE((SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?), 0)
			   AND e.rowid NOT IN (SELECT event_seq FROM agent_inbox_ack WHERE agent_id = ?)
			UNION
			SELECT e.rowid AS seq, e.event_id, e.node_id, e.author_id, e.payload, e.created_at
			  FROM sync_events e
			  JOIN inbox_deliveries d ON d.event_seq = e.rowid
			 WHERE d.agent_id = ?
			   AND e.rowid > COALESCE((SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?), 0)
			   AND e.rowid NOT IN (SELECT event_seq FROM agent_inbox_ack WHERE agent_id = ?)
		)
		ORDER BY seq ASC`

type inboxQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryInboxEligible(ctx context.Context, q inboxQuerier, agent string) (*sql.Rows, error) {
	return q.QueryContext(ctx, inboxEligibleSQL, agent, agent, agent, agent, agent, agent)
}
