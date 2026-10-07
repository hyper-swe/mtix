// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"time"
)

// InboxAckStore persists selective inbox acknowledgements per FR-19.5.
// It deliberately excludes inbox reads and broader store capabilities.
type InboxAckStore interface {
	InboxAck(context.Context, string, int64) error
}

// InboxService owns durable list/wait reads and selective acknowledgements
// without inventing domain broadcasts (FR-19.4/FR-19.5, Commandment 7).
type InboxService struct {
	store  InboxAckStore
	reader InboxReadStore
}

// InboxEvent is the per-agent event contract per FR-19.4, with unchanged JSON.
type InboxEvent sqlite.InboxEvent

// InboxReadStore supplies durable list/wait capabilities to the owning service.
type InboxReadStore interface {
	InboxList(context.Context, string) ([]sqlite.InboxEvent, error)
	InboxWait(context.Context, string, time.Duration) ([]sqlite.InboxEvent, error)
}

// NewInboxService injects the durable selective acknowledgement backend (FR-19.5).
func NewInboxService(st InboxAckStore) *InboxService {
	reader, _ := st.(InboxReadStore)
	return NewInboxServiceWithReads(st, reader)
}

// NewInboxServiceWithReads injects separate durable read/ack capabilities per
// FR-19.4/FR-19.5, preserving ack-only backends and selective acknowledgement.
func NewInboxServiceWithReads(st InboxAckStore, reader InboxReadStore) *InboxService {
	return &InboxService{store: st, reader: reader}
}

// InboxAck acknowledges only the requested event, preserving backend validation,
// idempotency and cancellation errors through wrapping (FR-19.5).
func (s *InboxService) InboxAck(ctx context.Context, agent string, seq int64) error {
	if err := s.store.InboxAck(ctx, agent, seq); err != nil {
		return wrapAPIOperation(fmt.Sprintf("ack inbox event %d for %s", seq, agent), err)
	}
	return nil
}

// InboxList retrieves addressed events in durable order per FR-19.4.
func (s *InboxService) InboxList(ctx context.Context, agent string) ([]InboxEvent, error) {
	if s.reader == nil {
		return nil, wrapAPIOperation("list inbox", model.ErrInvalidInput)
	}
	events, err := s.reader.InboxList(ctx, agent)
	if err != nil {
		return nil, wrapAPIOperation("list inbox", err)
	}
	return inboxEvents(events), nil
}

// InboxWait owns bounded caller-supplied long-poll reads per FR-19.5.
func (s *InboxService) InboxWait(ctx context.Context, agent string, timeout time.Duration) ([]InboxEvent, error) {
	if s.reader == nil {
		return nil, wrapAPIOperation("wait inbox", model.ErrInvalidInput)
	}
	events, err := s.reader.InboxWait(ctx, agent, timeout)
	if err != nil {
		return nil, wrapAPIOperation("wait inbox", err)
	}
	return inboxEvents(events), nil
}

func inboxEvents(events []sqlite.InboxEvent) []InboxEvent {
	if events == nil {
		return nil
	}
	out := make([]InboxEvent, len(events))
	for i, event := range events {
		out[i] = InboxEvent(event)
	}
	return out
}
