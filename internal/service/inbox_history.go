// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// InboxHistoryStore supplies the read-only history of events addressed to an
// agent without widening the list, wait, count or acknowledgement contracts
// (MTIX-112.2).
type InboxHistoryStore interface {
	InboxHistory(context.Context, string) ([]sqlite.InboxHistoryEntry, error)
}

// InboxHistory returns every event ever addressed to agent with its
// acknowledgement state and time. It never acknowledges, moves a cursor or
// refreshes a session, and it contacts no hub (MTIX-112.2).
func (s *InboxService) InboxHistory(ctx context.Context, agent string) ([]sqlite.InboxHistoryEntry, error) {
	if err := ValidateInboxAgent(agent); err != nil {
		return nil, wrapAPIOperation("inbox history", err)
	}
	if s.history == nil {
		return nil, wrapAPIOperation("inbox history", model.ErrInvalidInput)
	}
	out, err := s.history.InboxHistory(ctx, agent)
	if err != nil {
		return nil, wrapAPIOperation("inbox history", err)
	}
	return out, nil
}
