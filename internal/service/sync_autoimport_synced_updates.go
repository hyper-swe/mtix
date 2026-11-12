// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refusalMergeSynced is the kind of the refusal of an automatic import that
// changes tasks the store already holds, on a store that syncs.
const refusalMergeSynced = "merge_synced"

// heldChanges names what an automatic import would change on tasks the store
// already holds: the tasks whose content differs, and the dependencies the
// file adds that have an end the file does not add as a new task. A
// dependency between tasks the file itself adds comes with those tasks. It
// returns "" when the file changes no held task (MTIX-112.17.16.16).
func heldChanges(diff *sqlite.ReplaceDiff) string {
	var parts []string
	if len(diff.Updated) > 0 {
		parts = append(parts, sqlite.NameNodes(diff.Updated))
	}
	added := make(map[string]bool, len(diff.Added))
	for _, id := range diff.Added {
		added[id] = true
	}
	var links []string
	for _, dep := range diff.DepsAdded { // "<from> <type> <to>"
		ends := strings.Fields(dep)
		if len(ends) < 2 {
			links = append(links, dep) // unreadable: treat as a held change
			continue
		}
		if !added[ends[0]] || !added[ends[len(ends)-1]] {
			links = append(links, dep)
		}
	}
	if len(links) > 0 {
		parts = append(parts, "dependencies "+sqlite.NameNodes(links))
	}
	return strings.Join(parts, " and ")
}

// refuseSyncedUpdates refuses an automatic import whose file changes a task
// the store also holds, or adds a dependency with an end the store holds,
// when the store syncs with a hub or relay (MTIX-112.17.16.16). An import
// writes no sync journal, so a later hub edit to such a value would silently
// overwrite the imported one, and a peer would never hear of an added link. A
// file that only adds tasks (and links between them) is not refused here, and
// a store that does not sync is not refused. transient is the hub evidence
// noteHubEvidence found. It returns nil when the import may go on, or
// ErrAutoImportRefused after it printed the refusal once per process and
// recorded it (when the record does not already hold the same refusal) for
// mtix sync and AutoExport, which then leaves the pulled tasks.json alone.
// Nothing else is changed.
func (s *SyncService) refuseSyncedUpdates(ctx context.Context, mtixDir, fileHash string, diff *sqlite.ReplaceDiff, transient bool) error {
	changes := heldChanges(diff)
	if changes == "" {
		return nil
	}
	synced, err := s.storeSyncs(ctx)
	if err != nil {
		return err
	}
	if !synced && !transient {
		return nil
	}
	reason := "the file changes " + changes + ", this store syncs with a hub, and an import writes no sync journal, " +
		"so a later hub edit would silently overwrite the imported values and a peer would never hear of an added link"
	s.noticeOnce("merge-synced:"+fileHash, syncedUpdatesMessage(filepath.Dir(mtixDir), changes))
	if prior := s.readRefusal(mtixDir); prior == nil || prior.FileHash != fileHash || prior.Kind != refusalMergeSynced {
		s.recordRefusalOf(mtixDir, &AutoImportRefusal{FileHash: fileHash, Kind: refusalMergeSynced, Reason: reason, Synced: true})
		s.logger.Info("sync_import_refused", "event", "sync_import_refused", "kind", refusalMergeSynced,
			"file_hash", fileHash, "nodes_updated", len(diff.Updated), "dependencies_added", len(diff.DepsAdded))
	}
	return fmt.Errorf("%w: %s", ErrAutoImportRefused, reason)
}

// syncedUpdatesWays is the advice that ends a merge_synced refusal, in the
// refusal's printed text and in the pending line mtix sync shows. A pull
// alone does not end it: it changes the store, the file then differs from
// the store and from the last sync, and the next command refuses again as a
// conflict. mtix sync --fix rewrites the file from the store, so it is the
// step that ends the refusal, after a pull when the hub's board is wanted.
const syncedUpdatesWays = "mtix sync --fix: keep the local store and rewrite .mtix/tasks.json from it; the file's changes " +
	"are dropped, after the refused file is kept at " + refusedBoardCopyHint + ". To take the hub's board as well, " +
	"run mtix sync pull first and then mtix sync --fix: a pull alone does not end this refusal. Re-make the file's edits from that copy with mtix commands, which are journaled"

// syncedUpdatesMessage renders the refusal the user sees, run from the
// project root.
func syncedUpdatesMessage(projectRoot, changes string) string {
	return "mtix: auto-import of .mtix/tasks.json refused: the file changes " + changes + ", this store syncs with a hub, " +
		"and an import writes no sync journal, so a later hub edit would silently overwrite the imported values " +
		"and a peer would never hear of an added link.\n" +
		"Nothing was imported. Until you choose, mtix refuses again on every command, and writing commands save to " +
		"the local store but leave .mtix/tasks.json as it is.\n" +
		"Run from the project root (" + projectRoot + "):\n" +
		"  mtix sync --fix    keep the local store and rewrite .mtix/tasks.json from it; every change in the file is dropped,\n" +
		"                     after the refused file is kept at " + refusedBoardCopyHint + "\n" +
		"  mtix sync pull     to take the hub's board as well, run this first and then mtix sync --fix: " +
		"a pull alone does not end this refusal\n" +
		"Then re-make the file's edits from that copy with mtix commands, which are journaled.\n" +
		"mtix sync shows this refusal.\n"
}
