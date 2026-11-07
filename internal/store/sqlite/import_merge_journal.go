// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
)

// An import merge into a store that syncs journals what it changes
// (MTIX-112.17.16.16). The merge wrote nodes with no journal row, so nothing
// reached the hub, and a later pull of a hub edit to the same field replaced
// the imported value with no notice: the pull found no local event for the
// field, so the hub's applied unconditionally (LWW, FR-18.11). The journal
// holds one event per value that really changed, written in the import's own
// transaction, so LWW meets a real local event and the next push carries the
// change. The import contacts no hub.

// mergeControl is what mergeImportNode needs beside the node: the caller's
// choice for a workflow conflict, and the journal to record the node's
// change in (nil when the store does not sync).
type mergeControl struct {
	res     *WorkflowConflict
	journal *mergeJournal
}

// mergeJournal collects which tasks a merge created or changed, to be
// journaled once the merge has written them (journalMerge). It keeps the
// task as the store held it before, never the file's values: the events come
// from comparing that copy with the row the merge stored (principle A, round
// 2), so a value the merge kept local is never journaled. A nil
// *mergeJournal records nothing.
type mergeJournal struct {
	created []string
	changed []string
	before  map[string]exportNode
	deps    []exportDep
	// blocked maps a task the merge auto-blocked to its status before the
	// block: a peer derives the block from the link_dep event, so no
	// transition is journaled for it.
	blocked map[string]string
	// localOnly names, per task, the columns the merge changed that no sync
	// event carries, so the import reports them (principle B).
	localOnly map[string][]string
	// keptUID lists the tasks whose local uid the merge kept against the
	// file's, because the hub knows them by it (keepHubKnownUID).
	keptUID []string
}

// noteLocalOnly records columns of task id that stay local.
func (j *mergeJournal) noteLocalOnly(id string, fields ...string) {
	if len(fields) == 0 {
		return
	}
	if j.localOnly == nil {
		j.localOnly = map[string][]string{}
	}
	j.localOnly[id] = append(j.localOnly[id], fields...)
}

// localOnlyList is the local-only columns as the import result reports them,
// tasks and columns in order.
func (j *mergeJournal) localOnlyList() []LocalOnlyChange {
	var out []LocalOnlyChange
	for id, fields := range j.localOnly {
		sort.Strings(fields)
		out = append(out, LocalOnlyChange{ID: id, Fields: fields})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func (j *mergeJournal) noteCreated(n *exportNode) {
	if j != nil {
		j.created = append(j.created, n.ID)
	}
}

// noteChanged records the task as the store held it before the merge wrote
// it; a task noted twice keeps its first copy.
func (j *mergeJournal) noteChanged(before *exportNode) {
	if j == nil {
		return
	}
	if j.before == nil {
		j.before = map[string]exportNode{}
	}
	if _, held := j.before[before.ID]; !held {
		j.before[before.ID] = *before
		j.changed = append(j.changed, before.ID)
	}
}

// noteDep records a dependency the merge inserted, when the store did not
// hold it already.
func (j *mergeJournal) noteDep(ctx context.Context, tx *sql.Tx, d *exportDep) error {
	if j == nil {
		return nil
	}
	var held int
	// The merge inserts a dependency with INSERT OR IGNORE: only one the
	// store lacks is a change to journal.
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?`,
		d.FromID, d.ToID, d.DepType).Scan(&held); err != nil {
		return fmt.Errorf("check dependency %s->%s: %w", d.FromID, d.ToID, err)
	}
	if held == 0 {
		j.deps = append(j.deps, *d)
	}
	return nil
}

// autoBlockMerged blocks, as a peer does on the link_dep event, the
// dependent of every blocks dependency the merge inserted (FR-3.8), and
// records the status each had before so no transition is journaled for it.
func (s *Store) autoBlockMerged(ctx context.Context, tx *sql.Tx, j *mergeJournal) error {
	for _, d := range j.deps {
		if d.DepType != string(model.DepTypeBlocks) {
			continue
		}
		var was string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`, d.ToID).Scan(&was); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("read status of %s: %w", d.ToID, err)
		}
		if err := autoBlockNode(ctx, tx, s, d.ToID, s.clock); err != nil {
			return fmt.Errorf("auto-block %s for the imported dependency: %w", d.ToID, err)
		}
		if was == string(model.StatusOpen) || was == string(model.StatusInProgress) {
			if j.blocked == nil {
				j.blocked = map[string]string{}
			}
			if _, held := j.blocked[d.ToID]; !held {
				j.blocked[d.ToID] = was
			}
		}
	}
	return nil
}

// reblockMerged keeps blocked a task that was blocked before the merge and
// still has a live blocks dependency, when the merge wrote another status
// over it (a file chosen with --prefer theirs). Without it the repeat of an
// import that blocked the task would unblock it and journal the transition.
// The task is blocked as before, so the stored result equals the held one and
// nothing is journaled (principle A, round 3). It reads the live blockers: a
// blocker that is done, cancelled, invalidated or deleted no longer blocks.
func (s *Store) reblockMerged(ctx context.Context, tx *sql.Tx, j *mergeJournal) error {
	for _, id := range j.changed {
		if j.before[id].Status != string(model.StatusBlocked) {
			continue
		}
		var status string
		// The merged task's status, for a live task only.
		if err := tx.QueryRowContext(ctx, `SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("read status of %s: %w", id, err)
		}
		if status != string(model.StatusOpen) && status != string(model.StatusInProgress) {
			continue
		}
		var live int
		// Blocks dependencies of the task whose blocker is still live and unfinished.
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM dependencies d JOIN nodes f ON f.id = d.from_id
			WHERE d.to_id = ? AND d.dep_type = 'blocks' AND f.deleted_at IS NULL
			AND f.status NOT IN ('done', 'cancelled', 'invalidated')`, id).Scan(&live); err != nil {
			return fmt.Errorf("count live blockers of %s: %w", id, err)
		}
		if live == 0 {
			continue
		}
		if err := autoBlockNode(ctx, tx, s, id, s.clock); err != nil {
			return fmt.Errorf("keep %s blocked: %w", id, err)
		}
	}
	return nil
}

// journalMerge writes the journal rows for what the merge stored, when the
// store syncs (syncedWithHub). It blocks the dependents of the inserted
// blocks links, re-reads every created or changed task from the store, plans
// the events from the stored rows, checks the queue cap once for all of
// them, and writes nothing when the cap would be exceeded: the import's
// transaction rolls back (FR-18, ErrSyncQueueFull). It returns the number of
// events written.
func (s *Store) journalMerge(ctx context.Context, tx *sql.Tx, j *mergeJournal, wallTS int64) (int, error) {
	if err := s.autoBlockMerged(ctx, tx, j); err != nil {
		return 0, err
	}
	if err := s.reblockMerged(ctx, tx, j); err != nil {
		return 0, err
	}
	planned, err := s.planMergeEvents(ctx, tx, j, wallTS)
	if err != nil {
		return 0, err
	}
	if err := checkQueueRoom(ctx, tx, len(planned)); err != nil {
		return 0, err
	}
	ctx = withQueueChecked(ctx)
	for i := range planned {
		if err := emitEvent(ctx, tx, planned[i]); err != nil {
			return 0, fmt.Errorf("journal import change to %s: %w", planned[i].NodeID, err)
		}
	}
	return len(planned), nil
}

// mergeAndJournal runs the merge of an import and, when the store syncs,
// journals what it stored in the same transaction (MTIX-112.17.16.16). A
// store that has not synced is merged as before and journals nothing: a later
// backfill still finds an empty journal.
func (s *Store) mergeAndJournal(ctx context.Context, tx *sql.Tx, data *ExportData, cfg *importConfig) (ImportResult, error) {
	policy := workflowPolicy{resolution: cfg.workflow, now: s.clock()}
	synced, err := storeSyncs(ctx, tx, cfg.asSynced)
	if err != nil {
		return ImportResult{}, err
	}
	unjournaled := false
	if synced && !cfg.noJournal {
		// A journal that is empty beside a store that holds tasks is one a
		// backfill has not filled: events for the changed tasks alone would
		// reach the hub without their creates, and backfill would refuse the
		// store. Journal nothing, as before this change (principle E, round 3).
		if unjournaled, err = journalEmptyBesideTasks(ctx, tx); err != nil {
			return ImportResult{}, err
		}
		if !unjournaled {
			policy.journal = &mergeJournal{}
		}
	}
	result, err := mergeAllData(ctx, tx, data, policy)
	result.NeedsBackfill = unjournaled
	if err != nil || policy.journal == nil {
		return result, err
	}
	n, err := s.journalMerge(ctx, tx, policy.journal, policy.now.UnixMilli())
	result.Journaled, result.JournalLocalOnly = n, policy.journal.localOnlyList()
	result.keptUID = policy.journal.keptUID
	return result, err
}

type queueCheckedKey struct{}

// withQueueChecked marks ctx: the caller checked the queue cap for every
// event it will emit (checkQueueRoom), so emitEvent skips its per-event
// count, which would cost a scan of the queue for each of a large import's
// events.
func withQueueChecked(ctx context.Context) context.Context {
	return context.WithValue(ctx, queueCheckedKey{}, true)
}

// checkQueueRoom refuses, naming the cap and the fixes, when n more pending
// events would pass sync.max_queue_size: the config command that raises the
// cap to the value this import needs (it works whatever the queue holds, even
// when the import alone is over the cap or the board is relay-only and its
// events stay pending), and the push that drains a hub queue.
func checkQueueRoom(ctx context.Context, tx *sql.Tx, n int) error {
	var capRaw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'sync.max_queue_size'`).Scan(&capRaw); err != nil {
		return fmt.Errorf("read sync.max_queue_size: %w", err)
	}
	limit, err := strconv.ParseInt(capRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("parse sync.max_queue_size %q: %w", capRaw, err)
	}
	if limit <= 0 {
		return nil
	}
	var pending int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pending); err != nil {
		return fmt.Errorf("count pending sync_events: %w", err)
	}
	if need := pending + int64(n); need > limit {
		return fmt.Errorf("%w: the import changes tasks that sync, and journaling its %d events on top of %d pending "+
			"would pass sync.max_queue_size (%d); nothing was imported. Raise the cap with: mtix config set "+
			"sync.max_queue_size %d (0 is unlimited), then import again; or run mtix sync push to send the pending "+
			"events first (on a relay-only board they stay pending, so raise the cap instead)",
			model.ErrSyncQueueFull, n, pending, limit, need)
	}
	return nil
}

// journalEmptyBesideTasks reports whether the sync journal holds no event
// while the store holds a task.
func journalEmptyBesideTasks(ctx context.Context, tx *sql.Tx) (bool, error) {
	var empty bool
	// No journal row, and at least one task: the store was filled before any sync.
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM sync_events)
		AND EXISTS (SELECT 1 FROM nodes)`).Scan(&empty); err != nil {
		return false, fmt.Errorf("check the sync journal: %w", err)
	}
	return empty, nil
}

// nameLocalRenumbers names, on a store that syncs, each local task the merge
// moved to another number: no event carries a move, so the hub and the peers
// keep the old number and edits still apply by uid (MTIX-112.17.16.16).
func nameLocalRenumbers(ctx context.Context, tx *sql.Tx, cfg *importConfig, result *ImportResult) error {
	if len(cfg.localMoves.moves) == 0 || cfg.noJournal {
		return nil
	}
	synced, err := storeSyncs(ctx, tx, cfg.asSynced)
	if err != nil || !synced {
		return err
	}
	for _, m := range cfg.localMoves.moves {
		id, idErr := idOfUID(ctx, tx, m.uid)
		if idErr != nil {
			return idErr
		}
		result.JournalLocalOnly = append(result.JournalLocalOnly, LocalOnlyChange{ID: id, Fields: []string{"id (was " + m.oldID + ")"}})
	}
	sort.Slice(result.JournalLocalOnly, func(a, b int) bool { return result.JournalLocalOnly[a].ID < result.JournalLocalOnly[b].ID })
	return nil
}
