// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"
)

func readInboxJournal(ctx context.Context, q rowQuerier) (out *InboxJournal, err error) {
	out = &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}
	rows, err := q.QueryContext(ctx, exportInboxJournalSQL)
	if err != nil {
		return nil, fmt.Errorf("read addressed journal: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	loss := inboxLoss{}
	for rows.Next() {
		var event inboxJournalEvent
		if err = rows.Scan(&event.EventID, &event.Project, &event.NodeID, &event.UID,
			&event.Author, &event.Payload, &event.CreatedAt, &event.Acked, &event.AckedAt); err != nil {
			return out, fmt.Errorf("scan addressed journal: %w", err)
		}
		if !loss.keep(event) {
			continue
		}
		out.Events = append(out.Events, event)
	}
	if err = rows.Err(); err != nil {
		return out, fmt.Errorf("iterate addressed journal: %w", err)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return out, closeErr
	}
	out.Notes = loss.notes()
	countUncarriedInbox(ctx, q, out)
	out.Checksum, err = inboxJournalChecksum(out)
	if err == nil {
		err = validateInboxJournal(out)
	}
	return out, err
}
