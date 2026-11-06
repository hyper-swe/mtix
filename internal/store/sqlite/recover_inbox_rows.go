// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Rowid seeks bypass every secondary index and skip healthy gaps (FR-19.1).
const salvageInboxSQL = `SELECT event_id,project_prefix,node_id,COALESCE(uid,''),
 author_id,payload,created_at,rowid,op_type FROM sync_events NOT INDEXED WHERE rowid>=? ORDER BY rowid LIMIT 1`

type salvageMail struct {
	e   inboxJournalEvent
	seq int64
}

// salvageInboxRows unions a verified rowid walk and a readable table-scan prefix.
func salvageInboxRows(ctx context.Context, db *sql.DB, res *RecoverResult) (*InboxJournal, error) {
	journal := &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	rows, readErr := collectSalvageInboxRows(ctx, db)
	var records []salvageMail
	loss := inboxLoss{}
	for _, row := range rows {
		if row.op == "comment" && loss.keep(row.mail.e) {
			records = append(records, row.mail)
		}
	}
	notes := loss.notes()
	if len(notes) > 0 {
		res.InboxIncomplete = true
	}
	res.Notes = append(res.Notes, notes...)
	restoreSalvageCoverage(ctx, db, records, journal, res)
	return journal, readErr
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
		res.InboxIncomplete = true
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
