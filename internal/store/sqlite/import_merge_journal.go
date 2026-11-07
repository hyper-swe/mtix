// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
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
	res      *WorkflowConflict
	journal  *mergeJournal
	keepUIDs bool // a store that syncs keeps the uid of every task it holds
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
	// transition is journaled for it. unblocked holds the tasks the merge
	// released because a blocker it resolved: a peer derives that too.
	blocked   map[string]string
	unblocked map[string]bool
	// localOnly names, per task, the columns the merge changed that no sync
	// event carries, so the import reports them (principle B).
	localOnly map[string][]string
	// keptUID lists the tasks whose local uid the merge kept against the
	// file's, because the hub knows them by it (keepHubKnownUID).
	keptUID []string
	// skippedDeleted lists the tasks deleted here that the file holds live:
	// they stay deleted (round 4).
	skippedDeleted []string
	// unsent holds the tasks the sync log cannot carry events for (planner).
	unsent map[string]bool
}

// noteSkippedDeleted records a task deleted here that the file holds live.
func (j *mergeJournal) noteSkippedDeleted(file *exportNode) {
	if file.DeletedAt == "" {
		j.skippedDeleted = append(j.skippedDeleted, file.ID)
	}
}

// noteUnsent records a task the sync log has no creation for.
func (j *mergeJournal) noteUnsent(id string) {
	if j.unsent == nil {
		j.unsent = map[string]bool{}
	}
	j.unsent[id] = true
}

// unsentList is the unsent tasks, sorted.
func (j *mergeJournal) unsentList() []string {
	out := make([]string, 0, len(j.unsent))
	for id := range j.unsent {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
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

// journalMerge writes the journal rows for what the merge stored, when the
// store syncs (syncedWithHub). It blocks the dependents of the inserted
// blocks links, re-reads every created or changed task from the store, plans
// the events from the stored rows, checks the queue cap once for all of
// them, and writes nothing when the cap would be exceeded: the import's
// transaction rolls back (FR-18, ErrSyncQueueFull). It returns the number of
// events written.
func (s *Store) journalMerge(ctx context.Context, tx *sql.Tx, j *mergeJournal, wallTS int64) (int, error) {
	p := s.newMergePlanner(ctx, tx, j, wallTS)
	if err := p.settleBlocks(); err != nil {
		return 0, err
	}
	planned, err := p.planMergeEvents()
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
// store that has not synced is merged as before and journals nothing. A task
// whose creation the sync log lacks gets no event, nor does anything under it
// (UnsentTasks): backfill sends it.
func (s *Store) mergeAndJournal(ctx context.Context, tx *sql.Tx, data *ExportData, cfg *importConfig) (ImportResult, error) {
	policy := workflowPolicy{resolution: cfg.workflow, now: s.clock()}
	synced, err := storeSyncs(ctx, tx, cfg.asSynced)
	if err != nil {
		return ImportResult{}, err
	}
	policy.keepUIDs = synced
	if synced && !cfg.noJournal {
		policy.journal = &mergeJournal{}
	}
	result, err := mergeAllData(ctx, tx, data, policy)
	if err != nil || policy.journal == nil {
		return result, err
	}
	n, err := s.journalMerge(ctx, tx, policy.journal, policy.now.UnixMilli())
	result.Journaled, result.JournalLocalOnly = n, policy.journal.localOnlyList()
	result.UnsentTasks = policy.journal.unsentList()
	result.NeedsBackfill = len(result.UnsentTasks) > 0
	result.SkippedDeleted = append([]string(nil), policy.journal.skippedDeleted...)
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
	if n == 0 {
		return nil // a merge that changes nothing journals nothing, which never passes the cap
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
