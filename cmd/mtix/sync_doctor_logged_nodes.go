// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// loggedNodesCheckName is the name of the doctor's check that the board
// holds every task the local sync log creates (MTIX-107.31).
const loggedNodesCheckName = "board matches sync log"

// loggedNodesFix rebuilds the board and the sync log from the hub: the
// steps run one at a time, because reconcile drops what is not pushed yet,
// so mtix sync status must show pending 0 before the discard.
const loggedNodesFix = sqlite.SyncLogRecovery

// appendLoggedNodesCheck adds the doctor's "board matches sync log" check
// (MTIX-107.31): on a store that syncs with a hub it fails while the sync
// log creates a task the board lacks, with no delete event for it, as a
// replace import before 0.6 could leave. A pull cannot repair that, because
// it acknowledges the held events instead of applying them, so the fix
// rebuilds the board from the hub. Local only.
func appendLoggedNodesCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, loggedNodesCheckName, false, "local store not initialized")
	}
	synced, err := st.HasSyncedWithHub(ctx)
	if err != nil {
		return appendCheck(r, loggedNodesCheckName, false, err.Error())
	}
	if !synced && !hubConfigured(app.mtixDir) && !hubMarkConnected() {
		return appendCheck(r, loggedNodesCheckName, true, "ok (the store does not sync with a hub)")
	}
	missing, err := st.MissingLoggedNodes(ctx)
	if err != nil {
		return appendCheck(r, loggedNodesCheckName, false, err.Error())
	}
	if len(missing) == 0 {
		return appendCheck(r, loggedNodesCheckName, true, "ok")
	}
	return appendDoctorCheck(r, DoctorCheck{Name: loggedNodesCheckName, Detail: fmt.Sprintf(
		"the board lacks %d of the tasks the local sync log creates: %s; a pull cannot restore them",
		len(missing), sqlite.NameNodes(missing)), Fix: loggedNodesFix})
}
