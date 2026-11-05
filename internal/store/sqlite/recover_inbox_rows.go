// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Point reads bypass secondary indexes and isolate a damaged leaf (FR-19.1).
const salvageInboxSQL = `SELECT event_id,project_prefix,node_id,COALESCE(uid,''),
 author_id,payload,created_at,rowid,op_type FROM sync_events NOT INDEXED WHERE rowid=?`

type salvageMail struct {
	e   inboxJournalEvent
	seq int64
}

// salvageInboxRows visits each indexed row independently, so damage never ends
// the scan of later readable mail. Failed rows are counted explicitly (FR-19.1).
func salvageInboxRows(ctx context.Context, db *sql.DB, res *RecoverResult) (*InboxJournal, error) {
	keys, keyErr := salvageInboxKeys(ctx, db)
	journal := &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	var records []salvageMail
	loss := inboxLoss{}
	unreadable := 0
	var firstReadErr error
	for _, seq := range keys {
		if ctx.Err() != nil {
			keyErr = errors.Join(keyErr, ctx.Err())
			break
		}
		r := salvageMail{seq: seq}
		e := &r.e
		var op string
		err := db.QueryRowContext(ctx, salvageInboxSQL, seq).Scan(&e.EventID, &e.Project, &e.NodeID, &e.UID, &e.Author, &e.Payload, &e.CreatedAt, &r.seq, &op)
		if err != nil {
			unreadable++
			if firstReadErr == nil {
				firstReadErr = err
			}
			continue
		}
		if op == "comment" && loss.keep(*e) {
			records = append(records, r)
		}
	}
	res.Notes = append(res.Notes, loss.notes()...)
	if unreadable > 0 {
		keyErr = errors.Join(keyErr, firstReadErr, fmt.Errorf("%d unreadable sync event record(s) omitted; addressed-mail loss among these records unknown", unreadable))
	}
	restoreSalvageCoverage(ctx, db, records, journal, res)
	return journal, keyErr
}

// restoreSalvageCoverage combines duplicate identities by recipient coverage (FR-19.1, MTIX-112.2.1).
func restoreSalvageCoverage(ctx context.Context, db *sql.DB, records []salvageMail, j *InboxJournal, res *RecoverResult) {
	unreadable := 0
	positions := map[string]int{}
	for _, r := range records {
		if err := salvageInboxAck(ctx, db, &r.e, r.seq); err != nil {
			unreadable++
		}
		if i, exists := positions[r.e.EventID]; exists {
			if !sameInboxContent(j.Events[i], r.e) {
				res.Notes = append(res.Notes, "conflicting duplicate inbox identity omitted; first readable database copy retained")
				continue
			}
			res.Notes = append(res.Notes, "duplicate inbox identity combined; acknowledgment coverage retained")
			if !j.Events[i].Acked && r.e.Acked {
				j.Events[i].Acked, j.Events[i].AckedAt = true, r.e.AckedAt
			}
			continue
		}
		positions[r.e.EventID] = len(j.Events)
		j.Events = append(j.Events, r.e)
	}
	if unreadable > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("acknowledgment coverage unreadable for %d addressed inbox event(s); delivered as unacknowledged unless supplied export restores coverage", unreadable))
	}
}

// salvageInboxAck reads coverage by source rowid and validates its time (FR-19.1, MTIX-112.2.1).
func salvageInboxAck(ctx context.Context, db *sql.DB, event *inboxJournalEvent, seq int64) error {
	// A damaged uniqueness index can leave duplicate IDs. Coverage belongs to
	// the exact readable source row, whose sequence is never transported.
	err := db.QueryRowContext(ctx, `SELECT
 (k.event_seq IS NOT NULL OR e.rowid<=COALESCE(c.cursor,0)),COALESCE(k.acked_at,'')
 FROM sync_events e LEFT JOIN agent_inbox_ack k ON k.event_seq=e.rowid
 AND k.agent_id=json_extract(e.payload,'$.to')
 LEFT JOIN agent_inbox_cursor c ON c.agent_id=json_extract(e.payload,'$.to')
 WHERE e.rowid=?`, seq).Scan(&event.Acked, &event.AckedAt)
	if err == nil {
		err = validateInboxEvent(*event)
	}
	if err != nil {
		event.Acked = false
		event.AckedAt = ""
		return fmt.Errorf("read salvage acknowledgment: %w", err)
	}
	return nil
}
