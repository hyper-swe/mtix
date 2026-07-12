// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package channel

import (
	"context"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// InboxReader is the store slice the source needs — satisfied by
// *sqlite.Store, narrow so tests can fake it.
type InboxReader interface {
	InboxWait(ctx context.Context, agentID string, timeout time.Duration) ([]sqlite.InboxEvent, error)
}

// InboxSource adapts an agent's inbox into a channel Source. It rides the
// same long-poll primitive as `mtix inbox --wait`, then filters to events not
// yet yielded by this instance (highWater): the inbox intentionally resurfaces
// unacked events on every read, but a session should be pushed each event
// once — if the agent didn't ack it, re-pushing the same text every few
// seconds is noise, not delivery. A NEW source instance (fresh session)
// starts at zero and pushes the outstanding backlog once.
type InboxSource struct {
	store     InboxReader
	agent     string
	highWater int64
}

// NewInboxSource creates a source over agent's inbox.
func NewInboxSource(store InboxReader, agent string) *InboxSource {
	return &InboxSource{store: store, agent: agent}
}

// Next implements Source.
func (s *InboxSource) Next(ctx context.Context, wait time.Duration) ([]Event, error) {
	events, err := s.store.InboxWait(ctx, s.agent, wait)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range events {
		if e.Seq <= s.highWater {
			continue
		}
		s.highWater = e.Seq
		out = append(out, Event{Seq: e.Seq, Node: e.NodeID, From: e.Author, Body: e.Body})
	}
	return out, nil
}
