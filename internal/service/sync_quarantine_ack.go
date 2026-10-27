// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// maxQuarantineAcknowledgementReasonBytes bounds immutable operator input (MTIX-187.2).
const maxQuarantineAcknowledgementReasonBytes = 500

// AcknowledgeQuarantine accepts one invalid-node-id pull/sweep as unapplied on
// this replica. It preserves the raw quarantine, retries and every sync clock
// and reports valid repeats without replacing the first receipt (MTIX-187.2).
func (s *SyncService) AcknowledgeQuarantine(ctx context.Context, eventID, actor, reason string) (sqlite.QuarantineAcknowledgementResult, error) {
	var empty sqlite.QuarantineAcknowledgementResult
	if strings.TrimSpace(eventID) == "" {
		return empty, fmt.Errorf("event id is required: %w", model.ErrInvalidInput)
	}
	if !model.IsValidAuthorID(actor) || strings.TrimSpace(reason) == "" {
		return empty, fmt.Errorf("a valid author and nonblank reason are required: %w", model.ErrInvalidInput)
	}
	if len(reason) > maxQuarantineAcknowledgementReasonBytes {
		return empty, fmt.Errorf("acknowledgement reason exceeds %d UTF-8 bytes: %w", maxQuarantineAcknowledgementReasonBytes, model.ErrInvalidInput)
	}
	q, err := s.store.QuarantineForAcknowledgement(ctx, eventID)
	if err != nil {
		return empty, fmt.Errorf("read acknowledgement candidate: %w", err)
	}
	if err := validateQuarantineAcknowledgement(q); err != nil {
		return empty, fmt.Errorf("validate acknowledgement candidate: %w", err)
	}
	sum := sha256.Sum256([]byte(q.RawEvent))
	receipt := sqlite.QuarantineAcknowledgement{
		EventID: eventID, RawEventSHA256: hex.EncodeToString(sum[:]), Source: q.Source,
		Actor: actor, Reason: reason, AcknowledgedAt: s.clock().UTC().Format(time.RFC3339Nano),
	}
	return s.store.RecordQuarantineAcknowledgement(ctx, q, receipt)
}

func validateQuarantineAcknowledgement(q sqlite.QuarantinedEvent) error {
	if q.Source != "pull" && q.Source != "sweep" {
		return fmt.Errorf("only pulled invalid-node-id events can be acknowledged: %w", model.ErrInvalidInput)
	}
	var event model.SyncEvent
	if err := json.Unmarshal([]byte(q.RawEvent), &event); err != nil {
		return fmt.Errorf("decode retained event: %w", err)
	}
	if event.EventID != q.EventID {
		return fmt.Errorf("retained event id does not match quarantine: %w", model.ErrInvalidInput)
	}
	// Ingest rejects malformed envelopes before its PR153 node-id check.
	// Nil observations make the reference time irrelevant to this classification.
	if !errors.Is(validator.ValidateIngest(&event, time.Time{}, nil), validator.ErrInvalidNodeID) {
		return fmt.Errorf("retained event is not refused for an invalid node id: %w", model.ErrInvalidInput)
	}
	return nil
}

// QuarantineAcknowledgementEligible reports the same raw-event eligibility the
// acknowledgement command enforces. It does not grant a receipt or write data.
func QuarantineAcknowledgementEligible(q sqlite.QuarantinedEvent) bool {
	return validateQuarantineAcknowledgement(q) == nil
}
