// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxCountStore supplies optional read-only counts without widening the
// existing list/wait or acknowledgement contracts (FR-19.1).
type InboxCountStore interface {
	InboxCount(context.Context, string) (int64, int64, error)
}

// ValidateInboxAgent requires an unchanged local-agent identity for count
// queries per FR-19.1 and the MTIX-125 agent contract.
func ValidateInboxAgent(agent string) error {
	if agent == "" {
		return fmt.Errorf("agent ID is required: %w", model.ErrInvalidInput)
	}
	return validateAgentID(agent)
}

// InboxCount reads pending and total counts from one optional backend per
// FR-19.1, preserving cancellation and backend error identities.
func (s *InboxService) InboxCount(ctx context.Context, agent string) (int64, int64, error) {
	if err := ValidateInboxAgent(agent); err != nil {
		return 0, 0, wrapAPIOperation("count inbox", err)
	}
	if s.counter == nil {
		return 0, 0, wrapAPIOperation("count inbox", model.ErrInvalidInput)
	}
	pending, total, err := s.counter.InboxCount(ctx, agent)
	if err != nil {
		return 0, 0, wrapAPIOperation("count inbox", err)
	}
	return pending, total, nil
}
