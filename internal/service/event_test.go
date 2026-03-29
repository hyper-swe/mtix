// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"sync"

	"github.com/hyper-swe/mtix/internal/service"
)

// recordingBroadcaster captures events for test assertions.
type recordingBroadcaster struct {
	mu     sync.Mutex
	events []service.Event
}

func newRecordingBroadcaster() *recordingBroadcaster {
	return &recordingBroadcaster{}
}

func (r *recordingBroadcaster) Broadcast(_ context.Context, event service.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordingBroadcaster) Events() []service.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]service.Event, len(r.events))
	copy(copied, r.events)
	return copied
}

func (r *recordingBroadcaster) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}
