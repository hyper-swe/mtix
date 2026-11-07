// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// queueCapCheckName is the doctor check for the pending-event cap
// (MTIX-112.17.16.16).
const queueCapCheckName = "sync queue cap"

// appendQueueCapCheck adds the "sync queue cap" check: it shows the pending
// events against sync.max_queue_size (0 is unlimited) and WARNs, with the
// exact fix, when the queue is at the cap, where the next write that journals
// an event is refused. It reads the local store only.
func appendQueueCapCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if _, invalid := configuredQueueCap(); invalid != nil {
		return appendDoctorCheck(r, DoctorCheck{Name: queueCapCheckName, Pass: true, Warn: true,
			Detail: "the configured cap is refused and the queue is unlimited: " + invalid.Error(),
			Fix:    "mtix config set sync.max_queue_size <number> (0 is unlimited)"})
	}
	pending, limit, err := st.QueueDepth(ctx)
	if err != nil {
		return appendCheck(r, queueCapCheckName, false, "could not read the queue: "+err.Error())
	}
	if limit == 0 {
		return appendCheck(r, queueCapCheckName, true, fmt.Sprintf("%d pending, no cap (sync.max_queue_size 0)", pending))
	}
	detail := fmt.Sprintf("%d pending of a cap of %d (sync.max_queue_size)", pending, limit)
	if pending < limit {
		return appendCheck(r, queueCapCheckName, true, detail)
	}
	fix := fmt.Sprintf("mtix config set sync.max_queue_size %d (0 is unlimited), or run mtix sync push when a hub is configured "+
		"(on a relay-only board the events stay pending, so raise the cap)", pending*2)
	return appendDoctorCheck(r, DoctorCheck{Name: queueCapCheckName, Pass: true, Warn: true,
		Detail: detail + ": the next write that journals an event is refused", Fix: fix})
}

// configuredQueueCap reads the configured cap and the reason it was refused.
func configuredQueueCap() (int64, error) {
	if app.configSvc == nil {
		return 0, nil
	}
	return app.configSvc.MaxQueueSizeChecked()
}
