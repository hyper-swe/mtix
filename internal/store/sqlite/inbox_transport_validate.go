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

func validInboxIdentity(e inboxJournalEvent) bool {
	return e.CreatedAt != "" && e.EventID != "" && len(e.EventID) <= 64*1024 &&
		model.IsValidAuthorID(e.Author) && model.ValidateSyncNodeID(e.Project+"-1") == nil && model.ValidateSyncNodeID(e.NodeID) == nil
}

func validInboxPayload(raw string) bool {
	var payload model.CommentPayload
	return json.Unmarshal([]byte(raw), &payload) == nil && payload.To != "" && model.ValidateAgentID(payload.To) == nil &&
		(payload.AuthorID == "" || model.IsValidAuthorID(payload.AuthorID))
}
