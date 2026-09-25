// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// readQuarantineStatus fills st.QuarantinedEvents for `mtix sync status`:
// how many pulled events are held in the local quarantine, not applied
// (sync_quarantine, MTIX-95.11). Every pull retries them.
func readQuarantineStatus(ctx context.Context, store *sqlite.Store, st *SyncStatus) error {
	n, err := store.CountQuarantined(ctx)
	if err != nil {
		return err
	}
	st.QuarantinedEvents = n
	return nil
}
