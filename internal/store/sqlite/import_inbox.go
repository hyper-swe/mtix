// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Check before replace or UID adoption can remove or rewrite collision evidence.
func checkInboxJournalCollisions(ctx context.Context, tx *sql.Tx, j *InboxJournal) error {
	if j == nil {
		return nil
	}
	for _, incoming := range j.Events {
		var stored inboxJournalEvent
		var op string
		err := tx.QueryRowContext(ctx, `SELECT event_id, project_prefix, node_id,
   COALESCE(uid,''), author_id, payload, created_at, op_type FROM sync_events WHERE event_id=?`, incoming.EventID).
			Scan(&stored.EventID, &stored.Project, &stored.NodeID, &stored.UID, &stored.Author,
				&stored.Payload, &stored.CreatedAt, &op)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("check inbox event collision: %w", err)
		}
		if op != "comment" || !sameInboxContent(stored, incoming) {
			return fmt.Errorf("inbox event identity has different content: %w", model.ErrConflict)
		}
	}
	return nil
}

func importInboxJournal(ctx context.Context, tx *sql.Tx, j *InboxJournal) error {
	if j == nil {
		return nil
	}
	for _, event := range j.Events {
		if err := importInboxEvent(ctx, tx, event); err != nil {
			return err
		}
	}
	return nil
}

func importInboxEvent(ctx context.Context, tx *sql.Tx, e inboxJournalEvent) error {
	// These are recovered inbox records, not an outbound sync queue. Original
	// host identity and clocks are intentionally absent from the transport.
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO sync_events
  (event_id,project_prefix,node_id,uid,op_type,payload,author_id,created_at,
   wall_clock_ts,lamport_clock,vector_clock,author_machine_hash,sync_status)
  VALUES(?,?,?,?,'comment',?,?,?,0,0,'{}','','applied')`,
		e.EventID, e.Project, e.NodeID, nullableString(e.UID), e.Payload, e.Author, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("restore inbox event: %w", err)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count restored inbox event: %w", err)
	}
	if added != 0 {
		// Suppress only the newly restored event. Never advance the scan floor
		// over unrelated pending local work, or change an existing event's status.
		if suppressErr := suppressWorkflowHook(ctx, tx, e.EventID); suppressErr != nil {
			return suppressErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO inbox_restored_events(event_id) VALUES(?)`, e.EventID); err != nil {
			return fmt.Errorf("record restored inbox provenance: %w", err)
		}
	}
	if !e.Acked {
		return nil
	}
	var payload model.CommentPayload
	if err = json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return fmt.Errorf("decode restored inbox recipient: %w", err)
	}
	// Empty acked_at represents coverage whose source had already compacted
	// away its timestamp. Never invent a time or copy a source watermark.
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_inbox_ack(agent_id,event_seq,acked_at)
  SELECT ?,rowid,? FROM sync_events WHERE event_id=?`, payload.To, e.AckedAt, e.EventID)
	if err != nil {
		return fmt.Errorf("restore inbox acknowledgment: %w", err)
	}
	return nil
}
