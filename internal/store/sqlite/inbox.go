// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxEvent is one entry surfaced by a per-agent inbox query (FR-19.4).
// Seq is the sync_events rowid — the monotonic local sequence an agent acks.
type InboxEvent struct {
	Seq       int64     `json:"seq"`
	EventID   string    `json:"event_id"`
	NodeID    string    `json:"node_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// InboxList returns comment events addressed to agentID that are past the
// agent's ack cursor, oldest first. The inbox is DERIVED — a query over the
// durable event journal, not a separate mailbox — so it survives restarts and
// picks up synced events with no extra delivery step (FR-19.4).
func (s *Store) InboxList(ctx context.Context, agentID string) ([]InboxEvent, error) {
	if agentID == "" {
		return nil, fmt.Errorf("inbox list: agent id required: %w", model.ErrInvalidInput)
	}
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT e.rowid, e.event_id, e.node_id, e.author_id, e.payload, e.created_at
		  FROM sync_events e
		 WHERE e.op_type = 'comment'
		   AND json_valid(e.payload)
		   AND json_extract(e.payload, '$.to') = ?
		   AND e.rowid > COALESCE((SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?), 0)
		 ORDER BY e.rowid ASC`, agentID, agentID)
	if err != nil {
		return nil, fmt.Errorf("inbox list %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []InboxEvent
	for rows.Next() {
		var (
			ev        InboxEvent
			payload   string
			createdAt string
		)
		if scanErr := rows.Scan(&ev.Seq, &ev.EventID, &ev.NodeID, &ev.Author, &payload, &createdAt); scanErr != nil {
			return nil, fmt.Errorf("inbox scan: %w", scanErr)
		}
		var p model.CommentPayload
		if json.Unmarshal([]byte(payload), &p) == nil {
			ev.Body = p.Body
			if p.AuthorID != "" {
				ev.Author = p.AuthorID
			}
		}
		ev.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox iterate %q: %w", agentID, err)
	}
	return out, nil
}

// InboxAck advances agentID's read cursor to at least seq — a watermark: every
// event with rowid <= seq is marked seen. Idempotent and monotonic; a lower seq
// never rewinds the cursor.
func (s *Store) InboxAck(ctx context.Context, agentID string, seq int64) error {
	if agentID == "" {
		return fmt.Errorf("inbox ack: agent id required: %w", model.ErrInvalidInput)
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO agent_inbox_cursor (agent_id, cursor) VALUES (?, ?)
			ON CONFLICT(agent_id) DO UPDATE SET cursor = MAX(cursor, excluded.cursor)`,
			agentID, seq)
		return err
	})
}

// inboxPollInterval is how often InboxWait re-queries while blocked. Small
// enough to feel instant to a parked worker, large enough not to spin the DB.
const inboxPollInterval = 250 * time.Millisecond

// InboxWait long-polls the inbox: it returns immediately if the query is
// non-empty, otherwise blocks until a new addressed event lands or the timeout
// elapses (FR-19.4). This is the primitive a worker's outer loop parks on
// between tasks. A timeout returns (nil, nil) — the caller distinguishes it
// from a hit by the slice length (and, at the CLI, a distinct exit code).
func (s *Store) InboxWait(ctx context.Context, agentID string, timeout time.Duration) ([]InboxEvent, error) {
	deadline := time.Now().Add(timeout)
	for {
		events, err := s.InboxList(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			return events, nil
		}
		if !time.Now().Before(deadline) {
			return nil, nil // timed out with an empty inbox
		}
		wait := inboxPollInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}
