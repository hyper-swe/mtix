// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Arrival time, display number and adopted UID are replica bookkeeping.
// An event's author and payload remain immutable across synchronization.
func sameInboxContent(a, b inboxJournalEvent) bool {
	if a.EventID != b.EventID || a.Author != b.Author {
		return false
	}
	left, leftErr := canonicalInboxPayload(a.Payload)
	right, rightErr := canonicalInboxPayload(b.Payload)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func retainedInboxIDs(j *InboxJournal) (string, error) {
	var ids []string
	if j != nil {
		for _, event := range j.Events {
			ids = append(ids, event.EventID)
		}
	}
	return hexIDList(ids)
}

// Preserve integer precision: unknown payload fields are immutable content too.
func canonicalInboxPayload(raw string) ([]byte, error) {
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("invalid inbox payload JSON")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
