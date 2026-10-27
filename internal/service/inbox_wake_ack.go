// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

type inboxAckCoverage interface {
	WakeAckMark(context.Context, string) (int64, error)
}

type inboxAtomicAcker interface {
	InboxAckCoverage(context.Context, string, int64, bool) (int64, int64, error)
}

func (s *InboxService) acknowledgeWithWake(ctx context.Context, agent string, seq int64, through bool, write func() error) error {
	if backend, ok := s.store.(inboxAtomicAcker); ok && s.wakeAcks != nil {
		before, after, err := backend.InboxAckCoverage(ctx, agent, seq, through)
		if err != nil {
			return wrapAPIOperation("acknowledge inbox atomically", err)
		}
		if err := s.publishAckEffect(ctx, agent, before, after); err != nil {
			return wrapAPIOperation("publish inbox acknowledgement effect", err)
		}
		return nil
	}
	if err := s.ackWithWake(ctx, agent, write); err != nil {
		return wrapAPIOperation("acknowledge inbox with coverage observation", err)
	}
	return nil
}

// NewInboxServiceWithWakeAcks injects acknowledgement publication for a host.
func NewInboxServiceWithWakeAcks(st InboxAckStore, reader InboxReadStore, publisher WakeAckPublisher) *InboxService {
	counter, ok := reader.(InboxCountStore)
	if !ok {
		counter, _ = st.(InboxCountStore)
	}
	return &InboxService{store: st, reader: reader, counter: counter, wakeAcks: publisher}
}

// InboxAckThrough explicitly acknowledges the cumulative prefix (FR-19.5).
func (s *InboxService) InboxAckThrough(ctx context.Context, agent string, seq int64) error {
	backend, ok := s.store.(interface {
		InboxAckThrough(context.Context, string, int64) error
	})
	if !ok {
		return wrapAPIOperation("ack cumulative inbox", model.ErrInvalidInput)
	}
	err := s.acknowledgeWithWake(ctx, agent, seq, true, func() error { return backend.InboxAckThrough(ctx, agent, seq) })
	if err != nil {
		return wrapAPIOperation(fmt.Sprintf("ack inbox through %d for %s", seq, agent), err)
	}
	return nil
}

func (s *InboxService) ackWithWake(ctx context.Context, agent string, write func() error) error {
	coverage, ok := s.store.(inboxAckCoverage)
	if !ok || s.wakeAcks == nil {
		if err := write(); err != nil {
			return wrapAPIOperation("write inbox acknowledgement without coverage", err)
		}
		return nil
	}
	before, err := coverage.WakeAckMark(ctx, agent)
	if err != nil {
		return fmt.Errorf("observe inbox before acknowledgement: %w", err)
	}
	// No protected accounting lock spans the store's synchronous callbacks.
	if writeErr := write(); writeErr != nil {
		return wrapAPIOperation("write inbox acknowledgement", writeErr)
	}
	after, err := coverage.WakeAckMark(ctx, agent)
	if err != nil {
		return fmt.Errorf("observe inbox after acknowledgement: %w", err)
	}
	if err := s.publishAckEffect(ctx, agent, before, after); err != nil {
		return wrapAPIOperation("publish inbox acknowledgement effect", err)
	}
	return nil
}

func (s *InboxService) publishAckEffect(ctx context.Context, agent string, before, after int64) error {
	if after < before {
		return fmt.Errorf("acknowledgement coverage went backwards: %w", model.ErrConflict)
	}
	if after == before {
		return nil
	}
	if err := s.wakeAcks.Observe(ctx, agent, before, after); err != nil {
		return fmt.Errorf("publish effective inbox acknowledgement: %w", err)
	}
	return nil
}
