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
	// uidFor maps a task whose uid the merge adopted to the uid the hub knows
	// it by, which its events carry.
	uidFor map[string]string
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
		OpType: op, Payload: raw, WallClockTS: p.wallTS, EventID: eventID, UID: p.uidFor[id]})
	return nil
}

// storedNode re-reads a task from the store, after the merge wrote it.
func (p *mergePlanner) storedNode(id string) (exportNode, error) {
	n, err := scanExportNode(p.tx.QueryRowContext(p.ctx, exportNodeSelectSQL+" WHERE id = ?", id))
	if err != nil {
		return n, fmt.Errorf("read stored node %s: %w", id, err)
	}
	if was, blocked := p.j.blocked[id]; blocked {
		n.Status = was // the peer derives the block from the link_dep event
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

// adoptedUIDKnown notes, for a task whose uid the merge replaced, the uid the
// hub knows it by when its creation is past pending, so the task's events
// carry that uid (a peer does not know the adopted one) and the adoption is
// named local only. A task whose creation is still pending was carried to the
// new uid with its events (carryAdoptedUID).
func (p *mergePlanner) adoptedUIDKnown(b, a *exportNode) error {
	if b.UID == "" || b.UID == a.UID {
		return nil
	}
	var known bool
	// The old uid's creation was pushed or pulled: the hub knows the task by it.
	if err := p.tx.QueryRowContext(p.ctx, `SELECT EXISTS (SELECT 1 FROM sync_events
		WHERE op_type = 'create_node' AND ((uid = ? AND uid <> '') OR event_id = ?) AND sync_status <> 'pending')`,
		b.UID, b.UID).Scan(&known); err != nil {
		return fmt.Errorf("check whether the hub knows %s: %w", format.EventID(b.UID), err)
	}
	if !known {
		return nil
	}
	if p.uidFor == nil {
		p.uidFor = map[string]string{}
	}
	p.uidFor[a.ID] = b.UID
	p.j.noteLocalOnly(a.ID, "uid")
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
	if b.DeletedAt != "" && a.DeletedAt == "" {
		p.j.noteLocalOnly(a.ID, "deleted_at") // no event restores a deleted task
	}
	p.j.noteLocalOnly(a.ID, p.localOnlyColumns(b, a, false)...)
	if err := p.adoptedUIDKnown(b, a); err != nil {
		return err
	}
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

// planStateChange plans a status transition, a deletion and the annotations
// the before copy lacks.
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
	if b.DeletedAt == "" && a.DeletedAt != "" {
		if err := p.add(a.ID, model.OpDelete, &model.DeletePayload{}); err != nil {
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
