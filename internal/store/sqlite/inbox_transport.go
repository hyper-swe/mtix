// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxJournal carries addressed mail and recipient acknowledgment coverage for
// explicit exports (FR-19.1). It never carries local rowids or dispatch state.
type InboxJournal struct {
	Version          int                 `json:"version"`
	Events           []inboxJournalEvent `json:"events"`
	Checksum         string              `json:"checksum"`
	Notes            []string            `json:"-"`
	UncarriedUnacked int64               `json:"-"`
}

type inboxJournalEvent struct {
	EventID   string `json:"event_id"`
	Project   string `json:"project"`
	NodeID    string `json:"node_id"`
	UID       string `json:"uid,omitempty"`
	Author    string `json:"author"`
	Payload   string `json:"payload"`
	CreatedAt string `json:"created_at"`
	Acked     bool   `json:"acked"`
	AckedAt   string `json:"acked_at,omitempty"`
}

// ExportWithInbox exports board and addressed journal from one WAL snapshot
// (FR-7.8, FR-19.1). Automatic mirrors must use Export instead.
func (s *Store) ExportWithInbox(ctx context.Context, project, version string) (*ExportData, error) {
	var journal *InboxJournal
	data, err := s.readExportSnapshotWith(ctx, func(tx *sql.Tx) error {
		var readErr error
		journal, readErr = readInboxJournal(ctx, tx)
		return readErr
	})
	if err != nil {
		return nil, fmt.Errorf("export inbox snapshot: %w", err)
	}
	data.InboxJournal = journal
	return s.finishExport(data, project, version)
}

// Read only addressed comments, with coverage for their declared recipient.
const exportInboxJournalSQL = `SELECT e.event_id, e.project_prefix, e.node_id,
 COALESCE(e.uid,''), e.author_id, e.payload, e.created_at,
 (k.event_seq IS NOT NULL OR e.rowid <= COALESCE(c.cursor,0)), COALESCE(k.acked_at,'')
 FROM sync_events e
 LEFT JOIN agent_inbox_ack k ON k.event_seq=e.rowid
  AND k.agent_id=CASE WHEN json_valid(e.payload) THEN json_extract(e.payload,'$.to') END
 LEFT JOIN agent_inbox_cursor c ON c.agent_id=CASE WHEN json_valid(e.payload) THEN json_extract(e.payload,'$.to') END
 WHERE e.op_type='comment'
 ORDER BY e.rowid`

func inboxJournalChecksum(j *InboxJournal) (string, error) {
	document := struct {
		Version int                 `json:"version"`
		Events  []inboxJournalEvent `json:"events"`
	}{j.Version, j.Events}
	raw, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode inbox journal checksum: %w", err)
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func validateInboxJournal(j *InboxJournal) error {
	if j == nil {
		return nil
	}
	if j.Version != 1 {
		return fmt.Errorf("unsupported inbox journal version: %w", model.ErrInvalidInput)
	}
	sum, err := inboxJournalChecksum(j)
	if err != nil {
		return err
	}
	if sum != j.Checksum {
		return fmt.Errorf("inbox journal checksum mismatch: %w", model.ErrInvalidInput)
	}
	seen := make(map[string]bool, len(j.Events))
	for _, event := range j.Events {
		if seen[event.EventID] {
			return fmt.Errorf("duplicate inbox event identity: %w", model.ErrInvalidInput)
		}
		seen[event.EventID] = true
		if validationErr := validateInboxEvent(event); validationErr != nil {
			return validationErr
		}
	}
	return nil
}
