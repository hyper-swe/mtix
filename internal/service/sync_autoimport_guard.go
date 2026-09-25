// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ErrAutoImportRefused marks an automatic import of a changed
// .mtix/tasks.json that mtix refused because the replace import would
// delete local node or dependency data the file lacks (FR-15.2i,
// MTIX-95.31.2). Nothing was imported and nothing changed. AutoImport has
// already printed the refusal to the notice writer, so a caller must not
// print it again.
var ErrAutoImportRefused = errors.New("auto-import refused")

// maxLossLines caps how many nodes a refusal lists; the rest are counted.
const maxLossLines = 10

// The causes a refusal message names (refusalMessage): a replace that
// would delete local data (FR-15.2i), and a conflict whose replace would
// too (FR-15.2h, MTIX-95.31.4).
const (
	lossyCause    = "a replace import of the changed file would delete local data the file lacks"
	conflictCause = "both the local store and the file changed since the last sync, " +
		"and a replace import of the file would delete local data it lacks"
)

// AutoSyncConfig is the source of the sync.auto_sync switch (FR-15.2j,
// MTIX-95.31.2); ConfigService implements it.
type AutoSyncConfig interface {
	// AutoSyncSetting returns the configured value, whether automatic
	// import is on, and an error when the value is not true or false, in
	// which case automatic import is on, the default.
	AutoSyncSetting() (raw string, on bool, err error)
}

// SetAutoSyncConfig wires the sync.auto_sync switch that AutoImport reads
// on every call (FR-15.2j, MTIX-95.31.2). Without it, auto-import is on,
// the switch's default.
func (s *SyncService) SetAutoSyncConfig(cfg AutoSyncConfig) {
	s.autoSync = cfg
}

// SetNoticeWriter sets where AutoImport and AutoExport print their notices
// and refusals (MTIX-95.31.2); the default is stderr. nil discards them.
func (s *SyncService) SetNoticeWriter(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	s.notices = w
}

// AutoImportEnabled reports whether sync.auto_sync leaves automatic import
// on (FR-15.2j, MTIX-95.31.2): the value is true, no switch is wired, or the
// value is neither true nor false, in which case the default (on) applies
// and a one-line warning is printed once per process.
func (s *SyncService) AutoImportEnabled() bool {
	if s.autoSync == nil {
		return true
	}
	raw, on, err := s.autoSync.AutoSyncSetting()
	if err != nil {
		s.noticeOnce("auto_sync:"+raw, fmt.Sprintf("mtix: sync.auto_sync is %q, which is neither true nor false, "+
			"so automatic import stays on (the default); set it with: mtix config set sync.auto_sync true|false\n", raw))
		return true
	}
	return on
}

// autoImportSwitchedOff reports whether sync.auto_sync keeps this
// automatic import from running: the switch is off and the local store
// holds at least one node. A store without nodes (a fresh clone) is always
// imported, whatever the switch says: nothing in it can be lost (FR-15.2a,
// MTIX-95.31.2).
func (s *SyncService) autoImportSwitchedOff(ctx context.Context) bool {
	if s.AutoImportEnabled() {
		return false
	}
	var holdsNodes bool
	// Does the local store hold any node, soft-deleted ones included?
	if err := s.store.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes)`).Scan(&holdsNodes); err != nil {
		s.logger.Warn("sync.auto_sync is false and the store could not be read, skipping auto-import", "error", err)
		return true
	}
	if holdsNodes {
		s.logger.Debug("sync.auto_sync is false, skipping auto-import")
	}
	return holdsNodes
}

// noticeOnce prints msg on the notice writer the first time key is seen by
// this service (one process in the CLI), so a repeated condition is
// reported once.
func (s *SyncService) noticeOnce(key, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noticed[key] {
		return
	}
	if s.noticed == nil {
		s.noticed = make(map[string]bool)
	}
	s.noticed[key] = true
	fmt.Fprint(s.notices, msg)
}

// refuseLossyImport handles a replace auto-import that would delete local
// data the file lacks (FR-15.2i, MTIX-95.31.2): it prints the refusal once,
// naming what would be lost and the commands that proceed deliberately,
// records it for mtix sync and for AutoExport, which then leaves the
// refused tasks.json alone, and returns ErrAutoImportRefused. It changes
// nothing else: no import, no backup, and the stored hash stays, so the
// refusal repeats on every command until the user chooses.
func (s *SyncService) refuseLossyImport(mtixDir, fileHash string, diff *sqlite.ReplaceDiff) error {
	loss := lossList(diff.Losses, "; ")
	reason := "a replace import would delete local data the file lacks: " + loss
	fmt.Fprint(s.notices, refusalMessage(filepath.Dir(mtixDir), lossyCause, diff.Losses))
	s.recordLossyRefusal(mtixDir, fileHash, refusalLossy, reason, loss)
	s.logger.Info("sync_import_refused", "event", "sync_import_refused",
		"file_hash", fileHash, "nodes_losing_data", len(diff.Losses))
	return fmt.Errorf("%w: %s", ErrAutoImportRefused, reason)
}

// refuseConflict handles a changed tasks.json when the local store changed
// too since the last sync (FR-15.2h): nothing is imported, and the conflict
// is recorded for mtix sync and for AutoExport, which then keeps the file.
// MTIX-95.31.4: when a replace of the file would also delete local data,
// the conflict keeps that loss list: it is printed as a refusal (the replace
// option says it deletes the data listed), recorded in the reason and as
// the loss the pending line's replace option names (resolutionFor), and
// ErrAutoImportRefused is returned. A conflict that loses nothing is logged
// and returns nil.
func (s *SyncService) refuseConflict(mtixDir, fileHash string, diff *sqlite.ReplaceDiff) error {
	reason := "both the local store and tasks.json changed since the last sync (FR-15.2h)"
	if !diff.Lossy() {
		s.logger.Warn("conflict detected: both tasks.json and local database changed since last sync",
			"resolution", "combine them with 'mtix import .mtix/tasks.json --mode merge', keep the file with "+
				"'mtix import .mtix/tasks.json --mode replace', or keep the local store with 'mtix sync --fix'")
		s.recordRefusal(mtixDir, fileHash, refusalConflict, reason)
		return nil
	}
	loss := lossList(diff.Losses, "; ")
	reason += ", and a replace import would also delete local data the file lacks: " + loss
	fmt.Fprint(s.notices, refusalMessage(filepath.Dir(mtixDir), conflictCause, diff.Losses))
	s.recordLossyRefusal(mtixDir, fileHash, refusalConflict, reason, loss)
	s.logger.Info("sync_import_refused", "event", "sync_import_refused", "kind", refusalConflict,
		"file_hash", fileHash, "nodes_losing_data", len(diff.Losses))
	return fmt.Errorf("%w: %s", ErrAutoImportRefused, reason)
}

// refusalMessage renders the refusal the user sees: its cause, what would
// be lost, node by node, and the three deliberate ways to proceed, run from
// the project root, with what each of them keeps and loses. When a local
// task is a different task than the file's under its id, the merge option
// says the merge renumbers it and needs --confirm (MTIX-95.31.4). When the
// file holds a task under another uid assigned at upgrade, with another
// title, the merge option says the merge takes the file's title and content
// for it, names every such task, and says how to keep the local one (copy
// it with mtix create first), so the user can refuse the merge
// (MTIX-95.31.6). When the file holds a local task's id with another title
// and either has no uid to compare, the merge option says the merge takes
// them for different tasks and renumbers the local one with --confirm, and
// what to do when they are one task retitled (MTIX-95.31.9).
func refusalMessage(projectRoot, cause string, losses []sqlite.NodeLoss) string {
	upgradeMatched := sameTaskAtUpgrade(losses) // MTIX-95.31.6
	keeps := "keeps every value the refusal lists"
	if len(upgradeMatched) > 0 {
		keeps += " (a task treated as the same task takes the file's title and content, see below)"
	}
	var b strings.Builder
	b.WriteString("mtix: auto-import of .mtix/tasks.json refused: " + cause + ":\n")
	b.WriteString("  " + lossList(losses, "\n  ") + "\n")
	b.WriteString("Nothing was imported. Until you choose, mtix refuses again on every command, and writing " +
		"commands save to the local store but leave .mtix/tasks.json as it is.\n")
	fmt.Fprintf(&b, "Choose one, run from the project root (%s):\n", projectRoot)
	b.WriteString("  mtix import .mtix/tasks.json --mode merge    backs up the database, " + keeps +
		" and adds the file's changes: a field value listed above stays, with the task's local status when " +
		"the value is part of it; for a task whose content is unchanged, all local field values win, so a " +
		"teammate's change to its status or assignee is not applied and your next export reverts it\n")
	if holdsDifferentTask(losses) {
		b.WriteString("                                               a local task listed as a different task " +
			"under its id is renumbered to the next number free in both the store and the file (it keeps its " +
			"uid; the file's task keeps the id): the import lists the renumbering and applies it only when you " +
			"rerun it with --confirm\n")
	}
	if len(upgradeMatched) > 0 {
		b.WriteString(sameTaskAtUpgradeNote(upgradeMatched))
	}
	if ids := differentTitleNoUID(losses); len(ids) > 0 { // MTIX-95.31.9
		b.WriteString(differentTitleNoUIDNote(ids))
	}
	b.WriteString("  mtix sync --fix                              keep the local store and rewrite " +
		".mtix/tasks.json from it; every change in the file is dropped\n")
	b.WriteString("  mtix import .mtix/tasks.json --mode replace  make the file win and delete the local " +
		"data listed above (take a copy first: mtix backup <file>)\n")
	b.WriteString("mtix sync shows this refusal.\n")
	return b.String()
}

// holdsDifferentTask reports whether any loss is a local task that the file
// replaces with a different task under its id (MTIX-95.31.4).
func holdsDifferentTask(losses []sqlite.NodeLoss) bool {
	for i := range losses {
		if losses[i].DifferentTask {
			return true
		}
	}
	return false
}

// sameTaskAtUpgrade returns the ids of the local tasks the file holds under
// another uid assigned at upgrade, with another title (MTIX-95.31.6), every
// one of them, even those lossList counts without naming.
func sameTaskAtUpgrade(losses []sqlite.NodeLoss) []string {
	var ids []string
	for i := range losses {
		if losses[i].SameTaskAtUpgrade {
			ids = append(ids, losses[i].NodeID)
		}
	}
	return ids
}

// sameTaskAtUpgradeNote is the merge option's note on the tasks ids that
// the file holds under another uid assigned at upgrade, with another title
// (MTIX-95.31.6): the merge takes the file's uid, title and content for
// them, and a local task that is a different task is kept by copying it to
// a new task first.
func sameTaskAtUpgradeNote(ids []string) string {
	return "                                               a task treated as the same task (uid assigned at " +
		"upgrade), here " + strings.Join(ids, ", ") + ", keeps its id and takes the file's uid, title and " +
		"content, and the import lists each uid it adopts; if the two titles name different tasks (tasks " +
		"created in the same second whose create event at least one clone's event log lacks, for example " +
		"created before 0.2), yours would survive only in the backup the merge takes: copy it to a new task first (mtix " +
		"show <id>, then mtix create with its title and description; writes stay local while this refusal is " +
		"pending), then merge\n"
}

// differentTitleNoUID returns the ids of the local tasks the file holds
// under their id with another title while either has no uid to compare
// (MTIX-95.31.9), every one of them, even those lossList counts without
// naming.
func differentTitleNoUID(losses []sqlite.NodeLoss) []string {
	var ids []string
	for i := range losses {
		if losses[i].DifferentTitleNoUID {
			ids = append(ids, losses[i].NodeID)
		}
	}
	return ids
}

// differentTitleNoUIDNote is the merge option's note on the local tasks
// ids that the file holds under their id with another title and no uid to
// compare (MTIX-95.31.9): the merge takes them for different tasks, and a
// task a teammate only retitled on a board without uids ends up twice, the
// renumbered copy holding the local comments, activity and field values.
func differentTitleNoUIDNote(ids []string) string {
	return "                                               a local task listed with a different title and no uid " +
		"to compare is taken for a different task (here " + strings.Join(ids, ", ") + "): the merge renumbers " +
		"it, with its subtree, to the next number free in both the store and the file (the file's task keeps " +
		"the id), and applies that only when you rerun it with --confirm; if it is the same task, retitled on " +
		"a board without uids, the merge keeps both copies, and the renumbered one holds your comments, activity " +
		"and field values the board lacked: move them to the task at the id, or keep the renumbered copy, before " +
		"you delete anything\n"
}

// lossList describes up to maxLossLines nodes' losses joined by sep, and
// counts the rest.
func lossList(losses []sqlite.NodeLoss, sep string) string {
	parts := make([]string, 0, min(len(losses), maxLossLines)+1)
	for i := range losses {
		if i == maxLossLines {
			parts = append(parts, fmt.Sprintf("and %d more nodes", len(losses)-maxLossLines))
			break
		}
		parts = append(parts, describeLoss(&losses[i]))
	}
	return strings.Join(parts, sep)
}

// describeLoss renders one node's loss, for example
// "PROJ-1: 2 annotations (01J..., 01J...), 1 activity entry, field assignee",
// or, for a different task under the id (MTIX-95.31.4),
// `PROJ-3: a different task under this id (local "Mine", file "Theirs")`.
// A task the file holds with another title and no uid to compare starts
// with `a task under this id with a different title and no uid to compare
// (local "Mine", file "Theirs")` (MTIX-95.31.9).
// A task the file holds under another uid assigned at upgrade, with another
// title, starts with `treated as the same task (uid assigned at upgrade)
// (local "Mine", file "Theirs")` (MTIX-95.31.6).
func describeLoss(l *sqlite.NodeLoss) string {
	if l.DifferentTask {
		return fmt.Sprintf("%s: a different task under this id (local %q, file %q)", l.NodeID, l.LocalTitle, l.FileTitle)
	}
	if l.WholeNode {
		if l.SoftDeleted {
			return l.NodeID + ": the whole node (soft-deleted locally)"
		}
		return l.NodeID + ": the whole node"
	}
	var parts []string
	if l.DifferentTitleNoUID { // MTIX-95.31.9
		parts = append(parts, fmt.Sprintf("a task under this id with a different title and no uid to compare "+
			"(local %q, file %q)", l.LocalTitle, l.FileTitle))
	}
	if l.SameTaskAtUpgrade {
		parts = append(parts, fmt.Sprintf("treated as the same task (uid assigned at upgrade) (local %q, file %q)",
			l.LocalTitle, l.FileTitle))
	}
	if n := len(l.Annotations); n > 0 {
		parts = append(parts, fmt.Sprintf("%s (%s)", plural(n, "annotation", "annotations"),
			strings.Join(l.Annotations, ", ")))
	}
	if n := len(l.Unresolved); n > 0 {
		parts = append(parts, fmt.Sprintf("%s (%s)",
			plural(n, "annotation resolution", "annotation resolutions"), strings.Join(l.Unresolved, ", ")))
	}
	if l.Activity > 0 {
		parts = append(parts, plural(l.Activity, "activity entry", "activity entries"))
	}
	if n := len(l.Fields); n > 0 {
		label := "fields "
		if n == 1 {
			label = "field "
		}
		parts = append(parts, label+strings.Join(l.Fields, ", "))
	}
	if n := len(l.Dependencies); n > 0 {
		parts = append(parts, fmt.Sprintf("%s (%s)", plural(n, "dependency", "dependencies"),
			strings.Join(l.Dependencies, ", ")))
	}
	return l.NodeID + ": " + strings.Join(parts, ", ")
}

// plural renders n with the singular or plural noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// noticeImported prints the one line that says an auto-import applied a
// changed .mtix/tasks.json, what it changed and where the database before
// it is (FR-15.2k, MTIX-95.31.2).
func (s *SyncService) noticeImported(mtixDir string, diff *sqlite.ReplaceDiff, backupPath string) {
	shown := backupPath
	if rel, err := filepath.Rel(filepath.Dir(mtixDir), backupPath); err == nil {
		shown = rel
	}
	fmt.Fprintf(s.notices,
		"mtix: imported the changed .mtix/tasks.json: nodes %d added, %d updated, %d removed; "+
			"dependencies %d added, %d removed (local database backed up to %s)\n",
		len(diff.Added), len(diff.Updated), len(diff.Removed), len(diff.DepsAdded), len(diff.DepsRemoved), shown)
}
