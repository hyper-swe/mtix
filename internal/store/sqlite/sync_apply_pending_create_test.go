// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of pull against a number held by a task whose creation is pending
// here (MTIX-95.37). INVARIANT: no teammate event is applied to a local task
// that holds the number under a different uid, nor, when it names the task
// by number alone, to one whose creation the hub has not granted its number.

// TestApply_ForeignEventOnNumberOfPendingLocalCreate_NotApplied: the
// teammate's event is about the teammate's task of that number, never the
// local one. By uid it finds no local task; by number alone it is refused
// while the local creation is pending, and applies once it is on the hub.
func TestApply_ForeignEventOnNumberOfPendingLocalCreate_NotApplied(t *testing.T) {
	tests := []struct {
		name    string
		uid     string
		pushed  bool
		wantErr bool
	}{
		{"uid of the teammate's task, creation pending", "uid-teammate", false, true},
		{"no uid, creation pending", "", false, true},
		{"no uid, creation on the hub", "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithPendingNode(t)
			if tt.pushed {
				markEventsPushed(t, raw)
			}
			before := nodeRow(t, raw, "MTIX-1")
			claim := foreignWorkflowEvent(t, "MTIX-1", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, 9, "")
			claim.UID = tt.uid

			err := applyEventsInTx(s, []*model.SyncEvent{claim})

			if tt.wantErr {
				require.Error(t, err)
				if tt.uid == "" {
					require.ErrorIs(t, err, sqlite.ErrNumberHeldByPendingCreate)
				} else {
					require.ErrorIs(t, err, model.ErrNotFound)
				}
				require.Equal(t, before, nodeRow(t, raw, "MTIX-1"), "the local task is untouched")
				return
			}
			require.NoError(t, err)
			require.Equal(t, "agent-b", nodeRow(t, raw, "MTIX-1")["assignee"])
		})
	}
}

// foreignCreate is a teammate's creation of number id with uid.
func foreignCreate(t *testing.T, id, uid, title string) *model.SyncEvent {
	t.Helper()
	e := foreignWorkflowEvent(t, id, model.OpCreateNode,
		&model.CreateNodePayload{Title: title, NodeType: model.NodeTypeEpic}, 9, uid)
	e.UID = uid
	return e
}

// TestApply_ForeignCreateOnNumberOfPendingLocalCreate_RefusedUntilItMoves: a
// pulled creation of a number a pending local creation holds is refused, so
// it is quarantined and retried, instead of counting as applied and dropped
// for good; once the hub renumbers the local task, the retry applies it.
func TestApply_ForeignCreateOnNumberOfPendingLocalCreate_RefusedUntilItMoves(t *testing.T) {
	s, raw := replicaWithPendingNode(t)
	local := nodeRow(t, raw, "MTIX-1")["uid"]
	create := foreignCreate(t, "MTIX-1", "01a0f66f-7db8-7ef0-ace2-bbc04556f439", "teammate's task")

	err := applyEventsInTx(s, []*model.SyncEvent{create})
	require.ErrorIs(t, err, sqlite.ErrNumberHeldByPendingCreate)
	require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, create.EventID),
		"a refused creation is not recorded as applied")

	_, err = s.RenumberForHubRejection(context.Background(), local)
	require.NoError(t, err)
	require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{create}), "the retry applies it")
	require.Equal(t, "teammate's task", nodeRow(t, raw, "MTIX-1")["title"])
	require.Equal(t, create.UID, nodeRow(t, raw, "MTIX-1")["uid"])
}

// TestApply_ForeignCreateOnNumberOfSyncedLocalTask_KeepsFirstWriter: a number
// held by a task the hub already knows keeps the documented first-writer-wins
// no-op (ADR-003 §6): the creation is dropped without an error.
func TestApply_ForeignCreateOnNumberOfSyncedLocalTask_KeepsFirstWriter(t *testing.T) {
	s, raw := replicaWithPendingNode(t)
	markEventsPushed(t, raw)
	create := foreignCreate(t, "MTIX-1", "01a0f66f-7db8-7ef0-ace2-bbc04556f439", "teammate's task")

	require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{create}))

	require.Equal(t, "Test MTIX-1", nodeRow(t, raw, "MTIX-1")["title"])
}

// TestApply_EveryNodeRefByNumber_RefusedWhenAPendingLocalTaskHoldsIt is the
// class test of MTIX-95.37: every place where apply resolves a node by number
// (a creation's parent_id and its ancestors, a link's or unlink's
// depends_on_node_id, the source node of a link found by number, a node's own
// number for an event without a uid, a delete without a uid) refuses a number
// a pending local task holds, with the one error, and changes nothing.
func TestApply_EveryNodeRefByNumber_RefusedWhenAPendingLocalTaskHoldsIt(t *testing.T) {
	create := func(t *testing.T, id, parent string) *model.SyncEvent {
		e := foreignWorkflowEvent(t, id, model.OpCreateNode,
			&model.CreateNodePayload{Title: "teammate", ParentID: parent, NodeType: model.NodeTypeEpic}, 9, "")
		e.UID = e.EventID
		return e
	}
	link := func(t *testing.T, op model.OpType, from, uid, to string) *model.SyncEvent {
		var payload any = &model.LinkDepPayload{DependsOnNodeID: to, DepType: "blocks"}
		if op == model.OpUnlinkDep {
			payload = &model.UnlinkDepPayload{DependsOnNodeID: to, DepType: "blocks"}
		}
		e := foreignWorkflowEvent(t, from, op, payload, 9, "")
		e.UID = uid
		return e
	}
	tests := []struct {
		name  string
		event func(t *testing.T) *model.SyncEvent
	}{
		{"creation: parent_id is the pending task", func(t *testing.T) *model.SyncEvent { return create(t, "MTIX-1.1", "MTIX-1") }},
		{"creation: parent_id is below the pending task", func(t *testing.T) *model.SyncEvent { return create(t, "MTIX-1.1.1", "MTIX-1.1") }},
		{"creation: parent_id is a task below the pending one that this replica does not hold", func(t *testing.T) *model.SyncEvent {
			return create(t, "MTIX-1.5.2", "MTIX-1.5")
		}},
		{"creation: a grandchild's parent_id, two levels below, not held", func(t *testing.T) *model.SyncEvent {
			return create(t, "MTIX-1.5.2.1", "MTIX-1.5.2")
		}},
		{"link: depends_on is a task below the pending one that is not held", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpLinkDep, "MTIX-2", "uid-b2", "MTIX-1.7.3")
		}},
		{"link: depends_on is the pending task", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpLinkDep, "MTIX-2", "uid-b2", "MTIX-1")
		}},
		{"link: depends_on is below the pending task", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpLinkDep, "MTIX-2", "uid-b2", "MTIX-1.1")
		}},
		{"link: source found by number is the pending task", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpLinkDep, "MTIX-1", "", "MTIX-2")
		}},
		{"link: source with an unknown uid falls back to the pending number", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpLinkDep, "MTIX-1", "uid-unknown", "MTIX-2")
		}},
		{"unlink: depends_on is the pending task", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpUnlinkDep, "MTIX-2", "uid-b2", "MTIX-1")
		}},
		{"unlink: source found by number is the pending task", func(t *testing.T) *model.SyncEvent {
			return link(t, model.OpUnlinkDep, "MTIX-1", "", "MTIX-2")
		}},
		{"delete without a uid names the pending number", func(t *testing.T) *model.SyncEvent {
			return foreignWorkflowEvent(t, "MTIX-1", model.OpDelete, nil, 9, "")
		}},
		{"update without a uid names the pending number", func(t *testing.T) *model.SyncEvent {
			return foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress}, 9, "")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithPendingNode(t) // MTIX-1 pending
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			mustCreateNode(t, s, "MTIX-2", "")
			markEventsPushed(t, raw)
			// MTIX-1 and its child are the pending ones: re-queue their creations.
			_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pending' WHERE op_type = 'create_node' AND node_id IN ('MTIX-1', 'MTIX-1.1')`)
			require.NoError(t, err)
			before := nodeRow(t, raw, "MTIX-1")
			deps := countRows(t, raw, `SELECT COUNT(*) FROM dependencies`)
			nodes := countRows(t, raw, `SELECT COUNT(*) FROM nodes`)

			err = applyEventsInTx(s, []*model.SyncEvent{tt.event(t)})

			require.ErrorIs(t, err, sqlite.ErrNumberHeldByPendingCreate)
			require.Equal(t, before, nodeRow(t, raw, "MTIX-1"), "the local task is untouched")
			require.Equal(t, deps, countRows(t, raw, `SELECT COUNT(*) FROM dependencies`))
			require.Equal(t, nodes, countRows(t, raw, `SELECT COUNT(*) FROM nodes`))
		})
	}
}

// TestApply_EveryNodeRefByNumber_AppliesOnceTheLocalTaskIsOnTheHub: the same
// events apply when no pending creation holds the numbers.
func TestApply_EveryNodeRefByNumber_AppliesOnceTheLocalTaskIsOnTheHub(t *testing.T) {
	s, raw := replicaWithPendingNode(t)
	mustCreateNode(t, s, "MTIX-2", "")
	markEventsPushed(t, raw)
	e := foreignWorkflowEvent(t, "MTIX-2", model.OpLinkDep,
		&model.LinkDepPayload{DependsOnNodeID: "MTIX-1", DepType: "blocks"}, 9, "")
	e.UID = nodeRow(t, raw, "MTIX-2")["uid"]
	require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{e}))
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM dependencies`))
}
