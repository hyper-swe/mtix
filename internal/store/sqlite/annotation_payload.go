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

// annotationPayload carries the changed annotation rather than a bulk-update
// placeholder. A resolution has no To, Hop or ReplyBy and sends no new message.
func annotationPayload(previous string, next []model.Annotation, meta commentMeta) (*model.CommentPayload, string) {
	body, author, to := newCommentForSyncEvent(previous, next)
	p := &model.CommentPayload{AuthorID: author, Body: body, To: to, Hop: addressedStoredHop(to, meta.Hop), ReplyBy: addressedReplyBy(to, meta.ReplyBy)}
	var prev []model.Annotation
	if previous != "" && json.Unmarshal([]byte(previous), &prev) != nil {
		return p, p.AuthorID
	}
	for _, n := range next {
		var old *model.Annotation
		for i := range prev {
			if prev[i].ID == n.ID {
				old = &prev[i]
				break
			}
		}
		if old != nil && old.Resolved == n.Resolved {
			continue
		}
		if n.ID == "" {
			return p, p.AuthorID
		}
		p.AuthorID, p.Body = n.Author, n.Text
		p.Annotation = &model.CommentAnnotationState{ID: n.ID, CreatedAt: n.CreatedAt, Resolved: n.Resolved, Addressee: n.Addressee}
		if old != nil {
			p.To = ""
			p.Hop = 0
			p.ReplyBy = ""
			return p, ""
		}
		return p, p.AuthorID
	}
	return p, p.AuthorID
}

// applyAnnotationSnapshot upserts one stable annotation, including a resolution
// received before its creation. The largest Lamport/event-id key owns it.
func applyAnnotationSnapshot(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, p model.CommentPayload, current []model.Annotation) ([]model.Annotation, error) {
	n := p.Annotation
	if n.ID == "" {
		return nil, fmt.Errorf("apply annotation snapshot: missing identity: %w", model.ErrInvalidInput)
	}
	winner, err := annotationSnapshotWinner(ctx, tx, e, n.ID)
	if err != nil {
		return nil, err
	}
	if winner != e.EventID {
		return current, nil
	}
	value := model.Annotation{ID: n.ID, Author: p.AuthorID, Text: p.Body, CreatedAt: n.CreatedAt, Resolved: n.Resolved, Addressee: n.Addressee}
	for i := range current {
		if current[i].ID == n.ID {
			current[i] = value
			return current, nil
		}
	}
	return append(current, value), nil
}

func annotationSnapshotWinner(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, id string) (string, error) {
	// UID scopes protect a reused display number; old UID-less events match by
	// the event's number, just as the workflow winner lookup does.
	var winner string
	var err error
	if e.UID == "" {
		err = tx.QueryRowContext(ctx, `SELECT event_id FROM sync_events WHERE op_type = 'comment' AND node_id = ?
   AND json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END, '$.annotation.id') = ?
   ORDER BY lamport_clock DESC, event_id DESC LIMIT 1`, e.NodeID, id).Scan(&winner)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT event_id FROM sync_events WHERE op_type = 'comment'
   AND ((uid = ? AND uid <> '') OR ((uid IS NULL OR uid = '') AND node_id = ?))
   AND json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END, '$.annotation.id') = ?
   ORDER BY lamport_clock DESC, event_id DESC LIMIT 1`, e.UID, e.NodeID, id).Scan(&winner)
	}
	if err != nil {
		return "", fmt.Errorf("read annotation snapshot winner: %w", err)
	}
	return winner, nil
}

// A resolution snapshot can arrive before an old creation event. Preserve the
// snapshot rather than appending that legacy event a second time.
func appendLegacyAnnotation(current []model.Annotation, e *model.SyncEvent, p model.CommentPayload, at time.Time) []model.Annotation {
	for _, n := range current {
		if n.ID == e.EventID {
			return current
		}
	}
	return append(current, model.Annotation{ID: e.EventID, Author: p.AuthorID, Text: p.Body, CreatedAt: at, Addressee: p.To})
}
