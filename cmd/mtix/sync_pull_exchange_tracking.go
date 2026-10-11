// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Each pull accounts for encountered IDs before any filtering or fetch.
package main

import "github.com/hyper-swe/mtix/internal/model"

type pullExchangeTracker struct {
	ids       map[string]bool
	remaining int64
	reason    string
	drained   bool
}

func newPullExchangeTracker(capBytes int64) *pullExchangeTracker {
	t := &pullExchangeTracker{ids: map[string]bool{}, remaining: capBytes}
	if capBytes <= 0 {
		t.reason = "invalid-cap"
	}
	return t
}

func (t *pullExchangeTracker) observe(ids []string) {
	if t == nil || t.reason != "" {
		return
	}
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			t.reason = "unknown-event"
			return
		}
		if t.ids[id] {
			continue
		}
		cost := int64(len(id)) + 64
		if cost > t.remaining {
			t.reason = "tracking-limit"
			return
		}
		t.remaining -= cost
		t.ids[id] = true
	}
}

func (t *pullExchangeTracker) events(events []*model.SyncEvent) {
	if t == nil {
		return
	}
	for _, event := range events {
		if event == nil {
			t.reason = "unknown-event"
			return
		}
		t.observe([]string{event.EventID})
	}
}

func (t *pullExchangeTracker) cursorPage(count int, more bool) {
	if t == nil {
		return
	}
	if count == 0 && more {
		t.reason = "partial-pull"
		return
	}
	if !more {
		t.drained = true
	}
}

func (t *pullExchangeTracker) encountered() []string {
	ids := make([]string, 0, len(t.ids))
	for id := range t.ids {
		ids = append(ids, id)
	}
	return ids
}
