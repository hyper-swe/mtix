// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Recovery reads inbox state independently of damaged node indexes. The
// supplied export can fill events absent from the readable database prefix.
func recoverInboxJournal(ctx context.Context, dbPath string, mirror *ExportData, res *RecoverResult, limit time.Duration, sourceBefore os.FileInfo, inboxSources ...*ExportData) *InboxJournal {
	r := inboxRecoverer{salvage: salvageInboxJournal}
	return r.recover(ctx, dbPath, mirror, res, limit, sourceBefore, inboxSources...)
}

// inboxRecoverer carries the salvage read as a field so a test can change the
// source during that read and prove the size is re-checked after it.
type inboxRecoverer struct {
	salvage func(ctx context.Context, path string, res *RecoverResult, limit time.Duration) (*InboxJournal, error)
}

// recover is recoverInboxJournal with an injectable salvage read.
func (r inboxRecoverer) recover(ctx context.Context, dbPath string, mirror *ExportData, res *RecoverResult, limit time.Duration, sourceBefore os.FileInfo, inboxSources ...*ExportData) *InboxJournal {
	journal, readErr := r.salvage(ctx, dbPath, res, limit)
	checkRecoveredSourceSize(dbPath, sourceBefore, res)
	if readErr != nil {
		res.InboxIncomplete = true
		if errors.Is(readErr, errInboxIntegrityUnfinished) {
			res.Notes = append(res.Notes, fmt.Sprintf("inbox journal acknowledgment coverage could not be proven: %v; the time limit alone does not establish damage. If the store opens, use mtix export to preserve readable acknowledgment coverage.", readErr))
		} else {
			res.Notes = append(res.Notes, fmt.Sprintf("inbox journal partially unreadable or unavailable: %v", readErr))
		}
	}
	if journal == nil {
		journal = &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	}
	available := readErr == nil || len(journal.Events) > 0
	for _, source := range append([]*ExportData{mirror}, inboxSources...) {
		if source == nil || source.InboxJournal == nil {
			continue
		}
		if err := validateInboxJournal(source.InboxJournal); err != nil {
			res.InboxIncomplete = true
			res.Notes = append(res.Notes, fmt.Sprintf("export inbox journal rejected: %v", err))
			continue
		}
		available = true
		mergeRecoveredInbox(journal, source.InboxJournal, res)
	}

	if !available {
		res.InboxIncomplete = true
		res.Notes = append(res.Notes, "inbox journal unavailable from both database and supplied export; lost event count unknown; ordinary tasks.json carries no journal")
		return nil
	}
	// Any uncertainty applies to the whole journal, including supplied exports.
	// A damaged row or index cannot authenticate its own acknowledgment coverage.
	if res.InboxIncomplete {
		for i := range journal.Events {
			journal.Events[i].Acked, journal.Events[i].AckedAt = false, ""
		}
	}
	sum, err := inboxJournalChecksum(journal)
	if err != nil {
		res.InboxIncomplete = true
		res.Notes = append(res.Notes, fmt.Sprintf("recovered inbox checksum failed: %v", err))
		return nil
	}
	journal.Checksum = sum
	if res.InboxIncomplete {
		res.Notes = append(res.Notes, fmt.Sprintf("recovered %d addressed inbox events; acknowledgment coverage discarded because recovery is incomplete; all %d will be delivered as unread in a fresh store; already-read mail may cause repeated wakes", len(journal.Events), len(journal.Events)))
	} else {
		res.Notes = append(res.Notes, fmt.Sprintf("recovered %d addressed inbox events; acknowledgment coverage retained after a completed integrity check and complete salvage", len(journal.Events)))
	}
	return journal
}

// salvageInboxJournal reads damaged files without writes or index dependence (FR-19.1, MTIX-112.2.1).
func salvageInboxJournal(ctx context.Context, path string, res *RecoverResult, limit time.Duration) (journal *InboxJournal, err error) {
	if sourceErr := checkRecoverDatabase(path); sourceErr != nil {
		return nil, sourceErr
	}
	// Apply salvage pragmas on every connection through the shared DSN builder.
	params := append(readOnlyDSNParams(), "_pragma=cell_size_check(0)")
	db, err := openSQLite(path, params...)
	if err != nil {
		return nil, fmt.Errorf("open journal for salvage: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close salvage journal: %w", closeErr)
		}
	}()
	integrityErr := checkInboxIntegrity(ctx, db, limit)
	if integrityErr != nil {
		res.InboxIncomplete = true
	}
	journal, err = salvageInboxRows(ctx, db, res)
	return journal, errors.Join(err, integrityErr)
}

// mergeRecoveredInbox unions compatible identities and ack coverage (FR-19.1, MTIX-112.2.1).
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
		if !sameInboxContent(dst.Events[i], event) {
			res.InboxIncomplete = true
			res.Notes = append(res.Notes, "conflicting inbox event in supplied export skipped; database copy retained")
			continue
		}
		if !dst.Events[i].Acked && event.Acked {
			dst.Events[i].Acked, dst.Events[i].AckedAt = true, event.AckedAt
		}
	}
}
