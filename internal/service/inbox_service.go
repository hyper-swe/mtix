// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
)

// InboxAckStore persists selective inbox acknowledgements per FR-19.5.
// It deliberately excludes inbox reads and broader store capabilities.
type InboxAckStore interface {
	InboxAck(context.Context, string, int64) error
}

// InboxService owns acknowledgement writes without inventing domain broadcasts (FR-19.5).
type InboxService struct{ store InboxAckStore }

// NewInboxService injects the durable selective acknowledgement backend (FR-19.5).
func NewInboxService(st InboxAckStore) *InboxService { return &InboxService{store: st} }

// InboxAck acknowledges only the requested event, preserving backend validation,
// idempotency and cancellation errors through wrapping (FR-19.5).
func (s *InboxService) InboxAck(ctx context.Context, agent string, seq int64) error {
	if err := s.store.InboxAck(ctx, agent, seq); err != nil {
		return wrapAPIOperation(fmt.Sprintf("ack inbox event %d for %s", seq, agent), err)
	}
	return nil
}
