// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// mergePlanner turns the rows a merge stored into journal events
// (MTIX-112.17.16.16). It compares the task as the store held it before with
// the row the merge left, never with the file.
type mergePlanner struct {
	s       *Store
	ctx     context.Context
	tx      *sql.Tx
	wallTS  int64
	planned []emitParams
	j       *mergeJournal
}

func (p *mergePlanner) add(id string, op model.OpType, payload any) error {
	return p.addWithID(id, "", op, payload)
}

// addWithID plans an event under a given event id ("" for a fresh one).
func (p *mergePlanner) addWithID(id, eventID string, op model.OpType, payload any) error {
	raw, err := p.s.encodePayload(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload for %s: %w", op, id, err)
	}
	p.planned = append(p.planned, emitParams{NodeID: id, ProjectCode: projectPrefixFromNodeID(id),
		OpType: op, Payload: raw, WallClockTS: p.wallTS, EventID: eventID})
	return nil
}

// storedNode re-reads a task from the store, after the merge wrote it.
func (p *mergePlanner) storedNode(id string) (exportNode, error) {
	n, err := scanExportNode(p.tx.QueryRowContext(p.ctx, exportNodeSelectSQL+" WHERE id = ?", id))
	if err != nil {
		return n, fmt.Errorf("read stored node %s: %w", id, err)
	}
	if was, blocked := p.j.blocked[id]; blocked {
		// The peer derives the block from the link_dep event, unless the task
		// was blocked already: then the stored status is the held one.
		if held, ok := p.j.before[id]; !ok || held.Status != string(model.StatusBlocked) {
			n.Status = was
		}
	}
	return n, nil
}

// uidEventID returns the event id a created task's create event takes: its
// uid, as every create path does, so the hub's renumber outcome, which names
// the task by that id, finds it (ADR-003 section 2). It is "" (a fresh id)
// when the task has no uid, the uid is not valid text, or an event with that
// id exists already.
func (p *mergePlanner) uidEventID(uid string) (string, error) {
	if uid == "" || !utf8.ValidString(uid) {
		return "", nil
	}
	used, err := p.eventIDUsed(uid)
	if err != nil || used {
		return "", err
	}
	return uid, nil
}

// eventIDUsed reports whether an event with this id is in the journal or was
// applied from the hub.
func (p *mergePlanner) eventIDUsed(id string) (bool, error) {
	var used bool
	// An event row or an applied event with this id exists: it was journaled or pulled before.
	if err := p.tx.QueryRowContext(p.ctx, `SELECT EXISTS (SELECT 1 FROM sync_events WHERE event_id = ?)
		OR EXISTS (SELECT 1 FROM applied_events WHERE event_id = ?)`, id, id).Scan(&used); err != nil {
		return false, fmt.Errorf("check event id %s: %w", format.EventID(id), err)
	}
	return used, nil
}

// keepHubKnownUID keeps the uid of a task the hub holds (principle D, round
// 3): nothing the merge does moves the identity of a task the hub knows, so
// the file's other uid for it is ignored, not adopted. Peers name the task by
// the uid it was created under, and an event under another uid would
// quarantine on every one of them. A task the hub does not hold yet still
// adopts the file's uid, and its pending events follow (carryAdoptedUID). It
// asks only when the merge journals (a store that syncs), and names the
// kept uid local only.
func keepHubKnownUID(ctx context.Context, tx *sql.Tx, merged, local *exportNode, j *mergeJournal) error {
	if j == nil || merged.UID == "" || local.UID == "" || merged.UID == local.UID {
		return nil
	}
	var known bool
	// The local uid's creation was pushed or pulled: the hub knows the task by it.
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sync_events
		WHERE op_type = 'create_node' AND ((uid = ? AND uid <> '') OR event_id = ?) AND sync_status <> 'pending')`,
		local.UID, local.UID).Scan(&known); err != nil {
		return fmt.Errorf("check whether the hub knows %s: %w", format.EventID(local.UID), err)
	}
	if known {
		merged.UID = local.UID
		j.noteLocalOnly(merged.ID, "uid") // peers know the task by the old uid
		j.keptUID = append(j.keptUID, merged.ID)
	}
	return nil
}

// planMergeEvents turns the merge's changes into events: new tasks parents
// first, then each changed task's values, then new dependencies.
func (s *Store) planMergeEvents(ctx context.Context, tx *sql.Tx, j *mergeJournal, wallTS int64) ([]emitParams, error) {
	p := &mergePlanner{s: s, ctx: ctx, tx: tx, wallTS: wallTS, j: j}
	created := make([]exportNode, 0, len(j.created))
	for _, id := range j.created {
		n, err := p.storedNode(id)
		if err != nil {
			return nil, err
		}
		created = append(created, n)
	}
	sort.SliceStable(created, func(a, b int) bool { return created[a].Depth < created[b].Depth })
	for i := range created {
		if err := p.planCreated(&created[i]); err != nil {
			return nil, err
		}
	}
	for _, id := range j.changed {
		after, err := p.storedNode(id)
		if err != nil {
			return nil, err
		}
		before := j.before[id]
		if err := p.planChanged(&before, &after); err != nil {
			return nil, err
		}
	}
	for _, d := range j.deps {
		if err := p.add(d.FromID, model.OpLinkDep, &model.LinkDepPayload{DependsOnNodeID: d.ToID, DepType: d.DepType}); err != nil {
			return nil, err
		}
	}
	return p.planned, nil
}

// planCreated plans a new task: its create, then the state the create
// payload cannot carry (a status other than open, a deletion, annotations).
func (p *mergePlanner) planCreated(n *exportNode) error {
	eventID, err := p.uidEventID(n.UID)
	if err != nil {
		return err
	}
	if err := p.addWithID(n.ID, eventID, model.OpCreateNode, &model.CreateNodePayload{
		Title: n.Title, ParentID: n.ParentID, NodeType: model.NodeType(n.NodeType),
		IssueType: model.IssueType(n.IssueType), Description: n.Description, Prompt: n.Prompt,
		Acceptance: n.Acceptance, Priority: model.Priority(n.Priority), Labels: labelsOf(n.Labels),
		Assignee: n.Assignee, Creator: n.Creator,
	}); err != nil {
		return err
	}
	zero := exportNode{Status: string(model.StatusOpen), Weight: 1}
	p.j.noteLocalOnly(n.ID, p.localOnlyColumns(&zero, n, true)...)
	return p.planStateChange(&zero, n)
}

// planChanged plans the values of a changed task that differ: one event per
// field, a transition for a status change, a delete, and new annotations.
func (p *mergePlanner) planChanged(b, a *exportNode) error {
	if b.DeletedAt != "" {
		// A peer applies an event only to a live task and no event restores a
		// deleted one, so nothing is journaled for a task deleted before the
		// merge: its changed values stay on this machine, named (principle E).
		p.j.noteLocalOnly(a.ID, nodeFieldDiffs(b, a)...)
		p.j.noteLocalOnly(a.ID, p.localOnlyColumns(b, a, false)...)
		return nil
	}
	p.j.noteLocalOnly(a.ID, p.localOnlyColumns(b, a, false)...)
	if err := p.planAcceptancePrompt(b, a); err != nil {
		return err
	}
	for _, f := range changedPlainFields(b, a) {
		raw, err := json.Marshal(f.val)
		if err != nil {
			return fmt.Errorf("marshal %s of %s: %w", f.name, a.ID, err)
		}
		if err := p.add(a.ID, model.OpUpdateField, &model.UpdateFieldPayload{FieldName: f.name, NewValue: raw}); err != nil {
			return err
		}
	}
	return p.planStateChange(b, a)
}

func (p *mergePlanner) planAcceptancePrompt(b, a *exportNode) error {
	if b.Acceptance != a.Acceptance {
		if err := p.add(a.ID, model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: a.Acceptance}); err != nil {
			return err
		}
	}
	if b.Prompt != a.Prompt {
		return p.add(a.ID, model.OpSetPrompt, &model.SetPromptPayload{PromptText: a.Prompt})
	}
	return nil
}

// planStateChange plans a status transition, the annotations the before copy
// lacks and then a deletion, which comes after the comments.
func (p *mergePlanner) planStateChange(b, a *exportNode) error {
	if p.deferJournaled(b, a) { // the defer op carries the wake time a transition lacks
		if err := p.add(a.ID, model.OpDefer, deferPayload(a)); err != nil {
			return err
		}
	} else if a.Status != b.Status {
		if err := p.add(a.ID, model.OpTransitionStatus, &model.TransitionStatusPayload{
			From: model.Status(b.Status), To: model.Status(a.Status)}); err != nil {
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
		if err := p.planComment(a.ID, &x); err != nil {
			return err
		}
	}
	if b.DeletedAt == "" && a.DeletedAt != "" { // last: a peer applies a comment only to a live task
		return p.add(a.ID, model.OpDelete, &model.DeletePayload{})
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

// planComment plans a comment event under the annotation's own id, which the
// hub and the peers apply as the annotation id, so a comment they hold
// already is not added twice. An id that is not valid text, or an event the
// journal or the applied events hold already, plans nothing or a fresh id.
func (p *mergePlanner) planComment(nodeID string, x *model.Annotation) error {
	eventID := ""
	if x.ID != "" && utf8.ValidString(x.ID) {
		used, err := p.eventIDUsed(x.ID)
		if err != nil {
			return err
		}
		if used {
			return nil // this comment's event is in the journal or was applied: the hub has it
		}
		eventID = x.ID
	}
	return p.addWithID(nodeID, eventID, model.OpComment, &model.CommentPayload{AuthorID: x.Author, Body: x.Text})
}
