// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fetched responses qualify only against their exact requested batch.
package main

import "github.com/hyper-swe/mtix/internal/model"

func (t *pullExchangeTracker) qualifyFetched(requested []string, events []*model.SyncEvent) {
	if t == nil || t.reason != "" {
		return
	}
	t.observe(requested)
	if t.reason != "" {
		return
	}
	wanted := make(map[string]bool, len(requested))
	for _, id := range requested {
		wanted[id] = true
	}
	seen := make(map[string]bool, len(requested))
	for _, event := range events {
		if event == nil || !wanted[event.EventID] || seen[event.EventID] {
			t.reason = "unknown-event"
			return
		}
		seen[event.EventID] = true
	}
	if len(seen) != len(wanted) {
		t.reason = "unknown-event"
	}
}
