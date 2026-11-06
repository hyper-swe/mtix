// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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

// nodeChange is a task the merge changed: its values before and after.
type nodeChange struct{ before, after exportNode }

// mergeJournal collects what a merge wrote, to be journaled once the merge
// has written it (journalMerge). A nil *mergeJournal records nothing.
type mergeJournal struct {
	created []exportNode
	changed []nodeChange
	deps    []exportDep
	// localOnly names the tasks with a change the journal cannot carry (a
	// task the file restores from deletion), so the import reports them.
	localOnly []string
}

func (j *mergeJournal) noteCreated(n *exportNode) {
	if j != nil {
		j.created = append(j.created, *n)
	}
}

func (j *mergeJournal) noteChanged(before, after *exportNode) {
	if j != nil {
		j.changed = append(j.changed, nodeChange{*before, *after})
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

// journalMerge writes the journal rows for what the merge changed, when the
// store syncs (syncedWithHub). It plans every event first, checks the queue
// cap once for all of them, and writes nothing when the cap would be
// exceeded: the import's transaction rolls back (FR-18, ErrSyncQueueFull).
// It returns the ids of tasks whose change stays local only.
func (s *Store) journalMerge(ctx context.Context, tx *sql.Tx, j *mergeJournal, wallTS int64) (int, error) {
	planned, err := s.planMergeEvents(j, wallTS)
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
// journals what it changed in the same transaction (MTIX-112.17.16.16). A
// store that has not synced is merged as before and journals nothing: a later
// backfill still finds an empty journal.
func (s *Store) mergeAndJournal(ctx context.Context, tx *sql.Tx, data *ExportData, cfg *importConfig) (ImportResult, error) {
	policy := workflowPolicy{resolution: cfg.workflow, now: s.clock()}
	synced, err := storeSyncs(ctx, tx, cfg.asSynced)
	if err != nil {
		return ImportResult{}, err
	}
	if synced {
		policy.journal = &mergeJournal{}
	}
	result, err := mergeAllData(ctx, tx, data, policy)
	if err != nil || policy.journal == nil {
		return result, err
	}
	n, err := s.journalMerge(ctx, tx, policy.journal, policy.now.UnixMilli())
	result.Journaled, result.JournalLocalOnly = n, policy.journal.localOnly
	return result, err
}

// planMergeEvents turns the collected changes into events: new tasks parents
// first, then each changed task's values, then new dependencies.
func (s *Store) planMergeEvents(j *mergeJournal, wallTS int64) ([]emitParams, error) {
	var planned []emitParams
	add := func(id string, op model.OpType, payload any) error {
		raw, err := s.encodePayload(payload)
		if err != nil {
			return fmt.Errorf("encode %s payload for %s: %w", op, id, err)
		}
		planned = append(planned, emitParams{NodeID: id, ProjectCode: projectPrefixFromNodeID(id),
			OpType: op, Payload: raw, WallClockTS: wallTS})
		return nil
	}
	created := append([]exportNode(nil), j.created...)
	sort.SliceStable(created, func(a, b int) bool { return created[a].Depth < created[b].Depth })
	for i := range created {
		if err := planCreated(&created[i], add); err != nil {
			return nil, err
		}
	}
	for i := range j.changed {
		if err := planChanged(j, &j.changed[i], add); err != nil {
			return nil, err
		}
	}
	for _, d := range j.deps {
		if err := add(d.FromID, model.OpLinkDep, &model.LinkDepPayload{DependsOnNodeID: d.ToID, DepType: d.DepType}); err != nil {
			return nil, err
		}
	}
	return planned, nil
}

type planFn func(id string, op model.OpType, payload any) error

// planCreated plans a new task: its create, then the state the create
// payload cannot carry (a status other than open, a deletion, annotations).
func planCreated(n *exportNode, add planFn) error {
	if err := add(n.ID, model.OpCreateNode, &model.CreateNodePayload{
		Title: n.Title, ParentID: n.ParentID, NodeType: model.NodeType(n.NodeType),
		IssueType: model.IssueType(n.IssueType), Description: n.Description, Prompt: n.Prompt,
		Acceptance: n.Acceptance, Priority: model.Priority(n.Priority), Labels: labelsOf(n.Labels),
		Assignee: n.Assignee, Creator: n.Creator,
	}); err != nil {
		return err
	}
	zero := exportNode{Status: string(model.StatusOpen)}
	return planStateChange(&zero, n, add)
}

// planChanged plans the values of a changed task that differ: one event per
// field, a transition for a status change, a delete, and new annotations.
func planChanged(j *mergeJournal, c *nodeChange, add planFn) error {
	b, a := &c.before, &c.after
	if b.DeletedAt != "" && a.DeletedAt == "" {
		j.localOnly = append(j.localOnly, a.ID) // no event restores a deleted task
	}
	if err := planAcceptancePrompt(b, a, add); err != nil {
		return err
	}
	for _, f := range changedPlainFields(b, a) {
		raw, err := json.Marshal(f.val)
		if err != nil {
			return fmt.Errorf("marshal %s of %s: %w", f.name, a.ID, err)
		}
		if err := add(a.ID, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: f.name, NewValue: raw}); err != nil {
			return err
		}
	}
	return planStateChange(b, a, add)
}

func planAcceptancePrompt(b, a *exportNode, add planFn) error {
	if b.Acceptance != a.Acceptance {
		if err := add(a.ID, model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: a.Acceptance}); err != nil {
			return err
		}
	}
	if b.Prompt != a.Prompt {
		return add(a.ID, model.OpSetPrompt, &model.SetPromptPayload{PromptText: a.Prompt})
	}
	return nil
}

// planStateChange plans a status transition, a deletion and the annotations
// the before copy lacks.
func planStateChange(b, a *exportNode, add planFn) error {
	if a.Status != b.Status {
		if err := add(a.ID, model.OpTransitionStatus, &model.TransitionStatusPayload{
			From: model.Status(b.Status), To: model.Status(a.Status)}); err != nil {
			return err
		}
	}
	if b.DeletedAt == "" && a.DeletedAt != "" {
		if err := add(a.ID, model.OpDelete, &model.DeletePayload{}); err != nil {
			return err
		}
	}
	held := make(map[string]bool, len(b.Annotations))
	for _, x := range b.Annotations {
		held[x.ID] = true
	}
	for _, x := range a.Annotations {
		if held[x.ID] || x.Text == "" {
			continue
		}
		if err := add(a.ID, model.OpComment, &model.CommentPayload{AuthorID: x.Author, Body: x.Text}); err != nil {
			return err
		}
	}
	return nil
}

type changedField struct {
	name string
	val  any
}

// changedPlainFields lists the update_field values that differ.
func changedPlainFields(b, a *exportNode) []changedField {
	var out []changedField
	if b.IssueType != a.IssueType {
		out = append(out, changedField{"issue_type", a.IssueType})
	}
	if b.Title != a.Title {
		out = append(out, changedField{"title", a.Title})
	}
	if b.Description != a.Description {
		out = append(out, changedField{"description", a.Description})
	}
	if b.Priority != a.Priority {
		out = append(out, changedField{"priority", a.Priority})
	}
	if b.Labels != a.Labels {
		out = append(out, changedField{"labels", labelsOf(a.Labels)})
	}
	if b.Assignee != a.Assignee {
		out = append(out, changedField{"assignee", a.Assignee})
	}
	if b.AgentState != a.AgentState {
		out = append(out, changedField{"agent_state", a.AgentState})
	}
	return out
}

// labelsOf reads the stored labels text (a JSON array) as a list.
func labelsOf(raw string) []string {
	var out []string
	if raw == "" || json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

type queueCheckedKey struct{}

// withQueueChecked marks ctx: the caller checked the queue cap for every
// event it will emit (checkQueueRoom), so emitEvent skips its per-event
// count, which would cost a scan of the queue for each of a large import's
// events.
func withQueueChecked(ctx context.Context) context.Context {
	return context.WithValue(ctx, queueCheckedKey{}, true)
}

// checkQueueRoom refuses, naming the cap and the fix, when n more pending
// events would pass sync.max_queue_size. The fix it names is the push that
// drains the queue: the cap is a store setting no command raises.
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
	if pending+int64(n) > limit {
		return fmt.Errorf("%w: the import changes tasks that sync, and journaling its %d events on top of %d pending "+
			"would pass sync.max_queue_size (%d); nothing was imported. Run mtix sync push to drain the pending events, "+
			"then import again", model.ErrSyncQueueFull, n, pending, limit)
	}
	return nil
}
