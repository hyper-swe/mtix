// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
)

// Recovery reads inbox state independently of damaged node indexes. The
// supplied export can fill events absent from the readable database prefix.
func recoverInboxJournal(ctx context.Context, dbPath string, mirror *ExportData, res *RecoverResult) *InboxJournal {
	journal, readErr := salvageInboxJournal(ctx, dbPath, res)
	if readErr != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("inbox journal partially unreadable or unavailable: %v", readErr))
	}
	if journal == nil {
		journal = &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	}
	available := readErr == nil || len(journal.Events) > 0
	if mirror != nil && mirror.InboxJournal != nil {
		if err := validateInboxJournal(mirror.InboxJournal); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("export inbox journal rejected: %v", err))
		} else {
			available = true
			mergeRecoveredInbox(journal, mirror.InboxJournal, res)
		}
	}
	if !available {
		res.Notes = append(res.Notes, "inbox journal unavailable from both database and supplied export; lost event count unknown; ordinary tasks.json carries no journal")
		return nil
	}
	sum, err := inboxJournalChecksum(journal)
	if err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("recovered inbox checksum failed: %v", err))
		return nil
	}
	journal.Checksum = sum
	res.Notes = append(res.Notes, fmt.Sprintf("recovered %d addressed inbox events; acknowledgment coverage retained where readable", len(journal.Events)))
	return journal
}

func salvageInboxJournal(ctx context.Context, path string, res *RecoverResult) (journal *InboxJournal, err error) {
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("open journal for salvage: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close salvage journal: %w", closeErr)
		}
	}()
	journal, err = salvageInboxRows(ctx, db, res)
	return journal, err
}

func mergeRecoveredInbox(dst, src *InboxJournal, res *RecoverResult) {
	positions := make(map[string]int, len(dst.Events))
	for i, event := range dst.Events {
		positions[event.EventID] = i
	}
	for _, event := range src.Events {
		i, exists := positions[event.EventID]
		if !exists {
			positions[event.EventID] = len(dst.Events)
			dst.Events = append(dst.Events, event)
			continue
		}
		local, incoming := dst.Events[i], event
		local.Acked, local.AckedAt = false, ""
		incoming.Acked, incoming.AckedAt = false, ""
		if local != incoming {
			res.Notes = append(res.Notes, "conflicting inbox event in supplied export skipped; database copy retained")
			continue
		}
		if !dst.Events[i].Acked && event.Acked {
			dst.Events[i].Acked, dst.Events[i].AckedAt = true, event.AckedAt
		}
	}
}
