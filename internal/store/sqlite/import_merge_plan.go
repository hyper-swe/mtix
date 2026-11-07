// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	// taken holds the event ids this merge planned already: a second event
	// under the same id would fail the import, so it takes a fresh id.
	taken map[string]bool
	// newTasks holds the tasks the merge created; eligible memoizes eligibleOrNote.
	newTasks map[string]bool
	elig     map[string]bool
}

func (p *mergePlanner) add(id string, op model.OpType, payload any) error {
	return p.addWithID(id, "", op, payload)
}

// addWithID plans an event under a given event id ("" for a fresh one).
func (p *mergePlanner) addWithID(id, eventID string, op model.OpType, payload any) error {
	if eventID != "" {
		if p.taken[eventID] {
			eventID = "" // planned under this id already in this merge
		} else {
			if p.taken == nil {
				p.taken = map[string]bool{}
			}
			p.taken[eventID] = true
		}
	}
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
	if p.j.unblocked[id] {
		// Released by a blocker the merge resolved: a peer derives the release
		// from that transition, so the task keeps the status it held.
		if held, ok := p.j.before[id]; ok {
			n.Status = held.Status
		}
	}
	return n, nil
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

// keepHubKnownUID keeps the uid of a task held on a store that syncs (principle
// D, rounds 3 and 4): nothing the merge does moves the identity of a task a
// peer may know, so the file's other uid for it is ignored, not adopted, on
// every transport. It is not derived from the journal: a relay snapshot clone
// holds tasks with no create row, and peers name them by the uid they hold.
// Adoption stays for a store that does not sync. It names the kept
// uid local only.
func keepHubKnownUID(merged, local *exportNode, ctl mergeControl) {
	if !ctl.keepUIDs || merged.UID == "" || local.UID == "" || merged.UID == local.UID {
		return
	}
	merged.UID = local.UID
	if j := ctl.journal; j != nil {
		j.noteLocalOnly(merged.ID, "uid") // peers know the task by the old uid
		j.keptUID = append(j.keptUID, merged.ID)
	}
}

// newMergePlanner makes the planner for a merge's journal.
func (s *Store) newMergePlanner(ctx context.Context, tx *sql.Tx, j *mergeJournal, wallTS int64) *mergePlanner {
	p := &mergePlanner{s: s, ctx: ctx, tx: tx, wallTS: wallTS, j: j}
	p.newTasks = make(map[string]bool, len(j.created))
	for _, id := range j.created {
		p.newTasks[id] = true
	}
	return p
}

// planMergeEvents turns the merge's changes into events (rule G, round 5): a
// merge journals only field edits and transitions on a task the sync log
// holds, and the links between such tasks. A task the merge created is
// written here and named unsent, with everything under it and every link to
// it: it reaches the hub when its author pushes it.
func (p *mergePlanner) planMergeEvents() ([]emitParams, error) {
	for _, id := range p.j.created {
		p.j.noteUnsent(id)
	}
	if err := p.planChangedTasks(); err != nil {
		return nil, err
	}
	for _, d := range p.j.deps {
		if ok, err := p.depEligible(&d); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		if err := p.add(d.FromID, model.OpLinkDep, &model.LinkDepPayload{DependsOnNodeID: d.ToID, DepType: d.DepType}); err != nil {
			return nil, err
		}
	}
	return p.planned, nil
}

// planChanged plans the values of a changed task that differ: one event per
// field, a transition for a status change, a delete, and new annotations.
func (p *mergePlanner) planChanged(b, a *exportNode) error {
	p.j.noteLocalOnly(a.ID, p.localOnlyColumns(b, a)...)
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
	out := []string{} // an empty list, never nil: nil marshals as the text null
	if raw == "" || json.Unmarshal([]byte(raw), &out) != nil || out == nil {
		return []string{}
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
