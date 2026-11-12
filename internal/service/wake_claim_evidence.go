// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxAckReader is the read-only inbox capability the default delivery evidence needs.
type InboxAckReader interface {
	InboxEventAcked(ctx context.Context, agent string, seq int64) (bool, error)
}

// InboxAckEvidence is the delivery evidence both routes use in production (MTIX-112.40.13):
// an event its recipient already acknowledged was read, so a reclaimed claim for it is
// settled as confirmed instead of waking the session again. An unread event is no
// evidence, so it is redelivered once; an error counts as no evidence (never a lost wake).
func InboxAckEvidence(reader InboxAckReader) WakeEvidence {
	return func(ctx context.Context, key model.WakeKey) (bool, error) {
		acked, err := reader.InboxEventAcked(ctx, key.AgentID, key.EventSeq)
		if err != nil {
			return false, fmt.Errorf("wake delivery evidence: %w", err)
		}
		return acked, nil
	}
}

// defaultWakeEvidence keeps an explicit source and otherwise derives the acknowledgement
// evidence from the ledger store when it can read the inbox. nil means no evidence.
func defaultWakeEvidence(explicit WakeEvidence, store any) WakeEvidence {
	if explicit != nil {
		return explicit
	}
	if reader, ok := store.(InboxAckReader); ok {
		return InboxAckEvidence(reader)
	}
	return nil
}
