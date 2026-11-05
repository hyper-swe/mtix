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

// ReplyByEvent is an addressed comment event that carries a reply deadline
// (MTIX-112.20.3). Seq is the sync_events rowid, the local arrival order.
type ReplyByEvent struct {
	Seq     int64
	EventID string
	Author  string
	To      string
	ReplyBy string
	// Local is true for an event this machine wrote itself (not pulled from
	// the hub): delivering it to a local agent needs no hub contact.
	Local bool
}

// ReplyByEvents returns addressed comment events past afterSeq that carry a
// reply_by payload key, oldest first, at most limit. Local marks an event this
// machine wrote (sync_status other than applied). It is a local read over
// the journal: it never touches the hub.
func (s *Store) ReplyByEvents(ctx context.Context, afterSeq int64, limit int) ([]ReplyByEvent, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("reply events: limit must be positive: %w", model.ErrInvalidInput)
	}
	// Addressed comments with a reply deadline, in arrival order (rowid).
	// json_valid guards a hand-damaged payload; the index-free scan is bounded by limit.
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT e.rowid, e.event_id, e.author_id, e.payload,
          CASE WHEN EXISTS (SELECT 1 FROM inbox_restored_events r WHERE r.event_id=e.event_id) THEN 'restored' ELSE e.sync_status END
		  FROM sync_events e
		 WHERE e.op_type = 'comment'
		   AND e.rowid > ?
		   AND json_valid(e.payload)
		   AND json_extract(e.payload, '$.reply_by') IS NOT NULL
		   AND COALESCE(json_extract(e.payload, '$.to'), '') <> ''
		 ORDER BY e.rowid ASC
		 LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("reply events after %d: %w", afterSeq, err)
	}
	defer func() { _ = rows.Close() }()
	var out []ReplyByEvent
	for rows.Next() {
		var ev ReplyByEvent
		var payload, status string
		if scanErr := rows.Scan(&ev.Seq, &ev.EventID, &ev.Author, &payload, &status); scanErr != nil {
			return nil, fmt.Errorf("reply events scan: %w", scanErr)
		}
		var p model.CommentPayload
		if json.Unmarshal([]byte(payload), &p) != nil {
			continue // damaged payload: not an actionable deadline
		}
		if p.AuthorID != "" {
			ev.Author = p.AuthorID
		}
		ev.To, ev.ReplyBy = p.To, p.ReplyBy
		// Events applied from the hub carry 'applied'; this machine's own
		// writes are pending or pushed (or conflicted) and needed no pull.
		ev.Local = status != string(model.SyncStatusApplied)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reply events rows: %w", err)
	}
	return out, nil
}

// LatestSyncSeq returns the highest sync_events rowid, 0 for an empty journal.
func (s *Store) LatestSyncSeq(ctx context.Context) (int64, error) {
	var seq int64
	// Highest local arrival number: the mark a reply window opens after.
	if err := s.readDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0) FROM sync_events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("latest sync seq: %w", err)
	}
	return seq, nil
}

// ReplyArrived reports whether a comment authored by from and addressed to to
// arrived after afterSeq (MTIX-112.20.3): the reply that closes a sender window.
func (s *Store) ReplyArrived(ctx context.Context, from, to string, afterSeq int64) (bool, error) {
	if from == "" || to == "" {
		return false, fmt.Errorf("reply arrived: both agents required: %w", model.ErrInvalidInput)
	}
	var one int
	// Existence probe over the journal; the author is the payload author when
	// present, else the envelope author, matching the inbox.
	err := s.readDB.QueryRowContext(ctx, `
		SELECT 1 FROM sync_events e
		 WHERE e.op_type = 'comment'
		   AND e.rowid > ?
		   AND json_valid(e.payload)
		   AND json_extract(e.payload, '$.to') = ?
		   AND COALESCE(NULLIF(json_extract(e.payload, '$.author_id'), ''), e.author_id) = ?
		 LIMIT 1`, afterSeq, to, from).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("reply arrived from %s to %s: %w", from, to, err)
	}
	return true, nil
}
