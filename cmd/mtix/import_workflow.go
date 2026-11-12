// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
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
// that syncs with a hub (MTIX-107.31), and so is a merge (MTIX-112.17.16.16);
// a replace then passes the MTIX-90 gate
// (MTIX-95.31.13).
func importModeFor(ctx context.Context, exportData *sqlite.ExportData, f importFlags) (sqlite.ImportMode, error) {
	if flagErr := checkWorkflowFlags(f); flagErr != nil {
		return "", nothingWritten(flagErr)
	}
	if f.mode != "replace" {
		if syncErr := refuseSyncedMerge(ctx); syncErr != nil {
			return "", nothingWritten(syncErr)
		}
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
	if app.syncSvc == nil {
		return errors.New("the sync service is not initialized")
	}
	holds, err := app.syncSvc.StoreHoldsTasks(ctx)
	if err != nil || !holds {
		return err
	}
	synced, err := storeSyncs(ctx)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	counts, err := hubGuardCounts(ctx)
	if err != nil {
		return fmt.Errorf("import failed: %w: %w", err, sqlite.ErrReplaceOnSyncedStore)
	}
	if !synced && !counts {
		return nil
	}
	guide, err := app.syncSvc.InboxSafeRecoveryGuide(ctx, sqlite.SyncLogRecovery)
	if err != nil {
		return err
	}
	return fmt.Errorf("import failed: this store syncs with a hub, and a replace import would delete tasks and "+
		"changes whose events stay in the local sync log, which a pull cannot restore; nothing was imported. "+
		"To rebuild the board from the hub, run %s. To load the file as it is, import it into a project that "+
		"does not sync: %w", guide, sqlite.ErrReplaceOnSyncedStore)
}

// refuseSyncedMerge refuses a merge import into a store that syncs with a
// hub or relay: it has exchanged events or carries the hub-connected mark, a
// hub is configured, or local config shows one. A merge writes no sync
// journal, so a later hub edit to a merged value would silently overwrite it.
// It runs before the workflow checks and any backup, so nothing is written.
func refuseSyncedMerge(ctx context.Context) error {
	synced, err := storeSyncs(ctx)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	counts, err := hubGuardCounts(ctx)
	if err != nil {
		return fmt.Errorf("import failed: %w: %w", err, sqlite.ErrMergeOnSyncedStore)
	}
	if !synced && !counts {
		return nil
	}
	return fmt.Errorf("import failed: this store syncs with a hub, and a merge import writes no sync journal, "+
		"so a later hub edit would silently overwrite the merged values; nothing was imported. %s: %w",
		sqlite.MergeOnSyncedWays, sqlite.ErrMergeOnSyncedStore)
}

// workflowResolution returns the caller's answer to workflow conflicts from
// the --prefer, --theirs and --ours flags (MTIX-95.31.13).
func workflowResolution(f importFlags) sqlite.WorkflowResolution {
	return sqlite.WorkflowResolution{
		Prefer: sqlite.WorkflowChoice(f.prefer), Theirs: trimIDs(f.theirs), Ours: trimIDs(f.ours),
	}
}

// storeSyncs asks the sync service whether the open store syncs with a hub
// or relay, so the import handlers hold no store query of their own
// (MTIX-112.17.16.16). It fails closed when the service is missing.
func storeSyncs(ctx context.Context) (bool, error) {
	if app.syncSvc == nil {
		return false, errors.New("the sync service is not initialized")
	}
	return app.syncSvc.StoreSyncs(ctx)
}
