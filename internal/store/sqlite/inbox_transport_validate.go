// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// validateInboxEvent checks transport bounds and storable timestamps (FR-19.1, MTIX-112.2.1).
func validateInboxEvent(e inboxJournalEvent) error {
	if !validInboxIdentity(e) || !validInboxPayload(e.Payload) {
		return fmt.Errorf("invalid addressed journal event %s: %w", format.EventID(e.EventID), model.ErrInvalidInput)
	}
	for _, stamp := range []string{e.CreatedAt, e.AckedAt} {
		if stamp == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || !model.IsStorableTime(parsed) {
			return fmt.Errorf("invalid inbox journal timestamp for %s: %w", format.EventID(e.EventID), model.ErrInvalidInput)
		}
	}
	if !e.Acked && e.AckedAt != "" {
		return fmt.Errorf("inconsistent inbox journal acknowledgment for %s: %w", format.EventID(e.EventID), model.ErrInvalidInput)
	}
	return nil
}

// validInboxIdentity preserves historical actor text within transport bounds (FR-19.1, MTIX-112.2.1).
func validInboxIdentity(e inboxJournalEvent) bool {
	return e.CreatedAt != "" && e.EventID != "" && len(e.EventID) <= 64*1024 &&
		e.Author != "" && len(e.Author) <= 64 && model.ValidateSyncNodeID(e.Project+"-1") == nil && model.ValidateSyncNodeID(e.NodeID) == nil
}

// validInboxPayload requires a bounded recipient; payload author stays optional (FR-19.1, MTIX-112.2.1).
func validInboxPayload(raw string) bool {
	var payload model.CommentPayload
	return json.Unmarshal([]byte(raw), &payload) == nil && payload.To != "" && len(payload.To) <= 64 && len(payload.AuthorID) <= 64
}
