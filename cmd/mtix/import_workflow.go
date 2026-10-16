// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// checkWorkflowFlags rejects --prefer, --theirs and --ours on a replace
// import, which takes the file whole and has no conflicts to settle, and an
// unknown --prefer value (MTIX-95.31.13).
func checkWorkflowFlags(f importFlags) error {
	if f.prefer != "" && f.prefer != string(sqlite.WorkflowTheirs) && f.prefer != string(sqlite.WorkflowOurs) {
		return fmt.Errorf("--prefer must be theirs or ours, not %q: %w", f.prefer, model.ErrInvalidInput)
	}
	if f.mode == "replace" && (f.prefer != "" || len(f.theirs) > 0 || len(f.ours) > 0) {
		return fmt.Errorf("--prefer, --theirs and --ours apply to --mode merge only; "+
			"a replace takes every value of the file: %w", model.ErrInvalidInput)
	}
	return nil
}

// trimIDs returns ids without surrounding spaces or empty entries.
func trimIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// importModeFor validates the workflow flags (nothing is written when they
// are invalid) and returns the import mode; a replace is refused on a store
// that syncs with a hub (MTIX-107.31), then passes the MTIX-90 gate
// (MTIX-95.31.13).
func importModeFor(ctx context.Context, exportData *sqlite.ExportData, f importFlags) (sqlite.ImportMode, error) {
	if flagErr := checkWorkflowFlags(f); flagErr != nil {
		return "", nothingWritten(flagErr)
	}
	if f.mode != "replace" {
		return sqlite.ImportModeMerge, nil
	}
	if syncErr := refuseSyncedReplace(ctx); syncErr != nil {
		return "", nothingWritten(syncErr)
	}
	if guardErr := guardImportReplace(ctx, exportData); guardErr != nil {
		return "", guardErr
	}
	return sqlite.ImportModeReplace, nil
}

// refuseSyncedReplace refuses a replace import into a store that holds
// tasks and syncs with a hub or relay (MTIX-107.31): it has exchanged
// events or carries the hub-connected mark, a hub is configured, or local
// config shows one. A replace into an empty store, such as a restore into
// a fresh data directory, deletes nothing and runs as before. A
// replace deletes every task and value the file lacks while their events
// stay in the local sync log, which a pull then acknowledges instead of
// applying, so the board would stay behind the hub. It runs before the
// typed confirmation, so nobody is asked to confirm a delete that is
// refused.
func refuseSyncedReplace(ctx context.Context) error {
	nodes, err := countRows(ctx, "nodes")
	if err != nil || nodes == 0 {
		return err
	}
	synced, err := app.store.HasSyncedWithHub(ctx)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	if !synced && !hubConfigured(app.mtixDir) && !hubMarkConnected() {
		return nil
	}
	return fmt.Errorf("import failed: this store syncs with a hub, and a replace import would delete tasks and "+
		"changes whose events stay in the local sync log, which a pull cannot restore; nothing was imported. "+
		"To rebuild the board from the hub, run %s. To load the file as it is, import it into a project that "+
		"does not sync: %w", sqlite.SyncLogRecovery, sqlite.ErrReplaceOnSyncedStore)
}

// workflowResolution returns the caller's answer to workflow conflicts from
// the --prefer, --theirs and --ours flags (MTIX-95.31.13).
func workflowResolution(f importFlags) sqlite.WorkflowResolution {
	return sqlite.WorkflowResolution{
		Prefer: sqlite.WorkflowChoice(f.prefer), Theirs: trimIDs(f.theirs), Ours: trimIDs(f.ours),
	}
}
