// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// Unit tests for the workflow winner rule and column table
// (sync_workflow_winner.go, MTIX-95.10).

func TestWorkflowKeyBeats_Orderings_HigherLamportThenHigherEventID(t *testing.T) {
	tests := []struct {
		name         string
		aLamp        int64
		aID          string
		bLamp        int64
		bID          string
		wantAWinsOnB bool
	}{
		{"higher lamport wins over a higher event_id", 5, "a", 4, "z", true},
		{"lower lamport loses to a lower event_id", 4, "z", 5, "a", false},
		{"lamport tie: higher event_id wins", 5, "b", 5, "a", true},
		{"lamport tie: lower event_id loses", 5, "a", 5, "b", false},
		{"equal keys: no win", 5, "a", 5, "a", false},
		{"byte-string compare, not numeric", 1, "10", 1, "9", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantAWinsOnB, workflowKeyBeats(tt.aLamp, tt.aID, tt.bLamp, tt.bID))
		})
	}
}

func TestIsWorkflowOp_EveryOpType_OnlyWorkflowOpsMatch(t *testing.T) {
	want := map[model.OpType]bool{
		model.OpClaim: true, model.OpUnclaim: true, model.OpTransitionStatus: true, model.OpDefer: true,
	}
	for _, op := range model.AllOpTypes {
		require.Equal(t, want[op], isWorkflowOp(op), "op %s", op)
	}
}

func TestLookupWorkflowRule_TransitionTargets_MatchesSpecificRowsFirst(t *testing.T) {
	tests := []struct {
		name         string
		op           model.OpType
		from, to     model.Status
		wantFound    bool
		wantPrevious workflowAction
	}{
		{"blocked to open clears previous_status", model.OpTransitionStatus,
			model.StatusBlocked, model.StatusOpen, true, wfClear},
		{"blocked to in_progress clears previous_status", model.OpTransitionStatus,
			model.StatusBlocked, model.StatusInProgress, true, wfClear},
		{"done to open keeps previous_status", model.OpTransitionStatus,
			model.StatusDone, model.StatusOpen, true, wfKeep},
		{"open to in_progress keeps previous_status", model.OpTransitionStatus,
			model.StatusOpen, model.StatusInProgress, true, wfKeep},
		{"to blocked records the from-status", model.OpTransitionStatus,
			model.StatusOpen, model.StatusBlocked, true, wfSet},
		{"claim ignores the statuses", model.OpClaim, "", "", true, wfKeep},
		{"unknown to-status has no row", model.OpTransitionStatus, model.StatusOpen, "bogus", false, wfKeep},
		{"empty to-status has no row", model.OpTransitionStatus, model.StatusOpen, "", false, wfKeep},
		{"a non-workflow op has no row", model.OpUpdateField, "", model.StatusOpen, false, wfKeep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, found := lookupWorkflowRule(tt.op, tt.from, tt.to)
			require.Equal(t, tt.wantFound, found)
			require.Equal(t, tt.wantPrevious, rule.previousStatus)
		})
	}
}

// TestWorkflowWinnerTable_EveryRow_WritesClosedAtByTerminality pins the
// invariant that makes closed_at converge: every row writes closed_at, from
// the wall clock for a terminal status and NULL otherwise.
func TestWorkflowWinnerTable_EveryRow_WritesClosedAtByTerminality(t *testing.T) {
	for _, r := range workflowWinnerTable() {
		want := wfClear
		if r.to.IsTerminal() {
			want = wfSet
		}
		require.Equal(t, want, r.closedAt, "row %s %q -> %q", r.op, r.from, r.to)
		require.True(t, isWorkflowOp(r.op), "row op %s is a workflow op", r.op)
		require.True(t, r.to.IsValid(), "row status %q is valid", r.to)
	}
}

func TestResolveWorkflowWrite_NoRule_ReturnsInvalidInput(t *testing.T) {
	_, err := resolveWorkflowWrite(workflowInput{
		op: model.OpTransitionStatus, from: model.StatusOpen, to: "bogus",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
	require.Contains(t, err.Error(), "no workflow winner rule")
}

func TestWallClockSeconds_Milliseconds_TruncatedToWholeSecondsUTC(t *testing.T) {
	ts := time.Date(2026, 9, 1, 12, 0, 59, 999_000_000, time.FixedZone("x", 3600)).UnixMilli()
	require.Equal(t, "2026-09-01T11:00:59Z", wallClockSeconds(ts))
	require.Equal(t, "1970-01-01T00:00:00Z", wallClockSeconds(0))
}

// winnerTestEvent builds a foreign workflow event for the white-box tests.
func winnerTestEvent(t *testing.T, id string, op model.OpType, payload any, lamport int64) *model.SyncEvent {
	t.Helper()
	return &model.SyncEvent{
		EventID: id, ProjectPrefix: "MTIX", NodeID: "MTIX-1", OpType: op,
		Payload: mustEncode(t, payload), WallClockTS: 1_000, LamportClock: lamport,
		VectorClock: model.VectorClock{"peer": lamport}, AuthorID: "peer",
		AuthorMachineHash: "0123456789abcdef",
	}
}

// TestApply_ForeignClaimBelowNewerFieldEdit_StillApplies checks that only
// workflow events compete: a newer update_field on the same node does not
// make an older-but-winning claim lose.
func TestApply_ForeignClaimBelowNewerFieldEdit_StillApplies(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")
	require.NoError(t, applyOnce(t, s, makeUpdateFieldEvent(t,
		"MTIX-1", "alice", "title", `"newer title"`, 9, 100, "0123456789abcdef")))

	require.NoError(t, applyOnce(t, s, winnerTestEvent(t, "claim-1", model.OpClaim,
		&model.ClaimPayload{AgentID: "agent-b"}, 2)))

	require.Equal(t, string(model.StatusInProgress), readNodeColumn(t, raw, "MTIX-1", "status"))
	require.Equal(t, "agent-b", readNodeColumn(t, raw, "MTIX-1", "assignee"))
}

// TestApply_WorkflowWinnerOnMissingNode_ReturnsNotFound keeps the pre-existing
// contract: a winning workflow event for a node that has not arrived fails
// with ErrNotFound (the batch rolls back and is retried).
func TestApply_WorkflowWinnerOnMissingNode_ReturnsNotFound(t *testing.T) {
	s, _ := applyTestStore(t)
	err := applyOnce(t, s, winnerTestEvent(t, "claim-1", model.OpClaim,
		&model.ClaimPayload{AgentID: "agent-b"}, 2))
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrNotFound))
}

func TestApply_TransitionToUnknownStatus_ReturnsInvalidInput(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")
	e := winnerTestEvent(t, "move-1", model.OpTransitionStatus, &model.TransitionStatusPayload{
		From: model.StatusOpen, To: "bogus",
	}, 2)

	err := applyOnce(t, s, e)

	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
	require.Contains(t, err.Error(), e.EventID)
	require.Equal(t, string(model.StatusOpen), readNodeColumn(t, raw, "MTIX-1", "status"))
}

func TestDispatchWithLWW_WorkflowLookupFails_ReturnsWrappedError(t *testing.T) {
	s, _ := applyTestStore(t)
	e := winnerTestEvent(t, "claim-1", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, 2)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Test seam: the lookup table disappears, so the winner query fails.
		if _, err := tx.ExecContext(ctx, `ALTER TABLE sync_events RENAME TO sync_events_moved`); err != nil {
			return err
		}
		return dispatchWithLWW(ctx, tx, e)
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "claim-1")
	require.Contains(t, err.Error(), "workflow winner")
	require.Contains(t, err.Error(), "latest held workflow event")
}

func TestApply_WorkflowColumnWriteFails_ReturnsWrappedError(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")
	// Test seam: refuse every status write on nodes.
	_, err := raw.Exec(`CREATE TRIGGER refuse_status BEFORE UPDATE OF status ON nodes
		BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)

	err = applyOnce(t, s, winnerTestEvent(t, "unclaim-1", model.OpUnclaim, &model.UnclaimPayload{}, 2))

	require.Error(t, err)
	require.Contains(t, err.Error(), "unclaim-1")
	require.Contains(t, err.Error(), "write workflow columns of MTIX-1")
}

// TestApply_LosingMalformedWorkflowEvent_RecordedWithoutDecode documents that a
// losing workflow event is never dispatched, so its payload is not decoded:
// it is mirrored and recorded like any other loser.
func TestApply_LosingMalformedWorkflowEvent_RecordedWithoutDecode(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")
	require.NoError(t, applyOnce(t, s, winnerTestEvent(t, "claim-9", model.OpClaim,
		&model.ClaimPayload{AgentID: "agent-a"}, 9)))
	loser := winnerTestEvent(t, "claim-1", model.OpClaim, &model.ClaimPayload{}, 2)
	loser.Payload = json.RawMessage(`<<<not-json`)

	require.NoError(t, applyOnce(t, s, loser))

	require.Equal(t, "agent-a", readNodeColumn(t, raw, "MTIX-1", "assignee"))
	require.Equal(t, 3, countApplied(t, raw), "the create, the winner and the loser are all recorded")
}
