// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// subscriberBufferSize is the channel buffer size for each subscriber.
// Slow subscribers whose buffer fills are dropped with a warning.
const subscriberBufferSize = 256

// SubscriptionFilter controls which events a subscriber receives.
type SubscriptionFilter struct {
	// Under filters to events whose NodeID starts with this prefix.
	// Empty string means no prefix filtering (receive all).
	Under string `json:"under,omitempty"`

	// Events is a whitelist of event types to receive.
	// Empty slice means receive all event types.
	Events []EventType `json:"events,omitempty"`
}

// matches returns true if the event passes this filter.
func (f *SubscriptionFilter) matches(event Event) bool {
	if f.Under != "" && !strings.HasPrefix(event.NodeID, f.Under) {
		return false
	}
	if len(f.Events) > 0 {
		found := false
		for _, et := range f.Events {
			if et == event.Type {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// subscriber represents a registered event subscriber with its filter.
type subscriber struct {
	ch     chan Event
	filter SubscriptionFilter
}

// Hub is an in-memory EventBroadcaster that fans out events to subscribers.
// Thread-safe via sync.RWMutex. Implements EventBroadcaster.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[chan Event]*subscriber
	logger      *slog.Logger
}

// NewHub creates a new in-memory event broadcasting hub.
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		subscribers: make(map[chan Event]*subscriber),
		logger:      logger,
	}
}

// Broadcast fans out an event to all matching subscribers per FR-7.5a.
// Slow subscribers whose buffer is full are removed with a warning log.
// This method never blocks — non-delivery is logged but not propagated as an error.
func (h *Hub) Broadcast(_ context.Context, event Event) error {
	h.mu.RLock()
	subs := make([]*subscriber, 0, len(h.subscribers))
	for _, sub := range h.subscribers {
		subs = append(subs, sub)
	}
	h.mu.RUnlock()

	var dropped []chan Event
	for _, sub := range subs {
		if !sub.filter.matches(event) {
			continue
		}
		select {
		case sub.ch <- event:
			// Delivered.
		default:
			// Buffer full — subscriber is slow. Mark for removal.
			h.logger.Warn("dropping slow subscriber",
				"event_type", event.Type, "node_id", event.NodeID)
			dropped = append(dropped, sub.ch)
		}
	}

	// Remove slow subscribers.
	if len(dropped) > 0 {
		h.mu.Lock()
		for _, ch := range dropped {
			if sub, ok := h.subscribers[ch]; ok {
				close(sub.ch)
				delete(h.subscribers, ch)
			}
		}
		h.mu.Unlock()
	}

	return nil
}

// Subscribe registers a new subscriber with the given filter.
// Returns a buffered channel that will receive matching events.
// The caller MUST call Unsubscribe when done to prevent resource leaks.
func (h *Hub) Subscribe(filter SubscriptionFilter) <-chan Event {
	ch := make(chan Event, subscriberBufferSize)
	sub := &subscriber{ch: ch, filter: filter}

	h.mu.Lock()
	h.subscribers[ch] = sub
	h.mu.Unlock()

	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
// Safe to call multiple times for the same channel.
func (h *Hub) Unsubscribe(ch <-chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Find the writable channel matching the read-only channel.
	for writeCh, sub := range h.subscribers {
		if sub.ch == ch {
			close(writeCh)
			delete(h.subscribers, writeCh)
			return
		}
	}
}

// SubscriberCount returns the current number of active subscribers.
// Used for monitoring and testing.
func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
