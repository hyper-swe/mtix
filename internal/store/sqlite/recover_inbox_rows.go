// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Read all comments without relying on damaged secondary indexes. SQL must not
// hide damaged payloads before loss counting (FR-19.1, MTIX-112.2.1).
const salvageInboxSQL = `SELECT event_id,project_prefix,node_id,COALESCE(uid,''),
 author_id,payload,created_at,rowid FROM sync_events NOT INDEXED WHERE op_type='comment' ORDER BY rowid`

type salvageMail struct {
	event inboxJournalEvent
	seq   int64
}

// salvageInboxRows retains the readable prefix even if later rows fail (FR-19.1, MTIX-112.2.1).
func salvageInboxRows(ctx context.Context, db *sql.DB, res *RecoverResult) (journal *InboxJournal, err error) {
	journal = &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	rows, err := db.QueryContext(ctx, salvageInboxSQL)
	if err != nil {
		return nil, fmt.Errorf("read salvage inbox events: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var records []salvageMail
	loss := inboxLoss{}
	for rows.Next() {
		var r salvageMail
		e := &r.event
		if scanErr := rows.Scan(&e.EventID, &e.Project, &e.NodeID, &e.UID, &e.Author, &e.Payload, &e.CreatedAt, &r.seq); scanErr != nil {
			loss.unknown++
			continue
		}
		if loss.keep(*e) {
			records = append(records, r)
		}
	}
	res.Notes = append(res.Notes, loss.notes()...)
	readErr := rows.Err()
	closeErr := rows.Close()
	restoreSalvageCoverage(ctx, db, records, journal, res)
	return journal, errors.Join(readErr, closeErr)
}

// restoreSalvageCoverage combines duplicate identities by recipient coverage (FR-19.1, MTIX-112.2.1).
func restoreSalvageCoverage(ctx context.Context, db *sql.DB, records []salvageMail, j *InboxJournal, res *RecoverResult) {
	unreadable := 0
	positions := map[string]int{}
	for _, r := range records {
		if err := salvageInboxAck(ctx, db, &r.event, r.seq); err != nil {
			unreadable++
		}
		if i, exists := positions[r.event.EventID]; exists {
			if !sameInboxContent(j.Events[i], r.event) {
				res.Notes = append(res.Notes, "conflicting duplicate inbox identity omitted; first readable database copy retained")
				continue
			}
			res.Notes = append(res.Notes, "duplicate inbox identity combined; acknowledgment coverage retained")
			if !j.Events[i].Acked && r.event.Acked {
				j.Events[i].Acked, j.Events[i].AckedAt = true, r.event.AckedAt
			}
			continue
		}
		positions[r.event.EventID] = len(j.Events)
		j.Events = append(j.Events, r.event)
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
