// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refusalMergeSynced is the kind of the refusal of an automatic import that
// changes tasks the store already holds, on a store that syncs.
const refusalMergeSynced = "merge_synced"

// refuseSyncedUpdates refuses an automatic import whose file changes a task
// the store also holds, when the store syncs with a hub or relay
// (MTIX-112.17.16.16). An import writes no sync journal, so a later hub edit
// to such a value would silently overwrite the imported one. A file that only
// adds tasks is not refused here, and a store that does not sync is not
// refused. transient is the hub evidence noteHubEvidence found. It returns
// nil when the import may go on, or ErrAutoImportRefused after it printed the
// refusal once per process and recorded it (when the record does not already
// hold the same refusal) for mtix sync and AutoExport, which then leaves the
// pulled tasks.json alone. Nothing else is changed.
func (s *SyncService) refuseSyncedUpdates(ctx context.Context, mtixDir, fileHash string, diff *sqlite.ReplaceDiff, transient bool) error {
	if len(diff.Updated) == 0 {
		return nil
	}
	synced, err := s.storeSyncs(ctx)
	if err != nil {
		return err
	}
	if !synced && !transient {
		return nil
	}
	ids := sqlite.NameNodes(diff.Updated)
	reason := "the file changes " + ids + ", this store syncs with a hub, and an import writes no sync journal, " +
		"so a later hub edit would silently overwrite the imported values"
	s.noticeOnce("merge-synced:"+fileHash, syncedUpdatesMessage(filepath.Dir(mtixDir), ids))
	if prior := s.readRefusal(mtixDir); prior == nil || prior.FileHash != fileHash || prior.Kind != refusalMergeSynced {
		s.recordRefusalOf(mtixDir, &AutoImportRefusal{FileHash: fileHash, Kind: refusalMergeSynced, Reason: reason, Synced: true})
		s.logger.Info("sync_import_refused", "event", "sync_import_refused", "kind", refusalMergeSynced,
			"file_hash", fileHash, "nodes_updated", len(diff.Updated))
	}
	return fmt.Errorf("%w: %s", ErrAutoImportRefused, reason)
}

// syncedUpdatesMessage renders the refusal the user sees, run from the
// project root.
func syncedUpdatesMessage(projectRoot, ids string) string {
	return "mtix: auto-import of .mtix/tasks.json refused: the file changes " + ids + ", this store syncs with a hub, " +
		"and an import writes no sync journal, so a later hub edit would silently overwrite the imported values.\n" +
		"Nothing was imported. Until you choose, mtix refuses again on every command, and writing commands save to " +
		"the local store but leave .mtix/tasks.json as it is.\n" +
		"Choose one, run from the project root (" + projectRoot + "):\n" +
		"  mtix sync pull    take the hub's board, then re-make the file's edits with mtix commands, which are journaled\n" +
		"  mtix sync --fix   keep the local store and rewrite .mtix/tasks.json from it; every change in the file is dropped\n" +
		"mtix sync shows this refusal.\n"
}
