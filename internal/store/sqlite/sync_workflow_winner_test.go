// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// Workflow last-writer-wins regressions for MTIX-95.10 (ADR-006 D2; review
// F-28, F-43).
//
// claim, unclaim, transition_status and defer used to apply unconditionally
// at ingest, so any late or older workflow event reverted newer state: an own
// replay, a late-swept event, an out-of-order delivery or a teammate's
// concurrent event. A workflow event now applies only if its key
// (lamport_clock, event_id) beats every workflow event already held for the
// same node. A losing event is still mirrored and recorded as applied, but it
// changes no node column and logs no conflict.

const (
	// lowEventID sorts below every UUIDv7 event id, so an event carrying it
	// loses a Lamport tie.
	lowEventID = "00000000-0000-7000-8000-000000000001"
	// highEventID sorts above every UUIDv7 event id, so an event carrying it
	// wins a Lamport tie.
	highEventID = "ffffffff-ffff-7fff-bfff-ffffffffffff"
	// nullColumn stands for SQL NULL in a nodeRow snapshot.
	nullColumn = "<NULL>"
)

// foreignWallClock is the wall_clock_ts every foreign test event carries. The
// half second checks that closed_at keeps whole seconds only.
func foreignWallClock() time.Time {
	return time.Date(2026, 9, 1, 12, 0, 0, 500_000_000, time.UTC)
}

// foreignClosedAt is closed_at as a replica stamps it from foreignWallClock.
const foreignClosedAt = "2026-09-01T12:00:00Z"

// foreignWorkflowEvent builds a workflow event authored on another replica.
// It carries no uid, like every event a 0.5.x pull returns. An empty eventID
// draws a fresh UUIDv7.
func foreignWorkflowEvent(t *testing.T, nodeID string, op model.OpType, payload any,
	lamport int64, eventID string) *model.SyncEvent {
	t.Helper()
	raw, err := model.EncodePayload(payload)
	require.NoError(t, err)
	if eventID == "" {
		eventID = clock.MustNewEventID()
	}
	return &model.SyncEvent{
		EventID:           eventID,
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            op,
		Payload:           raw,
		WallClockTS:       foreignWallClock().UnixMilli(),
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{"peer-b": lamport},
		AuthorID:          "peer-b",
		AuthorMachineHash: "bbbbbbbbbbbbbbbb",
	}
}

// nodeRow returns every column of one nodes row as text, nullColumn for NULL.
func nodeRow(t *testing.T, raw *sql.DB, id string) map[string]string {
	t.Helper()
	// The whole row, so a test can prove that no column changed.
	rows, err := raw.Query(`SELECT * FROM nodes WHERE id = ?`, id)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	require.NoError(t, err)
	require.True(t, rows.Next(), "node %s must exist", id)
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	require.NoError(t, rows.Scan(ptrs...))
	out := make(map[string]string, len(cols))
	for i, c := range cols {
		out[c] = nullColumn
		if vals[i].Valid {
			out[c] = vals[i].String
		}
	}
	require.NoError(t, rows.Err())
	return out
}

// latestLamport returns the highest lamport_clock among events of op.
func latestLamport(t *testing.T, raw *sql.DB, op model.OpType) int64 {
	t.Helper()
	var l int64
	// Highest Lamport stamp of one op_type in the local log.
	require.NoError(t, raw.QueryRow(
		`SELECT MAX(lamport_clock) FROM sync_events WHERE op_type = ?`, string(op)).Scan(&l))
	return l
}

// claimedThenDone returns a store whose MTIX-1 was claimed by agent-a and then
// marked done locally.
func claimedThenDone(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
	return s, raw
}

// TestApply_LateForeignClaimAfterDone_StatusStaysDone is the late-claim
// regression (ADR-006 D2, T-H13): a claim from another replica whose key is
// below the local done arrives after it. Before MTIX-95.10 the claim applied
// unconditionally and reverted the node to in_progress.
func TestApply_LateForeignClaimAfterDone_StatusStaysDone(t *testing.T) {
	tests := []struct {
		name       string
		lamportOff int64 // added to the local done's Lamport
		eventID    string
	}{
		{"lower lamport", -1, ""},
		{"equal lamport, lower event_id", 0, lowEventID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := claimedThenDone(t)
			before := nodeRow(t, raw, "MTIX-1")
			require.Equal(t, string(model.StatusDone), before["status"])
			require.NotEqual(t, nullColumn, before["closed_at"], "done sets closed_at")
			lamport := latestLamport(t, raw, model.OpTransitionStatus) + tt.lamportOff
			late := foreignWorkflowEvent(t, "MTIX-1", model.OpClaim,
				&model.ClaimPayload{AgentID: "agent-b"}, lamport, tt.eventID)

			pullEvents(t, s, []*model.SyncEvent{late})

			status, closedAt := nodeStatusAndClosedAt(t, raw, "MTIX-1")
			require.Equal(t, string(model.StatusDone), status,
				"a late claim below the local done must not revert it")
			require.True(t, closedAt.Valid)
			require.Equal(t, before["closed_at"], closedAt.String, "closed_at is unchanged")
			require.Equal(t, before, nodeRow(t, raw, "MTIX-1"), "the losing claim changes no column")
		})
	}
}

// TestApply_LateForeignClaimCarryingUID_ScopedByUID_StatusStaysDone checks
// that the winner lookup scopes by uid when the event carries one, as the
// field LWW does: a late claim addressed to a stale display path still finds
// the node's workflow history through its uid.
func TestApply_LateForeignClaimCarryingUID_ScopedByUID_StatusStaysDone(t *testing.T) {
	s, raw := claimedThenDone(t)
	before := nodeRow(t, raw, "MTIX-1")
	late := foreignWorkflowEvent(t, "MTIX-9", model.OpClaim,
		&model.ClaimPayload{AgentID: "agent-b"}, 1, "")
	late.UID = before["uid"]

	pullEvents(t, s, []*model.SyncEvent{late})

	require.Equal(t, before, nodeRow(t, raw, "MTIX-1"),
		"the uid-addressed late claim loses to the node's done")
}

// TestApply_UIDScopedLamportTie_HigherEventIDHolds checks the tie-break on
// the uid-scoped lookup: three claims addressed by uid share one Lamport
// clock, and the one with the highest event_id holds whatever order the
// other two arrive in.
func TestApply_UIDScopedLamportTie_HigherEventIDHolds(t *testing.T) {
	s, raw := replicaWithNode(t)
	uid := nodeRow(t, raw, "MTIX-1")["uid"]
	claim := func(agent, id string) *model.SyncEvent {
		e := foreignWorkflowEvent(t, "MTIX-1", model.OpClaim, &model.ClaimPayload{AgentID: agent}, 5, id)
		e.UID = uid
		return e
	}
	// The highest id first, then the lowest, then one in between: the middle
	// claim must lose to the highest, not beat the lowest.
	pullEvents(t, s, []*model.SyncEvent{
		claim("agent-high", "evt-c"), claim("agent-low", "evt-a"), claim("agent-mid", "evt-b"),
	})

	require.Equal(t, "agent-high", nodeRow(t, raw, "MTIX-1")["assignee"])
}

// TestApply_NewerForeignClaimAfterDone_Applies guards the other side of the
// rule: done is not sticky. A foreign claim whose key beats the local done
// applies, exactly like before MTIX-95.10.
func TestApply_NewerForeignClaimAfterDone_Applies(t *testing.T) {
	s, raw := claimedThenDone(t)
	newer := foreignWorkflowEvent(t, "MTIX-1", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"},
		latestLamport(t, raw, model.OpTransitionStatus)+1, "")

	pullEvents(t, s, []*model.SyncEvent{newer})

	after := nodeRow(t, raw, "MTIX-1")
	require.Equal(t, string(model.StatusInProgress), after["status"])
	require.Equal(t, "agent-b", after["assignee"])
	require.Equal(t, nullColumn, after["closed_at"], "the winning claim clears closed_at")
}

// replicaWithNode returns a fresh store holding one open node MTIX-1.
func replicaWithNode(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	return s, raw
}

// TestApply_ConcurrentClaims_SameWinnerOnAllReplicas: two replicas apply the
// same two concurrent claims in opposite orders. Before MTIX-95.10 each ended
// on whichever claim it applied last.
func TestApply_ConcurrentClaims_SameWinnerOnAllReplicas(t *testing.T) {
	tests := []struct {
		name           string
		lampA, lampB   int64
		idA, idB, want string
	}{
		{"equal lamport: the higher event_id wins", 5, 5, lowEventID, highEventID, "agent-b"},
		{"the higher lamport wins over a higher event_id", 6, 5, lowEventID, highEventID, "agent-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claimA := foreignWorkflowEvent(t, "MTIX-1", model.OpClaim,
				&model.ClaimPayload{AgentID: "agent-a"}, tt.lampA, tt.idA)
			claimB := foreignWorkflowEvent(t, "MTIX-1", model.OpClaim,
				&model.ClaimPayload{AgentID: "agent-b"}, tt.lampB, tt.idB)
			storeA, rawA := replicaWithNode(t)
			storeB, rawB := replicaWithNode(t)

			pullEvents(t, storeA, []*model.SyncEvent{claimA, claimB})
			pullEvents(t, storeB, []*model.SyncEvent{claimB, claimA})

			for name, raw := range map[string]*sql.DB{"replica A": rawA, "replica B": rawB} {
				row := nodeRow(t, raw, "MTIX-1")
				require.Equal(t, string(model.StatusInProgress), row["status"], name)
				require.Equal(t, tt.want, row["assignee"], name)
			}
		})
	}
}

// TestApply_ConcurrentLocalClaims_ReplicasConvergeOnOneClaim runs the claim
// race through real stores: both replicas claim the same node offline at the
// same Lamport, push, and pull each other's claim.
func TestApply_ConcurrentLocalClaims_ReplicasConvergeOnOneClaim(t *testing.T) {
	ctx := context.Background()
	storeA, rawA := replicaWithNode(t)
	storeB, rawB := mutationTestStore(t)
	pullEvents(t, storeB, pushPendingOwnEvents(t, rawA)) // B receives the node

	require.NoError(t, storeA.ClaimNode(ctx, "MTIX-1", "agent-a"))
	require.NoError(t, storeB.ClaimNode(ctx, "MTIX-1", "agent-b"))
	fromA := pushPendingOwnEvents(t, rawA)
	fromB := pushPendingOwnEvents(t, rawB)
	require.Equal(t, fromA[0].LamportClock, fromB[0].LamportClock, "a true Lamport tie")
	want := "agent-a"
	if fromB[0].EventID > fromA[0].EventID {
		want = "agent-b"
	}

	hub := append(append([]*model.SyncEvent{}, fromA...), fromB...)
	pullEvents(t, storeA, hub)
	pullEvents(t, storeB, hub)

	require.Equal(t, want, nodeRow(t, rawA, "MTIX-1")["assignee"], "replica A")
	require.Equal(t, want, nodeRow(t, rawB, "MTIX-1")["assignee"], "replica B")
}

// TestIdempotentApply_OwnClaimReplayedAfterDoneWithoutLocalEventRow_StatusStaysDone
// is S7 with the MTIX-95.2 own-event rule bypassed (defense in depth): the
// claim's local sync_events row is deleted, as in a store restored from a
// backup taken before the claim, so the replayed claim is not recognized as
// held and reaches the winner rule, which must still reject it.
func TestIdempotentApply_OwnClaimReplayedAfterDoneWithoutLocalEventRow_StatusStaysDone(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	pushed := pushPendingOwnEvents(t, raw)
	require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
	before := nodeRow(t, raw, "MTIX-1")
	require.NotEqual(t, nullColumn, before["closed_at"], "done sets closed_at")

	// Seam: forget the claim locally, so the own-event rule cannot catch it.
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'claim'`)
	require.NoError(t, err)

	pullEvents(t, s, pushed)

	status, closedAt := nodeStatusAndClosedAt(t, raw, "MTIX-1")
	require.Equal(t, string(model.StatusDone), status,
		"a replayed claim below the local done must not revert it")
	require.True(t, closedAt.Valid, "a done node keeps closed_at")
	require.Equal(t, before["closed_at"], closedAt.String)
}

// TestIdempotentApply_OwnEventAndOlderForeignWorkflowEvent_ReplicasConverge is
// the regression the MTIX-95.2 review found: replica A claims at Lamport L,
// replica B concurrently changes the status at L-1, and the hub serves them
// in Lamport order, so A pulls [B's event, A's own claim]. A must not end on
// B's older status while B ends on A's claim.
func TestIdempotentApply_OwnEventAndOlderForeignWorkflowEvent_ReplicasConverge(t *testing.T) {
	tests := []struct {
		name  string
		onB   func(ctx context.Context, s *sqlite.Store) error
		wantB model.Status // B's local status before the exchange
	}{
		{"deferred on B", func(ctx context.Context, s *sqlite.Store) error {
			return s.TransitionStatus(ctx, "MTIX-1", model.StatusDeferred, "later", "agent-b")
		}, model.StatusDeferred},
		{"cancelled on B", func(ctx context.Context, s *sqlite.Store) error {
			return s.CancelNode(ctx, "MTIX-1", "not needed", "agent-b", false)
		}, model.StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			storeA, rawA := replicaWithNode(t)
			storeB, rawB := mutationTestStore(t)
			pullEvents(t, storeB, pushPendingOwnEvents(t, rawA)) // both at Lamport 1

			require.NoError(t, tt.onB(ctx, storeB)) // B: Lamport 2
			require.Equal(t, string(tt.wantB), nodeRow(t, rawB, "MTIX-1")["status"])
			setDescription(t, storeA, "MTIX-1", "notes")                   // A: Lamport 2
			require.NoError(t, storeA.ClaimNode(ctx, "MTIX-1", "agent-a")) // A: Lamport 3
			fromA := pushPendingOwnEvents(t, rawA)
			fromB := pushPendingOwnEvents(t, rawB)
			ownClaim := fromA[len(fromA)-1]
			require.Equal(t, model.OpClaim, ownClaim.OpType)
			require.Equal(t, ownClaim.LamportClock-1, fromB[0].LamportClock, "B's event is at L-1")

			// Hub order is Lamport order: A pulls B's event before its own claim.
			hub := []*model.SyncEvent{fromA[0], fromB[0], ownClaim}
			pullEvents(t, storeA, hub)
			pullEvents(t, storeB, hub)

			rowA, rowB := nodeRow(t, rawA, "MTIX-1"), nodeRow(t, rawB, "MTIX-1")
			require.Equal(t, string(model.StatusInProgress), rowA["status"], "A keeps its newer claim")
			require.Equal(t, rowA["status"], rowB["status"], "both replicas end on the same status")
			require.Equal(t, rowA["assignee"], rowB["assignee"])
			require.Equal(t, rowA["closed_at"], rowB["closed_at"])
		})
	}
}

// losingWorkflowEvents returns one workflow event of every op, each keyed
// below a done at Lamport doneLamport.
func losingWorkflowEvents(t *testing.T, doneLamport int64) map[string]*model.SyncEvent {
	t.Helper()
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	low := doneLamport - 1
	return map[string]*model.SyncEvent{
		"claim": foreignWorkflowEvent(t, "MTIX-1", model.OpClaim,
			&model.ClaimPayload{AgentID: "agent-b"}, low, ""),
		"unclaim": foreignWorkflowEvent(t, "MTIX-1", model.OpUnclaim,
			&model.UnclaimPayload{}, low, ""),
		"defer": foreignWorkflowEvent(t, "MTIX-1", model.OpDefer,
			&model.DeferPayload{Reason: "later", Until: &until}, low, ""),
		"transition to cancelled": foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
			&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusCancelled}, low, ""),
		"transition to open, tie on lamport": foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
			&model.TransitionStatusPayload{From: model.StatusDone, To: model.StatusOpen}, doneLamport, lowEventID),
	}
}

// TestApply_LosingWorkflowEvent_MirroredAndRecordedButChangesNothing pins the
// loser contract: the event is mirrored into sync_events and recorded in
// applied_events, but no column of the node changes and no sync_conflicts row
// is written (workflow state is not a user field; a conflict row would be
// noise).
func TestApply_LosingWorkflowEvent_MirroredAndRecordedButChangesNothing(t *testing.T) {
	_, probe := claimedThenDone(t)
	doneLamport := latestLamport(t, probe, model.OpTransitionStatus)
	for name, loser := range losingWorkflowEvents(t, doneLamport) {
		t.Run(name, func(t *testing.T) {
			s, raw := claimedThenDone(t)
			require.Equal(t, doneLamport, latestLamport(t, raw, model.OpTransitionStatus))
			before := nodeRow(t, raw, "MTIX-1")

			pullEvents(t, s, []*model.SyncEvent{loser})

			require.Equal(t, before, nodeRow(t, raw, "MTIX-1"), "no node column changes")
			require.Zero(t, countConflictRows(t, raw), "a losing workflow event logs no conflict")
			require.Equal(t, 1, countRows(t, raw,
				`SELECT COUNT(*) FROM sync_events WHERE event_id = ? AND sync_status = 'applied'`,
				loser.EventID), "the loser is mirrored")
			require.Equal(t, 1, countRows(t, raw,
				`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, loser.EventID),
				"the loser is recorded as applied")
		})
	}
}

// Sentinel column values seeded before a winning event applies, so a test can
// tell a written column from an untouched one.
const (
	seedAssignee   = "seed-agent"
	seedAgentState = "seed-state"
	seedPrevious   = "seed-prev"
	seedClosedAt   = "2000-01-01T00:00:00Z"
	seedDeferUntil = "2000-01-02T00:00:00Z"
	seedUpdatedAt  = "2000-01-03T00:00:00Z"
	seedProgress   = "0.25"
)

// seedWorkflowColumns sets every workflow column of MTIX-1 to a sentinel,
// with status set to status. Raw SQL emits no event, so the node's only
// logged event stays its create_node.
func seedWorkflowColumns(t *testing.T, raw *sql.DB, status model.Status) {
	t.Helper()
	// Test seam: sentinel values in every column a workflow event may write.
	_, err := raw.Exec(`
		UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, previous_status = ?,
		       closed_at = ?, defer_until = ?, updated_at = ?, progress = ?
		 WHERE id = 'MTIX-1'`,
		string(status), seedAssignee, seedAgentState, seedPrevious,
		seedClosedAt, seedDeferUntil, seedUpdatedAt, seedProgress)
	require.NoError(t, err)
}

// transition builds a transition_status payload.
func transition(from, to model.Status) *model.TransitionStatusPayload {
	return &model.TransitionStatusPayload{From: from, To: to, Reason: "test"}
}

// TestApply_WinningWorkflowEvent_WritesExactlyItsTableRow checks every row of
// the workflow winner table (sync_workflow_winner.go) against expectations
// written out by hand: a winning event writes exactly the listed columns, with
// the listed values, plus updated_at; every other column keeps its value.
func TestApply_WinningWorkflowEvent_WritesExactlyItsTableRow(t *testing.T) {
	until := time.Date(2027, 1, 1, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		seed    model.Status
		op      model.OpType
		payload any
		want    map[string]string // the written columns, besides updated_at
	}{
		{"claim", model.StatusOpen, model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"},
			map[string]string{"status": "in_progress", "assignee": "agent-b", "agent_state": "working", "closed_at": nullColumn}},
		{"unclaim", model.StatusInProgress, model.OpUnclaim, &model.UnclaimPayload{},
			map[string]string{"status": "open", "assignee": nullColumn, "agent_state": nullColumn, "closed_at": nullColumn}},
		{"defer with until", model.StatusOpen, model.OpDefer, &model.DeferPayload{Until: &until},
			map[string]string{"status": "deferred", "defer_until": "2027-01-01T09:30:00Z", "closed_at": nullColumn}},
		{"defer without until", model.StatusOpen, model.OpDefer, &model.DeferPayload{},
			map[string]string{"status": "deferred", "defer_until": nullColumn, "closed_at": nullColumn}},
		{"transition to done", model.StatusInProgress, model.OpTransitionStatus,
			transition(model.StatusInProgress, model.StatusDone),
			map[string]string{"status": "done", "closed_at": foreignClosedAt, "progress": "1"}},
		{"transition to cancelled", model.StatusOpen, model.OpTransitionStatus,
			transition(model.StatusOpen, model.StatusCancelled),
			map[string]string{"status": "cancelled", "closed_at": foreignClosedAt}},
		{"transition to invalidated", model.StatusDone, model.OpTransitionStatus,
			transition(model.StatusDone, model.StatusInvalidated),
			map[string]string{"status": "invalidated", "previous_status": "done", "closed_at": foreignClosedAt}},
		{"transition to blocked", model.StatusInProgress, model.OpTransitionStatus,
			transition(model.StatusInProgress, model.StatusBlocked),
			map[string]string{"status": "blocked", "previous_status": "in_progress", "closed_at": nullColumn}},
		{"auto-unblock to open", model.StatusBlocked, model.OpTransitionStatus,
			transition(model.StatusBlocked, model.StatusOpen),
			map[string]string{"status": "open", "previous_status": nullColumn, "closed_at": nullColumn}},
		{"auto-unblock to in_progress", model.StatusBlocked, model.OpTransitionStatus,
			transition(model.StatusBlocked, model.StatusInProgress),
			map[string]string{"status": "in_progress", "previous_status": nullColumn, "closed_at": nullColumn}},
		{"reopen", model.StatusDone, model.OpTransitionStatus,
			transition(model.StatusDone, model.StatusOpen),
			map[string]string{"status": "open", "closed_at": nullColumn}},
		{"start", model.StatusOpen, model.OpTransitionStatus,
			transition(model.StatusOpen, model.StatusInProgress),
			map[string]string{"status": "in_progress", "closed_at": nullColumn}},
		{"transition to deferred", model.StatusInProgress, model.OpTransitionStatus,
			transition(model.StatusInProgress, model.StatusDeferred),
			map[string]string{"status": "deferred", "closed_at": nullColumn}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithNode(t)
			seedWorkflowColumns(t, raw, tt.seed)
			before := nodeRow(t, raw, "MTIX-1")

			pullEvents(t, s, []*model.SyncEvent{foreignWorkflowEvent(t, "MTIX-1", tt.op, tt.payload, 2, "")})

			after := nodeRow(t, raw, "MTIX-1")
			updated, err := time.Parse(time.RFC3339, after["updated_at"])
			require.NoError(t, err, "every winner writes updated_at as RFC3339")
			require.True(t, updated.After(foreignWallClock().Add(-time.Hour)),
				"updated_at is the apply time, not the seeded value")
			for col, v := range before {
				want, written := tt.want[col]
				switch {
				case written:
					require.Equal(t, want, after[col], "column %s is written", col)
				case col != "updated_at":
					require.Equal(t, v, after[col], "column %s is not in the row and must not change", col)
				}
			}
		})
	}
}
