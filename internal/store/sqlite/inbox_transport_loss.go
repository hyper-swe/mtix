// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
)

type inboxLoss struct {
	unknown int
	invalid []string
}

// A corrupt comment cannot be classified as addressed or ordinary. A valid
// ordinary comment is intentionally excluded, never counted as lost mail.
func (l *inboxLoss) keep(e inboxJournalEvent) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(e.Payload), &fields) != nil || fields == nil {
		l.unknown++
		return false
	}
	raw, exists := fields["to"]
	if !exists || string(raw) == "null" {
		return false
	}
	var to string
	if json.Unmarshal(raw, &to) != nil {
		l.unknown++
		return false
	}
	if to == "" {
		return false
	}
	if err := validateInboxEvent(e); err != nil {
		l.invalid = append(l.invalid, err.Error())
		return false
	}
	return true
}

func (l *inboxLoss) notes() []string {
	var notes []string
	if l.unknown > 0 {
		notes = append(notes, fmt.Sprintf("%d unreadable comment record(s) omitted; recipient and addressed-mail loss unknown", l.unknown))
	}
	if len(l.invalid) > 0 {
		notes = append(notes, fmt.Sprintf("%d addressed inbox event(s) unreadable and omitted", len(l.invalid)))
		notes = append(notes, l.invalid...)
	}
	return notes
}

func countUncarriedInbox(ctx context.Context, q rowQuerier, j *InboxJournal) {
	var total int64
	if err := q.QueryRowContext(ctx, unackedInboxSQL).Scan(&total); err != nil {
		j.Notes = append(j.Notes, fmt.Sprintf("unacknowledged mail not carried could not be counted: %v", err))
		return
	}
	for _, event := range j.Events {
		if !event.Acked {
			total--
		}
	}
	j.UncarriedUnacked = max(total, 0)
}
