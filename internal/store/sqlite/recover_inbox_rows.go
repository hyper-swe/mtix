// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Read the mail independently of ack tables: damage to bookkeeping must not
// erase readable events. Missing coverage conservatively re-delivers the mail.
const salvageInboxSQL = `SELECT event_id,project_prefix,node_id,COALESCE(uid,''),
 author_id,payload,created_at FROM sync_events WHERE op_type='comment'
 AND CASE WHEN json_valid(payload) THEN
  json_type(payload,'$.to')='text' AND json_extract(payload,'$.to')<>'' ELSE 0 END
 ORDER BY rowid`

func salvageInboxRows(ctx context.Context, db *sql.DB, res *RecoverResult) (journal *InboxJournal, err error) {
	journal = &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	rows, err := db.QueryContext(ctx, salvageInboxSQL)
	if err != nil {
		return nil, fmt.Errorf("read salvage inbox events: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	lost := 0
	for rows.Next() {
		var event inboxJournalEvent
		scanErr := rows.Scan(&event.EventID, &event.Project, &event.NodeID, &event.UID, &event.Author, &event.Payload, &event.CreatedAt)
		if scanErr != nil || validateInboxEvent(event) != nil {
			lost++
			continue
		}
		journal.Events = append(journal.Events, event)
	}
	if lost > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d addressed inbox event(s) unreadable and omitted", lost))
	}
	if err := rows.Err(); err != nil {
		return journal, fmt.Errorf("iterate salvage inbox: %w", err)
	}
	if err := rows.Close(); err != nil {
		return journal, fmt.Errorf("close salvage inbox rows: %w", err)
	}
	unreadable := 0
	for i := range journal.Events {
		if err := salvageInboxAck(ctx, db, &journal.Events[i]); err != nil {
			unreadable++
		}
	}
	if unreadable > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("acknowledgment coverage unreadable for %d addressed inbox event(s); delivered as unacknowledged unless supplied export restores coverage", unreadable))
	}
	return journal, nil
}

func salvageInboxAck(ctx context.Context, db *sql.DB, event *inboxJournalEvent) error {
	// Join by stable identity, then derive coverage from this database's rowid.
	err := db.QueryRowContext(ctx, `SELECT
 (k.event_seq IS NOT NULL OR e.rowid<=COALESCE(c.cursor,0)),COALESCE(k.acked_at,'')
 FROM sync_events e LEFT JOIN agent_inbox_ack k ON k.event_seq=e.rowid
 AND k.agent_id=json_extract(e.payload,'$.to')
 LEFT JOIN agent_inbox_cursor c ON c.agent_id=json_extract(e.payload,'$.to')
 WHERE e.event_id=?`, event.EventID).Scan(&event.Acked, &event.AckedAt)
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
